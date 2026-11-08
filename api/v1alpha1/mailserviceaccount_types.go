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

// MailServiceAccountSpec defines the desired state of a
// MailServiceAccount.
//
// A MailServiceAccount is the sending credential of an application. It
// lives in the namespace of the application and may reference a
// MailServer of another namespace, which has to consent to it (ADR 0008,
// ADR 0015).
type MailServiceAccountSpec struct {
	// ServerRef references the MailServer the application sends
	// through. Without a namespace it is a MailServer of the namespace
	// of the account. A MailServer of another namespace takes effect
	// only when that server lists the namespace of the account in
	// spec.serviceAccounts.allowedNamespaces. The server need not
	// exist yet.
	// +kubebuilder:validation:Required
	ServerRef NamespacedNameReference `json:"serverRef"`

	// Sender is the single address the application may send as:
	// `<local part>@<domain>`, lower case. The local part holds
	// lower-case ASCII letters, digits and "." "_" "-", starts and ends
	// with a letter or digit, has no ".." and is at most 64 characters
	// (no "+": plus-addressing is not supported). The domain follows
	// the rule of MailDomain.spec.domain and must be served by the
	// server and authorized for it; the controller checks both, CEL
	// cannot.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=318
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?@([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?[.])+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="!self.contains('..')",message="sender must not contain two consecutive dots"
	// +kubebuilder:validation:XValidation:rule="size(self.substring(self.indexOf('@') + 1)) <= 253",message="the domain of sender must be at most 253 characters"
	Sender string `json:"sender"`
}

// MailServiceAccountStatus defines the observed state of a
// MailServiceAccount.
type MailServiceAccountStatus struct {
	// ObservedGeneration is the generation of the spec last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the MailServiceAccount.
	// The "Ready" condition is true when the credential works.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Sender",type="string",JSONPath=".spec.sender"
// +kubebuilder:printcolumn:name="Server",type="string",JSONPath=".spec.serverRef.name"
// +kubebuilder:printcolumn:name="Server Namespace",type="string",JSONPath=".spec.serverRef.namespace"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// The name is a label value on the credential Secret and the
// NetworkPolicy of the account, which allows 63 characters.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="the MailServiceAccount name must be at most 63 characters: it is a label value on the credential Secret and the NetworkPolicy"

// MailServiceAccount is the Schema for the cnms mail service accounts.
type MailServiceAccount struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the MailServiceAccount
	// +required
	Spec MailServiceAccountSpec `json:"spec"`

	// status defines the observed state of the MailServiceAccount
	// +optional
	Status MailServiceAccountStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MailServiceAccountList contains a list of MailServiceAccount
type MailServiceAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MailServiceAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MailServiceAccount{}, &MailServiceAccountList{})
		return nil
	})
}
