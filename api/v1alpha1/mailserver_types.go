/*
Copyright 2026 Rocket Pad Platforms GmbH.

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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
)

// MailServerSpec defines the desired state of a MailServer.
//
// A MailServer is a tenant-facing mail system: inbound and submission
// services, outbound delivery, mailboxes, and the set of domains it
// handles. Each MailServer is isolated from all others: own workloads,
// queues, IPs, DKIM keys and Rspamd/Valkey instances.
type MailServerSpec struct {
	// Directory used for recipient validation and user authentication.
	// References a MailDirectory in the same namespace.
	// +kubebuilder:validation:Required
	DirectoryRef NameReference `json:"directoryRef"`

	// Outbound selects the outbound delivery mode.
	// +kubebuilder:validation:Required
	Outbound Outbound `json:"outbound"`

	// Domains handled by this mail server. They are signed with DKIM
	// (when enabled on the domain) and accepted for submission.
	// Each entry references a MailDomain in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y == x).size() == 1)",message="domain references must be unique"
	Domains []NameReference `json:"domains"`

	// TLS for inbound, submission and outbound mail. The certificate
	// is managed by cert-manager from the referenced issuer.
	// +kubebuilder:validation:Required
	TLS TLS `json:"tls"`

	// Inbound mail (port 25) service settings.
	// +optional
	Inbound *Inbound `json:"inbound,omitempty"`

	// Submission (port 587) settings.
	// +optional
	Submission *Submission `json:"submission,omitempty"`

	// Mailboxes configures Dovecot mail storage (IMAP, LMTP delivery).
	// When disabled while submission is enabled, Dovecot runs in
	// auth-only mode.
	// +optional
	Mailboxes *Mailboxes `json:"mailboxes,omitempty"`

	// Postfix workload settings.
	// +optional
	Postfix *Postfix `json:"postfix,omitempty"`

	// Rspamd workload settings.
	// +optional
	Rspamd *Rspamd `json:"rspamd,omitempty"`

	// Valkey (Rspamd state) settings.
	// +optional
	Valkey *Valkey `json:"valkey,omitempty"`

	// Availability settings (disruption budget, topology spread).
	// +optional
	Availability *Availability `json:"availability,omitempty"`

	// Observability settings.
	// +optional
	Observability *Observability `json:"observability,omitempty"`

	// Backup settings (k8up annotations on managed resources).
	// +optional
	Backup *Backup `json:"backup,omitempty"`
}

// Outbound selects the outbound delivery mode.
// Exactly one of direct or relay must be set.
//
// +kubebuilder:validation:XValidation:rule="has(self.direct) != has(self.relay)",message="exactly one of direct or relay must be set"
type Outbound struct {
	// Deliver directly from the mail server's own sending addresses
	// (reputation is tied to those addresses).
	// +optional
	Direct *DirectOutbound `json:"direct,omitempty"`

	// Deliver via a smart host.
	// +optional
	Relay *RelayOutbound `json:"relay,omitempty"`
}

// DirectOutbound delivers mail directly.
type DirectOutbound struct {
	// Reference to a MailIPPoolClaim providing the sending addresses.
	// Optional: without a claim, mail leaves via the pods' source
	// addresses.
	// +optional
	IpPoolClaimRef *NameReference `json:"ipPoolClaimRef,omitempty"`
}

// RelayOutbound delivers mail via a smart host.
type RelayOutbound struct {
	// Reference to a MailRelay in the same namespace.
	// +kubebuilder:validation:Required
	RelayRef NameReference `json:"relayRef"`
}

// TLS configures the certificate of a MailServer via cert-manager.
type TLS struct {
	// Issuer used to request the certificate.
	// +kubebuilder:validation:Required
	IssuerRef IssuerReference `json:"issuerRef"`

	// Name of the Secret the certificate is written to. Defaults to
	// `"<name>-tls"`.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	SecretName *string `json:"secretName,omitempty"`
}

// IssuerReference references a cert-manager Issuer or ClusterIssuer.
type IssuerReference struct {
	// Kind of the issuer.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	Kind string `json:"kind"`

	// Name of the issuer.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// Inbound configures the inbound (port 25) Service.
type Inbound struct {
	// Service type for inbound mail. Defaults to LoadBalancer.
	// +optional
	// +kubebuilder:validation:Enum=LoadBalancer;ClusterIP
	// +kubebuilder:default=LoadBalancer
	ServiceType *string `json:"serviceType,omitempty"`

	// externalTrafficPolicy of the inbound service. Defaults to Local
	// so that client addresses are preserved for postscreen and
	// rate limiting.
	// +optional
	// +kubebuilder:validation:Enum=Local;Cluster
	// +kubebuilder:default=Local
	ExternalTrafficPolicy *string `json:"externalTrafficPolicy,omitempty"`
}

// Submission configures authenticated submission (port 587).
type Submission struct {
	// Enable submission. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// InsecureStaticUsers provides test users from a Secret instead
	// of the MailDirectory for authentication (Phase 2 test source).
	// The Secret must contain the key "users.yaml" with password
	// hashes, never plaintext passwords. The operator only accepts it
	// when started with --enable-insecure-test-features.
	// +optional
	InsecureStaticUsers *InsecureStaticUsers `json:"insecureStaticUsers,omitempty"`
}

// InsecureStaticUsers points at a Secret with static test users.
type InsecureStaticUsers struct {
	// Name of the Secret in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	SecretName string `json:"secretName"`
}

// Mailboxes configures Dovecot mail storage.
type Mailboxes struct {
	// Enable mail storage (IMAP access, LMTP delivery). Defaults to
	// false.
	// +optional
	// +kubebuilder:default=false
	Enabled *bool `json:"enabled,omitempty"`

	// Size of the mail storage volume. Defaults to 10Gi.
	// +optional
	// +kubebuilder:default="10Gi"
	Storage *resource.Quantity `json:"storage,omitempty"`

	// Per-user quota. Unset means no quota.
	// +optional
	Quota *resource.Quantity `json:"quota,omitempty"`
}

// Postfix configures the Postfix workload.
type Postfix struct {
	// Number of replicas. Each replica runs with its own queue
	// volume. Defaults to 2.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:default=2
	Replicas *int32 `json:"replicas,omitempty"`

	// Size of the per-replica queue volume. Defaults to 1Gi.
	// +optional
	// +kubebuilder:default="1Gi"
	QueueStorage *resource.Quantity `json:"queueStorage,omitempty"`
}

// Rspamd configures the Rspamd workload.
type Rspamd struct {
	// Number of replicas. Defaults to 2.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:default=2
	Replicas *int32 `json:"replicas,omitempty"`
}

// Valkey configures the Rspamd key-value store.
//
// +kubebuilder:validation:XValidation:rule="!has(self.persistence) || !self.persistence || has(self.storage)",message="storage is required when persistence is enabled"
type Valkey struct {
	// Persist Valkey data (Bayes, fuzzy and rate-limit state).
	// Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Persistence *bool `json:"persistence,omitempty"`

	// Size of the persistence volume. Required when persistence is
	// enabled.
	// +nullable
	// +optional
	Storage *resource.Quantity `json:"storage,omitempty"`
}

// Availability configures disruption budgets and topology spread.
type Availability struct {
	// Minimum available Postfix replicas (PodDisruptionBudget).
	// Defaults to 1.
	// +optional
	// +kubebuilder:default="1"
	PostfixMinAvailable *intstr.IntOrString `json:"postfixMinAvailable,omitempty"`

	// Topology spread constraints for mail server pods.
	// +optional
	TopologySpread *TopologySpread `json:"topologySpread,omitempty"`
}

// TopologySpread configures topology spread constraints.
// WhenUnsatisfiable is always ScheduleAnyway.
type TopologySpread struct {
	// Maximum allowed skew between topology domains. Defaults to 1.
	// +optional
	// +kubebuilder:default=1
	MaxSkew *int32 `json:"maxSkew,omitempty"`

	// Topology key. Defaults to kubernetes.io/hostname.
	// +optional
	// +kubebuilder:validation:MaxLength=316
	// +kubebuilder:default="kubernetes.io/hostname"
	TopologyKey *string `json:"topologyKey,omitempty"`
}

// Observability configures monitoring integrations.
type Observability struct {
	// Create Prometheus Operator ServiceMonitors: "auto" creates them
	// when the CRDs are present, "enabled" requires them, "disabled"
	// never creates them. Defaults to auto.
	// +optional
	// +kubebuilder:validation:Enum=auto;enabled;disabled
	// +kubebuilder:default=auto
	PrometheusOperator *string `json:"prometheusOperator,omitempty"`
}

// Backup configures backup annotations (k8up) on managed resources.
type Backup struct {
	// Annotate managed volumes and pods for k8up. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
}

// MailServerStatus defines the observed state of a MailServer.
type MailServerStatus struct {
	// ObservedGeneration is the generation of the spec last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the MailServer. The
	// "Ready" condition is true when all managed workloads are
	// running with validated configuration.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MailServer is the Schema for the cnms mail servers.
type MailServer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the MailServer
	// +required
	Spec MailServerSpec `json:"spec"`

	// status defines the observed state of the MailServer
	// +optional
	Status MailServerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MailServerList contains a list of MailServer
type MailServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MailServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MailServer{}, &MailServerList{})
		return nil
	})
}
