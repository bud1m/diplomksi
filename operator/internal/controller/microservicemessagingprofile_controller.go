/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	messagingv1alpha1 "thesis.local/messaging-operator/api/v1alpha1"
	"thesis.local/messaging-operator/internal/rabbitmq"
)

// maxParallelQueues caps how many queues are provisioned at once. A profile
// with fifty queues must not open fifty connections to the broker.
const maxParallelQueues = 4

// MicroserviceMessagingProfileReconciler provisions one profile into RabbitMQ.
type MicroserviceMessagingProfileReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NewBroker builds the client for one profile. It defaults to the real
	// broker. Tests replace it with a client pointed at a fake server, which
	// is the only seam the reconciler needs to be testable.
	NewBroker func(context.Context, *messagingv1alpha1.MicroserviceMessagingProfile) (*rabbitmq.Client, error)
}

// +kubebuilder:rbac:groups=messaging.thesis.local,resources=microservicemessagingprofiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=messaging.thesis.local,resources=microservicemessagingprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=messaging.thesis.local,resources=microservicemessagingprofiles/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one profile towards the state its spec asks for.
func (r *MicroserviceMessagingProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	profile := new(messagingv1alpha1.MicroserviceMessagingProfile)
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	newBroker := r.NewBroker
	if newBroker == nil {
		newBroker = r.brokerClient
	}

	broker, err := newBroker(ctx, profile)
	if err != nil {
		r.setNotReady(profile, messagingv1alpha1.ReasonClusterSecretMissing, err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, profile)
	}

	if !profile.DeletionTimestamp.IsZero() {
		return r.cleanup(ctx, profile, broker)
	}

	if controllerutil.AddFinalizer(profile, messagingv1alpha1.Finalizer) {
		if err := r.Update(ctx, profile); err != nil {
			return ctrl.Result{}, err
		}
	}

	// The vhost must exist before anything inside it.
	if err := broker.EnsureVhost(ctx, profile.Spec.Vhost); err != nil {
		return r.failed(ctx, profile, messagingv1alpha1.ReasonBrokerUnreachable, err)
	}
	if err := broker.EnsureExchange(ctx, profile.Spec.Vhost, profile.ExchangeName()); err != nil {
		return r.failed(ctx, profile, messagingv1alpha1.ReasonBrokerUnreachable, err)
	}

	queueStatuses, queueErrs := r.provisionQueues(ctx, profile, broker)

	if err := r.ensureUserAndSecret(ctx, profile, broker); err != nil {
		return r.failed(ctx, profile, messagingv1alpha1.ReasonBrokerUnreachable, err)
	}

	profile.Status.ObservedGeneration = profile.Generation
	profile.Status.Vhost = profile.Spec.Vhost
	profile.Status.Exchange = profile.ExchangeName()
	profile.Status.SecretName = profile.SecretName()
	profile.Status.Username = profile.UserName()
	profile.Status.Queues = queueStatuses

	if len(queueErrs) > 0 {
		// Report the rest as provisioned rather than hiding the partial success.
		r.setNotReady(profile, messagingv1alpha1.ReasonPartialFailure,
			fmt.Sprintf("%d of %d queues failed: %v",
				len(queueErrs), len(profile.Spec.Queues), queueErrs[0]))
		logger.Error(queueErrs[0], "queue provisioning failed", "failures", len(queueErrs))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, profile)
	}

	r.setReady(profile)
	return ctrl.Result{}, r.Status().Update(ctx, profile)
}

// provisionQueues creates every queue, its dead letter queue and its binding.
//
// This is the parallel step. One goroutine per queue, a buffered channel as a
// semaphore, a WaitGroup to join, and a mutex to guard the shared slices. The
// loop collects every error instead of returning the first, because a partial
// failure must still report which queues succeeded.
func (r *MicroserviceMessagingProfileReconciler) provisionQueues(
	ctx context.Context,
	profile *messagingv1alpha1.MicroserviceMessagingProfile,
	broker *rabbitmq.Client,
) ([]messagingv1alpha1.QueueStatus, []error) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses []messagingv1alpha1.QueueStatus
		errs     []error
		sem      = make(chan struct{}, maxParallelQueues)
	)

	vhost := profile.Spec.Vhost
	exchange := profile.ExchangeName()

	for _, q := range profile.Spec.Queues {
		wg.Add(1)
		go func(q messagingv1alpha1.QueueSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dlq := messagingv1alpha1.DeadLetterQueueName(q.Name)

			// The dead letter queue comes first. A main queue that points at a
			// queue which does not exist yet would drop its rejected messages.
			if err := broker.EnsureQueue(ctx, vhost, dlq, ""); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("queue %s: %w", dlq, err))
				mu.Unlock()
				return
			}
			if err := broker.EnsureQueue(ctx, vhost, q.Name, dlq); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("queue %s: %w", q.Name, err))
				mu.Unlock()
				return
			}
			if err := broker.EnsureBinding(ctx, vhost, exchange, q.Name, q.RoutingKey); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("binding %s: %w", q.Name, err))
				mu.Unlock()
				return
			}

			// Read the live state back. The replica count and the leader are
			// the evidence that this really is a replicated quorum queue.
			status := messagingv1alpha1.QueueStatus{Name: q.Name, DeadLetterQueue: dlq}
			if info, err := broker.GetQueue(ctx, vhost, q.Name); err == nil {
				status.Replicas = len(info.Members)
				status.Leader = info.Leader
			}

			mu.Lock()
			statuses = append(statuses, status)
			mu.Unlock()
		}(q)
	}

	wg.Wait()
	return statuses, errs
}

