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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MicroserviceMessagingProfile is the single object a developer writes to get a
// complete messaging setup. See docs/plans for the design and the rationale.

// ClusterReference points at the RabbitmqCluster this profile provisions into.
type ClusterReference struct {
	// Name of the RabbitmqCluster.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the RabbitmqCluster.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
}

// QueueSpec is one queue the microservice consumes. The operator gives every
// queue a dead letter queue of its own.
type QueueSpec struct {
	// Name of the queue inside the vhost.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]+$`
	Name string `json:"name"`

	// RoutingKey binds the queue to the exchange. It accepts the AMQP topic
	// wildcards, so "order.*" and "order.#" are both valid.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	RoutingKey string `json:"routingKey"`
}

// MicroserviceMessagingProfileSpec defines the desired messaging setup.
type MicroserviceMessagingProfileSpec struct {
	// ClusterRef is the broker to provision into.
	ClusterRef ClusterReference `json:"clusterRef"`

	// Vhost isolates this profile from every other tenant of the broker. The
	// operator creates it if it does not exist. The operator does not delete
	// it, because a second profile may share it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]+$`
	Vhost string `json:"vhost"`

	// Queues the microservice consumes. Each one becomes a quorum queue with a
	// dead letter queue beside it.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	Queues []QueueSpec `json:"queues"`
}

// QueueStatus reports what the broker holds now, not what the spec asked for.
// Replicas and Leader come from a live read of the management API. They are
// the evidence that the queue really is a replicated quorum queue.
type QueueStatus struct {
	// Name of the main queue.
	Name string `json:"name"`

	// DeadLetterQueue that receives the rejected messages of this queue.
	DeadLetterQueue string `json:"deadLetterQueue"`

	// Replicas is the number of Raft members of the quorum queue.
	// +optional
	Replicas int `json:"replicas,omitempty"`

	// Leader is the broker node that currently holds the Raft leadership.
	// +optional
	Leader string `json:"leader,omitempty"`
}

// MicroserviceMessagingProfileStatus reports the observed state.
type MicroserviceMessagingProfileStatus struct {
	// ObservedGeneration is the spec generation this status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Vhost the operator provisioned.
	// +optional
	Vhost string `json:"vhost,omitempty"`

	// Exchange the operator provisioned.
	// +optional
	Exchange string `json:"exchange,omitempty"`

	// SecretName holds the connection details for the microservice.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// Username of the scoped broker user. The password lives in the Secret.
	// +optional
	Username string `json:"username,omitempty"`

	// Queues reports the live state of each queue.
	// +optional
	// +listType=map
	// +listMapKey=name
	Queues []QueueStatus `json:"queues,omitempty"`

	// Conditions holds Ready, with a reason that names the failing step.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types and reasons used by the controller.
const (
	// ConditionReady is true once every object exists in the broker and the
	// Secret is written.
	ConditionReady = "Ready"

	ReasonProvisioned          = "Provisioned"
	ReasonBrokerUnreachable    = "BrokerUnreachable"
	ReasonClusterSecretMissing = "ClusterSecretNotFound"
	ReasonPartialFailure       = "PartialFailure"
	ReasonInvalidSpec          = "InvalidSpec"
	ReasonDeleting             = "Deleting"
)

// Finalizer holds the object until the operator revokes the broker user and
// removes the queues it created.
const Finalizer = "messaging.thesis.local/cleanup"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mmp
// +kubebuilder:printcolumn:name="Vhost",type=string,JSONPath=`.spec.vhost`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MicroserviceMessagingProfile is the Schema for the microservicemessagingprofiles API.
type MicroserviceMessagingProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MicroserviceMessagingProfileSpec   `json:"spec,omitempty"`
	Status MicroserviceMessagingProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MicroserviceMessagingProfileList contains a list of MicroserviceMessagingProfile.
type MicroserviceMessagingProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MicroserviceMessagingProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion,
			&MicroserviceMessagingProfile{},
			&MicroserviceMessagingProfileList{},
		)
		return nil
	})
}

// ExchangeName is the topic exchange the operator creates for this profile.
// It is derived, not configured, so two profiles cannot collide.
func (p *MicroserviceMessagingProfile) ExchangeName() string {
	return p.Name
}

// UserName is the scoped broker user for this profile.
func (p *MicroserviceMessagingProfile) UserName() string {
	return p.Namespace + "-" + p.Name
}

// SecretName is the Kubernetes Secret that carries the connection details.
func (p *MicroserviceMessagingProfile) SecretName() string {
	return p.Name + "-messaging"
}

// DeadLetterQueueName is the dead letter queue that belongs to queue name.
func DeadLetterQueueName(queue string) string {
	return queue + ".dlq"
}
