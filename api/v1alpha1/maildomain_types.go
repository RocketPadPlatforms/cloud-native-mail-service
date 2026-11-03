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

// MailDomainSpec defines the desired state of a MailDomain.
//
// A MailDomain owns the public DNS records of one domain: DKIM, SPF,
// DMARC, MTA-STS and TLS-RPT. The status lists every required record
// and whether a lookup verified it.
type MailDomainSpec struct {
	// Domain name, e.g. "example.org". Lowercase DNS name with at
	// least two labels of up to 63 characters (A-labels only, no
	// trailing dot). The last label starts with a letter, so an IP
	// address is not a domain. The same rule applies to the name of a
	// MailDomainGrant; keep the two patterns in sync. Immutable:
	// DKIM keys, DNS records and the domain authorization belong to
	// one domain; create a new MailDomain for another one.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="domain is immutable; create a new MailDomain for another domain"
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?[.])+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`
	Domain string `json:"domain"`

	// DNS mode: "manual" (default) reports the required records in
	// the status for the operator to create; "externalDNS" creates
	// DNSEndpoint resources.
	// +optional
	// +kubebuilder:validation:Enum=manual;externalDNS
	// +kubebuilder:default=manual
	DNSMode *string `json:"dnsMode,omitempty"`

	// DKIM signing configuration.
	// +optional
	DKIM *DKIM `json:"dkim,omitempty"`

	// DMARC policy. Records published by CNMS use DMARC1 semantics
	// (RFC 9989-9991).
	// +optional
	DMARC *DMARC `json:"dmarc,omitempty"`

	// MTA-STS policy publication.
	// +optional
	MTAStS *MTAStS `json:"mtaSts,omitempty"`

	// TLS-RPT reporting.
	// +optional
	TLSRPT *TLSRPT `json:"tlsRpt,omitempty"`
}

// DKIM configures DKIM signing for the domain.
type DKIM struct {
	// Selector of the current signing key. Defaults to "cnms".
	// +optional
	// +kubebuilder:validation:MaxLength=30
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	// +kubebuilder:default="cnms"
	Selector *string `json:"selector,omitempty"`

	// Key rotation. When set, the operator rotates the key on the
	// given interval and publishes the new selector with an overlap
	// so that both selectors stay valid during the switch. Rotation
	// progress (schedule, start times) is recorded in the
	// "cnms.rpp.gmbh/rotation" annotation on the resource, because it
	// must survive a restore without status.
	// +optional
	Rotation *DKIMRotation `json:"rotation,omitempty"`
}

// DKIMRotation configures DKIM key rotation.
//
// +kubebuilder:validation:XValidation:rule="duration(self.interval) >= duration('24h') && (!has(self.overlap) || duration(self.overlap) < duration(self.interval))",message="interval must be at least 24h and longer than the overlap (default 24h)"
type DKIMRotation struct {
	// Interval after which the key is rotated. At least 24h.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	Interval metav1.Duration `json:"interval"`

	// Overlap during which the old and the new selector are both
	// valid. Defaults to 24h; must be shorter than the interval.
	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:default="24h0m0s"
	Overlap *metav1.Duration `json:"overlap,omitempty"`
}

// DMARC configures the DMARC policy of the domain.
type DMARC struct {
	// Enforcement policy. Defaults to "none".
	// +optional
	// +kubebuilder:validation:Enum=none;quarantine;reject
	// +kubebuilder:default=none
	Policy *string `json:"policy,omitempty"`

	// Reporting address for aggregate reports (rua): a plain email
	// address without the "mailto:" scheme, which the renderer adds.
	// Characters that would end the URI or the tag (":", ";", ",", quotes,
	// "!", whitespace) are rejected.
	// +optional
	// +kubebuilder:validation:MaxLength=254
	// +kubebuilder:validation:Pattern=`^[^\x00-\x20\x7f:;,"'!@<>?%\\]+@[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)+$`
	Rua *string `json:"rua,omitempty"`

	// Reporting address for failure reports (ruf): a plain email
	// address without the "mailto:" scheme, which the renderer adds.
	// Characters that would end the URI or the tag (":", ";", ",", quotes,
	// "!", whitespace) are rejected.
	// +optional
	// +kubebuilder:validation:MaxLength=254
	// +kubebuilder:validation:Pattern=`^[^\x00-\x20\x7f:;,"'!@<>?%\\]+@[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)+$`
	Ruf *string `json:"ruf,omitempty"`

	// Testing mode: the t= tag is set, so policies are evaluated but
	// not enforced. Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Testing *bool `json:"testing,omitempty"`
}