// ensureUserAndSecret creates the scoped broker user and writes the connection
// details into a Secret the microservice can mount.
func (r *MicroserviceMessagingProfileReconciler) ensureUserAndSecret(
	ctx context.Context,
	profile *messagingv1alpha1.MicroserviceMessagingProfile,
	broker *rabbitmq.Client,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      profile.SecretName(),
			Namespace: profile.Namespace,
		},
	}

	// Keep the existing password if the Secret is already there. A new password
	// on every reconcile would break every running consumer.
	password := ""
	existing := new(corev1.Secret)
	err := r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existing)
	switch {
	case err == nil:
		password = string(existing.Data["AMQP_PASSWORD"])
	case !apierrors.IsNotFound(err):
		return err
	}
	if password == "" {
		if password, err = randomPassword(); err != nil {
			return err
		}
	}

	user := profile.UserName()
	if err := broker.EnsureUser(ctx, user, password); err != nil {
		return err
	}
	if err := broker.SetPermissions(ctx, profile.Spec.Vhost, user); err != nil {
		return err
	}

	host := fmt.Sprintf("%s.%s.svc", profile.Spec.ClusterRef.Name, profile.Spec.ClusterRef.Namespace)
	uri := fmt.Sprintf("amqp://%s:%s@%s:5672/%s", user, password, host, profile.Spec.Vhost)

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.StringData = map[string]string{
			"AMQP_URI":      uri,
			"AMQP_HOST":     host,
			"AMQP_PORT":     "5672",
			"AMQP_VHOST":    profile.Spec.Vhost,
			"AMQP_USERNAME": user,
			"AMQP_PASSWORD": password,
			"AMQP_EXCHANGE": profile.ExchangeName(),
		}
		return controllerutil.SetControllerReference(profile, secret, r.Scheme)
	})
	return err
}

// cleanup revokes the user and removes the queues and the exchange. It keeps
// the vhost, because a second profile may share it.
func (r *MicroserviceMessagingProfileReconciler) cleanup(
	ctx context.Context,
	profile *messagingv1alpha1.MicroserviceMessagingProfile,
	broker *rabbitmq.Client,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(profile, messagingv1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	// Revoke the credentials first. Leaked credentials are the worst outcome.
	if err := broker.DeleteUser(ctx, profile.UserName()); err != nil {
		logger.Error(err, "cannot revoke the broker user, holding the finalizer")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	for _, q := range profile.Spec.Queues {
		if err := broker.DeleteQueue(ctx, profile.Spec.Vhost, q.Name); err != nil {
			logger.Error(err, "cannot delete queue, holding the finalizer", "queue", q.Name)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		dlq := messagingv1alpha1.DeadLetterQueueName(q.Name)
		if err := broker.DeleteQueue(ctx, profile.Spec.Vhost, dlq); err != nil {
			logger.Error(err, "cannot delete queue, holding the finalizer", "queue", dlq)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}
	if err := broker.DeleteExchange(ctx, profile.Spec.Vhost, profile.ExchangeName()); err != nil {
		logger.Error(err, "cannot delete the exchange, holding the finalizer")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	controllerutil.RemoveFinalizer(profile, messagingv1alpha1.Finalizer)
	return ctrl.Result{}, r.Update(ctx, profile)
}

// brokerClient builds a client from the user the Cluster Operator generated.
func (r *MicroserviceMessagingProfileReconciler) brokerClient(
	ctx context.Context,
	profile *messagingv1alpha1.MicroserviceMessagingProfile,
) (*rabbitmq.Client, error) {
	ref := profile.Spec.ClusterRef
	secret := new(corev1.Secret)
	key := types.NamespacedName{Name: ref.Name + "-default-user", Namespace: ref.Namespace}
	if err := r.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}

	url := fmt.Sprintf("http://%s.%s.svc:15672", ref.Name, ref.Namespace)
	return rabbitmq.New(url, string(secret.Data["username"]), string(secret.Data["password"])), nil
}

func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (r *MicroserviceMessagingProfileReconciler) failed(
	ctx context.Context,
	profile *messagingv1alpha1.MicroserviceMessagingProfile,
	reason string,
	cause error,
) (ctrl.Result, error) {
	r.setNotReady(profile, reason, cause.Error())
	if err := r.Status().Update(ctx, profile); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *MicroserviceMessagingProfileReconciler) setReady(p *messagingv1alpha1.MicroserviceMessagingProfile) {
	setCondition(p, metav1.ConditionTrue, messagingv1alpha1.ReasonProvisioned,
		"every object exists in the broker")
}

func (r *MicroserviceMessagingProfileReconciler) setNotReady(
	p *messagingv1alpha1.MicroserviceMessagingProfile, reason, message string,
) {
	setCondition(p, metav1.ConditionFalse, reason, message)
}

func setCondition(
	p *messagingv1alpha1.MicroserviceMessagingProfile,
	status metav1.ConditionStatus, reason, message string,
) {
	condition := metav1.Condition{
		Type:               messagingv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: p.Generation,
	}
	for i, existing := range p.Status.Conditions {
		if existing.Type == condition.Type {
			if existing.Status == condition.Status {
				condition.LastTransitionTime = existing.LastTransitionTime
			}
			p.Status.Conditions[i] = condition
			return
		}
	}
	p.Status.Conditions = append(p.Status.Conditions, condition)
}

// SetupWithManager registers the controller.
func (r *MicroserviceMessagingProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&messagingv1alpha1.MicroserviceMessagingProfile{}).
		Owns(&corev1.Secret{}).
		Named("microservicemessagingprofile").
		Complete(r)
}
