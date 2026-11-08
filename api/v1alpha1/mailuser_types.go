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

// AddressKind says whether an address is the primary (canonical)
// address of a MailUser or one of its aliases.
type AddressKind string

const (
	// AddressKindPrimary is the canonical address: `<name>@<domain>`.
	AddressKindPrimary AddressKind = "primary"
	// AddressKindAlias is an address from spec.aliases.
	AddressKindAlias AddressKind = "alias"
)

// MailUserSpec defines the desired state of a MailUser.
//
// A MailUser is an operator-managed account for environments without a
// directory service (ADR 0008, ADR 0015). The name of the object is the
// login and the local part of the primary address. The mailbox is
// keyed by the name. A MailServer has either a directory or MailUsers,
// not both.
type MailUserSpec struct {
	// ServerRef references the MailServer in the same namespace that
	// hosts the account. The server need not exist yet.
	// +kubebuilder:validation:Required
	ServerRef NameReference `json:"serverRef"`

	// DomainRef references the MailDomain in the same namespace of the
	// primary address, `<name>@<domain>`. The domain must be served by
	// the same server as the account and authorized for it; the
	// controller checks both, CEL cannot.
	// +kubebuilder:validation:Required
	DomainRef NameReference `json:"domainRef"`

	// Aliases are further addresses that deliver into the mailbox of
	// the account and that the account may send as. Aliases are not
	// login names. At most 32.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:XValidation:rule="self.all(a, self.exists_one(b, b.localPart == a.localPart && b.domainRef.name == a.domainRef.name))",message="aliases must be unique: a local part and domain may appear only once"
	// +optional
	Aliases []MailUserAlias `json:"aliases,omitempty"`

	// Disabled removes the login and the right to send. Inbound mail
	// keeps being delivered to the mailbox. Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Disabled *bool `json:"disabled,omitempty"`

	// PasswordSecretRef references a Secret in the same namespace with
	// the plaintext password under the key "password" (at least 12
	// characters). Unset means the operator generates a password and
	// keeps it in the credential Secret of the account. A generated
	// password is lost with the credential Secret; in production use a
	// Secret from GitOps or an external secret store.
	// +optional
	PasswordSecretRef *SecretReference `json:"passwordSecretRef,omitempty"`
}

// MailUserAlias is one alias address of a MailUser.
type MailUserAlias struct {
	// LocalPart of the address: lower-case ASCII letters, digits and
	// "." "_" "-", starting and ending with a letter or digit, no
	// "..". No "+": plus-addressing is not supported.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="!self.contains('..')",message="localPart must not contain two consecutive dots"
	LocalPart string `json:"localPart"`

	// DomainRef references the MailDomain in the same namespace of the
	// address. The domain must be served by the same server as the
	// account and authorized for it; an alias that fails a check is
	// dropped and reported in status.addresses.
	// +kubebuilder:validation:Required
	DomainRef NameReference `json:"domainRef"`
}

// MailUserStatus defines the observed state of a MailUser.
type MailUserStatus struct {
	// ObservedGeneration is the generation of the spec last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the MailUser. The
	// "Ready" condition is true when the account works.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Address is the canonical (primary) address, `<name>@<domain>`.
	// Unset while the domain is not resolved.
	// +kubebuilder:validation:MaxLength=318
	// +optional
	Address string `json:"address,omitempty"`

	// Addresses lists the primary address and every alias with the
	// outcome of each, at most 33 entries.
	// +listType=map
	// +listMapKey=address
	// +kubebuilder:validation:MaxItems=33
	// +optional
	Addresses []MailUserAddressStatus `json:"addresses,omitempty"`

	// CredentialSecretName is the name of the Secret with the
	// operator-owned credential of the account.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	CredentialSecretName string `json:"credentialSecretName,omitempty"`

	// CredentialRevision is the resourceVersion of the credential
	// Secret after the last write that changed the password hash. It
	// is not secret; its change tells the MailServer to pick up the
	// new hash.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	CredentialRevision string `json:"credentialRevision,omitempty"`
}

// MailUserAddressStatus is the outcome for one address of a MailUser.
type MailUserAddressStatus struct {
	// Address is `<local part>@<domain>`, lower case.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=318
	Address string `json:"address"`

	// Kind of the address: the primary address or an alias.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=primary;alias
	Kind AddressKind `json:"kind"`

	// Served is true when mail for the address is accepted and the
	// account may send as it.
	// +kubebuilder:validation:Required
	Served bool `json:"served"`

	// Reason is Served, or why the address is not: AliasDomainNotFound,
	// AliasDomainNotServed, AliasDomainNotAuthorized or AddressConflict.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Address",type="string",JSONPath=".status.address"
// +kubebuilder:printcolumn:name="Server",type="string",JSONPath=".spec.serverRef.name"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// The name is the login, the local part of the primary address and the
// mailbox directory. The API server already requires a DNS-1123
// subdomain; the local-part limit of RFC 5321 is 64. Names that start
// with "sa." are the logins of service accounts (ADR 0015, section 4.9).
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 64",message="the MailUser name must be at most 64 characters (the local-part limit of RFC 5321)"
// +kubebuilder:validation:XValidation:rule="!self.metadata.name.startsWith('sa.')",message="names that start with 'sa.' are reserved for service accounts"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.aliases) || !self.spec.aliases.exists(a, a.localPart == self.metadata.name && a.domainRef.name == self.spec.domainRef.name)",message="an alias must not be the primary address"

// MailUser is the Schema for the cnms mail users.
type MailUser struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the MailUser
	// +required
	Spec MailUserSpec `json:"spec"`

	// status defines the observed state of the MailUser
	// +optional
	Status MailUserStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MailUserList contains a list of MailUser
type MailUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MailUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MailUser{}, &MailUserList{})
		return nil
	})
}