// MTAStS configures MTA-STS for the domain.
type MTAStS struct {
	// Report the MTA-STS policy for this domain. Defaults to false.
	// MTA-STS is report-only: CNMS does not serve the policy. The
	// status holds the exact policy text, its id and the required
	// "_mta-sts" TXT record; the policy has to be served by the
	// operator of the domain at
	// `https://mta-sts.<domain>/.well-known/mta-sts.txt` with a valid
	// public certificate.
	// +optional
	// +kubebuilder:default=false
	Enabled *bool `json:"enabled,omitempty"`
}

// TLSRPT configures TLS-RPT for the domain.
type TLSRPT struct {
	// Enable TLS-RPT for this domain. Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Enabled *bool `json:"enabled,omitempty"`

	// Reporting address: a plain email address without the "mailto:"
	// scheme, which the renderer adds. Characters that would end the
	// URI or the tag (":", ";", ",", quotes, "!", whitespace) are
	// rejected. Defaults to the DMARC rua when set.
	// +optional
	// +kubebuilder:validation:MaxLength=254
	// +kubebuilder:validation:Pattern=`^[^\x00-\x20\x7f:;,"'!@<>?%\\]+@[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]{0,61}[a-zA-Z0-9])?)+$`
	Rua *string `json:"rua,omitempty"`
}

// MailDomainStatus defines the observed state of a MailDomain.
type MailDomainStatus struct {
	// ObservedGeneration is the generation of the spec last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Records lists every DNS record required by this domain and
	// whether a lookup verified it. The list is atomic: neither the
	// name nor name and type identify a record, because one name can
	// hold several records of the same type (MX, A).
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Records []DNSRecord `json:"records,omitempty"`

	// DKIM reports the current signing state.
	// +optional
	DKIM *DKIMStatus `json:"dkim,omitempty"`

	// MTAStS reports the published policy state.
	// +optional
	MTAStS *MTAStSStatus `json:"mtaSts,omitempty"`

	// Conditions represent the current state of the MailDomain. The
	// "Ready" condition is true when the required records are
	// published and verified.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DNSRecord is the desired state of one DNS record and its
// verification state.
type DNSRecord struct {
	// Fully qualified record name, e.g. "_dmarc.example.org".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Record type (TXT, A, ...).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=10
	Type string `json:"type"`

	// Expected record value (full string for TXT records).
	// +kubebuilder:validation:Required
	Value string `json:"value"`

	// Verified is true when the last lookup matched the expected
	// value.
	// +optional
	Verified *bool `json:"verified,omitempty"`

	// LastChecked is the time of the last lookup.
	// +optional
	LastChecked *metav1.Time `json:"lastChecked,omitempty"`
}

// DKIMStatus reports the current DKIM signing state.
type DKIMStatus struct {
	// Selector of the active key.
	// +optional
	Selector string `json:"selector,omitempty"`

	// Age of the active key.
	// +optional
	KeyAge *metav1.Duration `json:"keyAge,omitempty"`

	// Selector of the key being rolled in, during an overlap.
	// +optional
	PendingSelector string `json:"pendingSelector,omitempty"`
}

// MTAStSStatus reports the MTA-STS policy state.
type MTAStSStatus struct {
	// Policy id of the "_mta-sts" TXT record. It is derived from the
	// policy content (a truncated SHA-256), so it changes exactly
	// when the policy changes and can be re-derived without status.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]+$`
	PolicyID string `json:"policyId,omitempty"`

	// Exact policy content to publish at the policy endpoint.
	// +optional
	Policy string `json:"policy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type="string",JSONPath=".spec.domain"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MailDomain is the Schema for the cnms mail domains.
type MailDomain struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the MailDomain
	// +required
	Spec MailDomainSpec `json:"spec"`

	// status defines the observed state of the MailDomain
	// +optional
	Status MailDomainStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MailDomainList contains a list of MailDomain
type MailDomainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MailDomain `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MailDomain{}, &MailDomainList{})
		return nil
	})
}
