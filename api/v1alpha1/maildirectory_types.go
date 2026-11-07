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
	// Directory type. "sql" means PostgreSQL; other SQL servers are
	// not supported.
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
	// Server host name or IPv4 address, without a port (see port).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)*$`
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

	// Base DN for user searches. Control characters are rejected.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\x00-\x1f\x7f]*$`
	BaseDN string `json:"baseDN"`

	// Bind DN used for searches. Empty means an anonymous bind.
	// Control characters are rejected.
	// +nullable
	// +optional
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\x00-\x1f\x7f]*$`
	BindDN *string `json:"bindDN,omitempty"`

	// Secret with the bind password (key "password"). Required when
	// bindDN is set.
	// +optional
	BindSecretRef *SecretReference `json:"bindSecretRef,omitempty"`

	// Attributes names the directory attributes of an account. Unset
	// means the defaults of the fields below.
	// +optional
	Attributes *LDAPAttributes `json:"attributes,omitempty"`

	// ActiveFilter is a static restriction that every lookup of an
	// account adds, for example
	// "(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))". It
	// is a complete LDAP filter without a placeholder: the operator
	// builds the lookups around it and supplies the looked-up value
	// itself. Unset means no restriction. The characters "{", "}" and
	// "%" and control characters are rejected.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\x00-\x1f\x7f%{}]*$`
	ActiveFilter *string `json:"activeFilter,omitempty"`
}

// LDAPAttributes names the LDAP attributes of an account. Only
// attribute names are accepted, never filter or query fragments.
type LDAPAttributes struct {
	// Login is the attribute with the login name. It is also the
	// account name (and so the mailbox directory); it must be
	// single-valued. Defaults to "uid".
	// +optional
	// +kubebuilder:default=uid
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9-]{0,63}$`
	Login *string `json:"login,omitempty"`

	// Mail is the attribute with the canonical (primary) address of
	// the account. It must be single-valued. Defaults to "mail".
	// +optional
	// +kubebuilder:default=mail
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9-]{0,63}$`
	Mail *string `json:"mail,omitempty"`

	// Aliases is the multi-valued attribute with the further
	// addresses of the account, for example "mailAlias". Unset means
	// the directory has no aliases.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9-]{0,63}$`
	Aliases *string `json:"aliases,omitempty"`
}

// LDAPTLS configures transport security for the LDAP connection.
// +kubebuilder:validation:XValidation:rule="!has(self.caSecretRef) || !has(self.mode) || self.mode != 'None'",message="caSecretRef must not be set when mode is None (no TLS to verify)"
type LDAPTLS struct {
	// Mode of the connection: "StartTLS" upgrades an existing
	// connection (default), "LDAPS" connects directly over TLS, "None"
	// disables transport security. With "None", bindDN and caSecretRef
	// must not be set, and a MailServer cannot use the directory
	// (DirectoryInvalid): Dovecot checks a password by binding to the
	// directory as the user, which would send the password of every
	// login in clear text.
	// +optional
	// +kubebuilder:validation:Enum=StartTLS;LDAPS;None
	// +kubebuilder:default=StartTLS
	Mode *string `json:"mode,omitempty"`

	// CASecretRef references a Secret with the CA bundle (key
	// "ca.crt") used to verify the server certificate.
	// +optional
	CASecretRef *SecretReference `json:"caSecretRef,omitempty"`
}

// SQLTLS configures transport security for the SQL connection.
// +kubebuilder:validation:XValidation:rule="!has(self.caSecretRef) || !has(self.mode) || self.mode != 'disable'",message="caSecretRef must not be set when mode is disable (no TLS to verify)"
type SQLTLS struct {
	// Mode of the connection: "verify-full" encrypts and verifies the
	// host name (default), "require" encrypts without host name
	// verification (with caSecretRef it also verifies the certificate
	// chain, like libpq with a root certificate), "disable" sends
	// cleartext. With "disable", caSecretRef must not be set.
	// +optional
	// +kubebuilder:validation:Enum=verify-full;require;disable
	// +kubebuilder:default=verify-full
	Mode *string `json:"mode,omitempty"`

	// CASecretRef references a Secret with the CA bundle (key
	// "ca.crt") used to verify the server certificate.
	// +optional
	CASecretRef *SecretReference `json:"caSecretRef,omitempty"`
}

// SQL configures a SQL user directory (PostgreSQL).
type SQL struct {
	// Server host name or IPv4 address, without a port (see port).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)*$`
	Host string `json:"host"`

	// Server port. Defaults to 5432.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=5432
	Port *int32 `json:"port,omitempty"`

	// Database name: letters, digits, "_", "-" and "." only, so that
	// the value cannot add keywords to the connection string.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`
	Database string `json:"database"`

	// Database user: letters, digits, "_", "-" and "." only, so that
	// the value cannot add keywords to the connection string.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`
	Username string `json:"username"`

	// Secret with the database password (key "password").
	// +kubebuilder:validation:Required
	PasswordSecretRef SecretReference `json:"passwordSecretRef"`

	// Transport security. Defaults to verify-full.
	// +optional
	TLS *SQLTLS `json:"tls,omitempty"`

	// Users is the relation (table or view) with the accounts. The
	// operator generates every query itself and reads the fixed
	// columns login, mail and password (a "{SCHEME}hash" value); a
	// view adapts an existing schema, and is also where accounts are
	// switched off. Defaults to "cnms_users" in the schema "public".
	// +optional
	// +kubebuilder:default={name: "cnms_users"}
	Users *SQLRelation `json:"users,omitempty"`

	// Aliases is the relation with the further addresses, read as the
	// fixed columns alias and target (the canonical address). Unset
	// means the directory has no aliases.
	// +optional
	Aliases *SQLRelation `json:"aliases,omitempty"`
}

// SQLRelation names a relation (table or view) of the directory
// database. Only identifiers are accepted, never SQL.
type SQLRelation struct {
	// Name of the relation: lower case letters, digits and "_".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	Name string `json:"name"`

	// Schema of the relation, with the same characters as name.
	// Defaults to "public".
	// +optional
	// +kubebuilder:default=public
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	Schema *string `json:"schema,omitempty"`
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
