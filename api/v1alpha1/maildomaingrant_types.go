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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MailDomainGrantSpec defines the desired state of a MailDomainGrant.
type MailDomainGrantSpec struct {
	// Namespace that is allowed to use this domain. The grant binds to
	// an explicit namespace name (not a label selector, which a tenant
	// could influence).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// IncludeSubdomains also grants all subdomains of the domain, at
	// any depth. The apex domain is always covered. A more specific
	// grant for a subdomain takes precedence over this grant. Defaults
	// to false.
	// +optional
	// +kubebuilder:default=false
	IncludeSubdomains *bool `json:"includeSubdomains,omitempty"`
}

// MailDomainGrantStatus defines the observed state of a MailDomainGrant.
//
// Conditions are informational only; they never feed back into
// authorization decisions.
type MailDomainGrantStatus struct {
	// ObservedGeneration is the generation of the spec last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the MailDomainGrant.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".spec.namespace"
// +kubebuilder:printcolumn:name="Subdomains",type="boolean",JSONPath=".spec.includeSubdomains"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([a-z0-9-]*[a-z0-9])?([.][a-z0-9]([a-z0-9-]*[a-z0-9])?)+$')",message="name must be a domain with at least two labels, e.g. example.org"

// MailDomainGrant grants one namespace the right to use one domain for
// mail: sending, receiving, DKIM signing, certificates and DNS records.
//
// Grants are cluster-scoped, admin-owned resources (GitOps). The object
// name is the domain itself, so the API server guarantees exactly one
// grant per exact domain and normalizes the name (lowercase, no
// trailing dot, no underscores). Domain authorization is a pure
// function of the grants and the claimed `MailDomain`s; see the domain
// authorization documentation for the uniqueness and wildcard rules.
type MailDomainGrant struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the MailDomainGrant
	// +required
	Spec MailDomainGrantSpec `json:"spec"`

	// status defines the observed state of the MailDomainGrant
	// +optional
	Status MailDomainGrantStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MailDomainGrantList contains a list of MailDomainGrant
type MailDomainGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MailDomainGrant `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MailDomainGrant{}, &MailDomainGrantList{})
		return nil
	})
}
