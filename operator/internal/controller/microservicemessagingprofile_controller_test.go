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
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	messagingv1alpha1 "thesis.local/messaging-operator/api/v1alpha1"
	"thesis.local/messaging-operator/internal/rabbitmq"
)

// fakeBroker is a management API that records the paths it was asked for and
// can be told to fail.
type fakeBroker struct {
	mu     sync.Mutex
	paths  []string
	server *httptest.Server
	fail   bool
}

func newFakeBroker() *fakeBroker {
	b := new(fakeBroker)
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.Method+" "+r.URL.EscapedPath())
		failing := b.fail
		b.mu.Unlock()

		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"broker down"}`))
			return
		}
		if r.Method == http.MethodGet {
			// Bindings read as a list, a queue reads as an object. Reply with
			// whichever the path asks for.
			if len(r.URL.EscapedPath()) > 14 && r.URL.EscapedPath()[:14] == "/api/bindings/" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`{"leader":"rabbit@server-0","members":["a","b","c"]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	return b
}

func (b *fakeBroker) called(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.paths {
		if p == path {
			return true
		}
	}
	return false
}

func (b *fakeBroker) setFailing(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = v
}

var _ = Describe("MicroserviceMessagingProfile Controller", func() {
	const (
		name      = "orders-service"
		namespace = "default"
		vhost     = "orders"
	)

	var (
		ctx        context.Context
		broker     *fakeBroker
		reconciler *MicroserviceMessagingProfileReconciler
		key        types.NamespacedName
		profile    *messagingv1alpha1.MicroserviceMessagingProfile
	)

	BeforeEach(func() {
		ctx = context.Background()
		broker = newFakeBroker()
		DeferCleanup(broker.server.Close)

		reconciler = &MicroserviceMessagingProfileReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			NewBroker: func(context.Context, *messagingv1alpha1.MicroserviceMessagingProfile) (*rabbitmq.Client, error) {
				return rabbitmq.New(broker.server.URL, "guest", "guest"), nil
			},
		}

		key = types.NamespacedName{Name: name, Namespace: namespace}
		profile = &messagingv1alpha1.MicroserviceMessagingProfile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: messagingv1alpha1.MicroserviceMessagingProfileSpec{
				ClusterRef: messagingv1alpha1.ClusterReference{
					Name: "rabbit-ha", Namespace: "messaging",
				},
				Vhost: vhost,
				Queues: []messagingv1alpha1.QueueSpec{
					{Name: "orders.created", RoutingKey: "order.created"},
					{Name: "orders.shipped", RoutingKey: "order.shipped"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, profile)).To(Succeed())
	})

	AfterEach(func() {
		current := new(messagingv1alpha1.MicroserviceMessagingProfile)
		if err := k8sClient.Get(ctx, key, current); err == nil {
			current.Finalizers = nil
			Expect(k8sClient.Update(ctx, current)).To(Succeed())
			Expect(k8sClient.Delete(ctx, current)).To(Succeed())
		}
	})

	reconcileOnce := func() reconcile.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	It("provisions the vhost, the queues and their dead letter queues", func() {
		reconcileOnce()

		Expect(broker.called("PUT /api/vhosts/orders")).To(BeTrue())
		Expect(broker.called("PUT /api/exchanges/orders/orders-service")).To(BeTrue())
		for _, q := range []string{"orders.created", "orders.shipped"} {
			Expect(broker.called("PUT /api/queues/orders/"+q)).To(BeTrue(), q)
			// Every queue gets a dead letter queue without the developer
			// asking for one.
			Expect(broker.called("PUT /api/queues/orders/"+q+".dlq")).To(BeTrue(), q+".dlq")
		}
	})

	It("reports Ready with the live Raft state of each queue", func() {
		reconcileOnce()

		updated := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())

		Expect(updated.Status.Conditions).To(HaveLen(1))
		Expect(updated.Status.Conditions[0].Type).To(Equal(messagingv1alpha1.ConditionReady))
		Expect(updated.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
		Expect(updated.Status.Conditions[0].Reason).To(Equal(messagingv1alpha1.ReasonProvisioned))

		Expect(updated.Status.Queues).To(HaveLen(2))
		for _, q := range updated.Status.Queues {
			Expect(q.Replicas).To(Equal(3), q.Name)
			Expect(q.Leader).To(Equal("rabbit@server-0"), q.Name)
			Expect(q.DeadLetterQueue).To(Equal(q.Name + ".dlq"))
		}
	})

	It("writes a Secret the microservice can mount, owned by the profile", func() {
		reconcileOnce()

		secret := new(corev1.Secret)
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: name + "-messaging", Namespace: namespace,
		}, secret)).To(Succeed())

		Expect(secret.Data).To(HaveKey("AMQP_URI"))
		Expect(string(secret.Data["AMQP_USERNAME"])).To(Equal(namespace + "-" + name))
		Expect(string(secret.Data["AMQP_VHOST"])).To(Equal(vhost))
		Expect(secret.OwnerReferences).To(HaveLen(1))
		Expect(secret.OwnerReferences[0].Kind).To(Equal("MicroserviceMessagingProfile"))
	})

	It("keeps the same password across reconciles", func() {
		reconcileOnce()

		secretKey := types.NamespacedName{Name: name + "-messaging", Namespace: namespace}
		first := new(corev1.Secret)
		Expect(k8sClient.Get(ctx, secretKey, first)).To(Succeed())
		firstPassword := string(first.Data["AMQP_PASSWORD"])
		Expect(firstPassword).NotTo(BeEmpty())

		reconcileOnce()

		second := new(corev1.Secret)
		Expect(k8sClient.Get(ctx, secretKey, second)).To(Succeed())
		// A new password on every pass would break every running consumer.
		Expect(string(second.Data["AMQP_PASSWORD"])).To(Equal(firstPassword))
	})

	It("adds the cleanup finalizer", func() {
		reconcileOnce()

		updated := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Finalizers).To(ContainElement(messagingv1alpha1.Finalizer))
	})

	It("reports NotReady when the broker is unreachable", func() {
		broker.setFailing(true)

		result := reconcileOnce()
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		updated := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
		Expect(updated.Status.Conditions[0].Reason).To(Equal(messagingv1alpha1.ReasonBrokerUnreachable))
	})

	It("reports ClusterSecretNotFound when the broker credentials are missing", func() {
		// Every other spec injects a fake broker, so the real brokerClient -
		// the code that reads <cluster>-default-user and builds the client -
		// is never exercised. Drop the injection and point the profile at a
		// cluster whose Secret does not exist.
		reconciler.NewBroker = nil

		result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		updated := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Conditions).NotTo(BeEmpty())
		Expect(updated.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
		Expect(updated.Status.Conditions[0].Reason).
			To(Equal(messagingv1alpha1.ReasonClusterSecretMissing))
	})

	It("holds the finalizer when cleanup cannot reach the broker", func() {
		reconcileOnce()

		current := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		Expect(k8sClient.Delete(ctx, current)).To(Succeed())

		// A broker that fails mid-cleanup must not let the object vanish with
		// the user still valid, which is the whole point of the finalizer.
		broker.setFailing(true)
		result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		still := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, still)).To(Succeed())
		Expect(still.Finalizers).To(ContainElement(messagingv1alpha1.Finalizer))

		// Once the broker answers again, cleanup completes and the object goes.
		broker.setFailing(false)
		reconcileOnce()
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, still))).To(BeTrue())
	})

	It("revokes the user and removes the queues on delete", func() {
		reconcileOnce()

		current := new(messagingv1alpha1.MicroserviceMessagingProfile)
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		Expect(k8sClient.Delete(ctx, current)).To(Succeed())

		reconcileOnce()

		// The credentials go first. A half finished cleanup must not leave a
		// working user behind.
		Expect(broker.called("DELETE /api/users/default-orders-service")).To(BeTrue())
		Expect(broker.called("DELETE /api/queues/orders/orders.created")).To(BeTrue())
		Expect(broker.called("DELETE /api/queues/orders/orders.created.dlq")).To(BeTrue())
		Expect(broker.called("DELETE /api/exchanges/orders/orders-service")).To(BeTrue())
		// The vhost is shared, so it survives on purpose.
		Expect(broker.called("DELETE /api/vhosts/orders")).To(BeFalse())

		gone := new(messagingv1alpha1.MicroserviceMessagingProfile)
		err := k8sClient.Get(ctx, key, gone)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the finalizer should have been removed")
	})
})
