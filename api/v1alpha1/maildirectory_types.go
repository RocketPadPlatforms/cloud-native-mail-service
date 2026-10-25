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

// DirectoryType is the type of an external user directory.
type DirectoryType string

const (
	// DirectoryTypeLDAP is an LDAP directory.
	DirectoryTypeLDAP DirectoryType = "ldap"
	// DirectoryTypeSQL is a SQL (PostgreSQL) directory.
	DirectoryTypeSQL DirectoryType = "sql"
)

// MailDirectorySpec defines the desired state of a MailDirectory.
//
// A MailDirectory is an external user directory. Mail is delivered to
// users and authenticated against it; the directory and its accounts
// are managed outside of CNMS.
//
// +kubebuilder:validation:XValidation:rule="has(self.ldap) != has(self.sql) && ((self.type == 'ldap' && has(self.ldap)) || (self.type == 'sql' && has(self.sql)))",message="exactly one of ldap or sql must be set and must match type"
type MailDirectorySpec struct {
	// Directory type.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ldap;sql
	Type DirectoryType `json:"type"`

	// LDAP settings. Required when type is "ldap".
	// +optional
	LDAP *LDAP `json:"ldap,omitempty"`

	// SQL settings. Required when type is "sql".
	// +optional
	SQL *SQL `json:"sql,omitempty"`
}

// LDAP configures an LDAP user directory.
//
// +kubebuilder:validation:XValidation:rule="!has(self.bindDN) || has(self.bindSecretRef)",message="bindSecretRef is required when bindDN is set"
// +kubebuilder:validation:XValidation:rule="!has(self.bindDN) || !has(self.tls) || !has(self.tls.mode) || self.tls.mode != 'None'",message="bindDN must not be set when tls.mode is None (cleartext bind)"
type LDAP struct {
	// Server host.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host"`

	// Server port. Unset means the operator picks 389, or 636 when
	// tls.mode is LDAPS.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port *int32 `json:"port,omitempty"`

	// Transport security. Defaults to StartTLS with verification.
	// +optional
	TLS *LDAPTLS `json:"tls,omitempty"`

	// Base DN for user searches.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=256
	BaseDN string `json:"baseDN"`

	// Bind DN used for searches. Empty means an anonymous bind.
	// +nullable
	// +optional
	// +kubebuilder:validation:MaxLength=256
	BindDN *string `json:"bindDN,omitempty"`

	// Secret with the bind password (key "password"). Required when
	// bindDN is set.
	// +optional
	BindSecretRef *SecretReference `json:"bindSecretRef,omitempty"`

	// Search filter for users; %s is replaced with the user's mail
	// address. Defaults to "(mail=%s)".
	// +optional
	// +kubebuilder:validation:MaxLength=256
	UserFilter *string `json:"userFilter,omitempty"`
}

// LDAPTLS configures transport security for the LDAP connection.
type LDAPTLS struct {
	// Mode of the connection: "StartTLS" upgrades an existing
	// connection (default), "LDAPS" connects directly over TLS, "None"
	// disables transport security. With "None", bindDN must not be
	// set (cleartext bind).
	// +optional
	// +kubebuilder:validation:Enum=StartTLS;LDAPS;None
	// +kubebuilder:default=StartTLS
	Mode *string `json:"mode,omitempty"`

	// CASSecretRef references a Secret with the CA bundle (key
	// "ca.crt") used to verify the server certificate.
	// +optional
	CASSecretRef *SecretReference `json:"caSecretRef,omitempty"`
}

// SQLTLS configures transport security for the SQL connection.
type SQLTLS struct {
	// Mode of the connection: "verify-full" encrypts and verifies the
	// host name (default), "require" encrypts without host name
	// verification, "disable" sends cleartext.
	// +optional
	// +kubebuilder:validation:Enum=verify-full;require;disable
	// +kubebuilder:default=verify-full
	Mode *string `json:"mode,omitempty"`

	// CASSecretRef references a Secret with the CA bundle (key
	// "ca.crt") used to verify the server certificate.
	// +optional
	CASSecretRef *SecretReference `json:"caSecretRef,omitempty"`
}

// SQL configures a SQL user directory (PostgreSQL).
type SQL struct {
	// Server host.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host"`

	// Server port. Defaults to 5432.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=5432
	Port *int32 `json:"port,omitempty"`

	// Database name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	Database string `json:"database"`

	// Database user.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	Username string `json:"username"`

	// Secret with the database password (key "password").
	// +kubebuilder:validation:Required
	PasswordSecretRef SecretReference `json:"passwordSecretRef"`

	// Transport security. Defaults to verify-full.
	// +optional
	TLS *SQLTLS `json:"tls,omitempty"`
}

// MailDirectoryStatus defines the observed state of a MailDirectory.
type MailDirectoryStatus struct {
	// ObservedGeneration is the generation of the spec last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the MailDirectory.
	// The "Ready" condition is true when the directory is reachable
	// and answers queries.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MailDirectory is the Schema for the cnms mail directories.
type MailDirectory struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the MailDirectory
	// +required
	Spec MailDirectorySpec `json:"spec"`

	// status defines the observed state of the MailDirectory
	// +optional
	Status MailDirectoryStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MailDirectoryList contains a list of MailDirectory
type MailDirectoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MailDirectory `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MailDirectory{}, &MailDirectoryList{})
		return nil
	})
}
