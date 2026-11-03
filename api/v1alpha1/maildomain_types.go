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
//
// +kubebuilder:validation:XValidation:rule="!has(self.tlsRpt) || !has(self.tlsRpt.enabled) || !self.tlsRpt.enabled || has(self.tlsRpt.rua) || (has(self.dmarc) && has(self.dmarc.rua))",message="tlsRpt.enabled requires a report address in tlsRpt.rua or dmarc.rua"
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

	// SPF record generation: the sending sources of the domain.
	// +optional
	SPF *SPF `json:"spf,omitempty"`

	// MTA-STS policy reporting. CNMS does not serve the policy.
	// +optional
	MTAStS *MTAStS `json:"mtaSts,omitempty"`

	// TLS-RPT reporting.
	// +optional
	TLSRPT *TLSRPT `json:"tlsRpt,omitempty"`
}

// DKIM configures DKIM signing for the domain. A domain always signs
// (ADR 0011): either with keys the operator generates (RSA-2048, plus
// an optional Ed25519 key) or with the keys listed in keys.
//
// +kubebuilder:validation:XValidation:rule="!has(self.keys) || !(has(self.rotation) || has(self.ed25519) || has(self.selectorPrefix))",message="keys excludes rotation, ed25519 and selectorPrefix: with your own keys you own the selectors and the schedule"
// +kubebuilder:validation:XValidation:rule="!has(self.keys) || self.keys.exists(k, !has(k.signing) || k.signing)",message="at least one key must have signing set to true"
type DKIM struct {
	// Prefix of the selector names of generated keys. The RSA selector
	// is `<prefix>-<yyyymmdd>` and the Ed25519 selector
	// `<prefix>-ed25519-<yyyymmdd>`, the UTC date the key was created
	// (rotation is at most daily), so a rotation publishes a new
	// selector next to the old one. The selectors in use are reported
	// in status.dkim. Defaults to "cnms" when the operator generates
	// the keys; not allowed together with keys.
	// +optional
	// +kubebuilder:validation:MaxLength=30
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	SelectorPrefix *string `json:"selectorPrefix,omitempty"`

	// Ed25519 adds a second signature with an Ed25519 key (RFC 8463)
	// next to the RSA one. Receivers that do not know Ed25519 ignore
	// it. Defaults to false; not allowed together with keys.
	// +optional
	Ed25519 *bool `json:"ed25519,omitempty"`

	// Key rotation. When set, the operator rotates the generated keys
	// on the given interval and publishes the new selector with an
	// overlap so that both selectors stay valid during the switch.
	// Rotation progress (which keys are active, when a retiring key
	// ends) is recorded in the "cnms.rpp.gmbh/rotation" annotation on
	// the resource, because it must survive a restore without status.
	// Not allowed together with keys.
	// +optional
	Rotation *DKIMRotation `json:"rotation,omitempty"`

	// Keys lists your own keys by reference (bring your own keys, the
	// recommended production setup: the keys live in GitOps or an
	// external secret store). It replaces key generation. Rotate by
	// publishing the new key with signing false, waiting until the
	// status shows its record verified, flipping the signing flags in
	// one change and removing the old key after the overlap. Each
	// Secret value is a PEM (or base64) private key: RSA of at least
	// 2048 bits or Ed25519.
	// +optional
	// +listType=map
	// +listMapKey=selector
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Keys []DKIMKey `json:"keys,omitempty"`
}

// DKIMKey references one DKIM private key.
type DKIMKey struct {
	// Selector of the key: a DNS label. The record is published at
	// `<selector>._domainkey.<domain>`.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	Selector string `json:"selector"`

	// SecretRef is the Secret key (same namespace) that holds the
	// private key.
	// +kubebuilder:validation:Required
	SecretRef SecretKeyReference `json:"secretRef"`

	// Signing is true when the key signs outgoing mail. A key with
	// signing false is only published, so that receivers know it
	// before it takes over. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	Signing *bool `json:"signing,omitempty"`
}

// DKIMRotation configures DKIM key rotation. The overlap defaults to
// 120h, so an interval of 5 days or less is rejected unless the overlap
// is set explicitly to a shorter value.
//
// +kubebuilder:validation:XValidation:rule="duration(self.interval) >= duration('24h') && (!has(self.overlap) || duration(self.overlap) < duration(self.interval))",message="interval must be at least 24h and longer than the overlap (default 120h); set overlap explicitly for an interval of 5 days or less"
type DKIMRotation struct {
	// Interval after which the key is rotated. At least 24h.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	Interval metav1.Duration `json:"interval"`

	// Overlap during which the old and the new selector are both
	// valid. It has to cover the Postfix queue lifetime (5 days):
	// deferred mail is verified on delivery, possibly days after it was
	// signed. Defaults to 120h; must be shorter than the interval, so
	// an interval of 5 days or less needs an explicit overlap.
	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:default="120h0m0s"
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="overlap must be positive"
	Overlap *metav1.Duration `json:"overlap,omitempty"`
}

// DMARC configures the DMARC policy of the domain.
type DMARC struct {
	// Enforcement policy. Defaults to "none".
	// +optional
	// +kubebuilder:validation:Enum=none;quarantine;reject
	// +kubebuilder:default=none
	Policy *string `json:"policy,omitempty"`

	// Policy for existing subdomains (sp). Omitted by default, so the
	// subdomains follow policy.
	// +optional
	// +kubebuilder:validation:Enum=none;quarantine;reject
	SubdomainPolicy *string `json:"subdomainPolicy,omitempty"`

	// Policy for non-existent subdomains (np, RFC 9989). Omitted by
	// default, so they follow subdomainPolicy, then policy.
	// +optional
	// +kubebuilder:validation:Enum=none;quarantine;reject
	NonExistentSubdomainPolicy *string `json:"nonExistentSubdomainPolicy,omitempty"`

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

	// Testing mode: publishes t=y (RFC 9989). Receivers that implement
	// it apply the policy one level lower (reject as quarantine,
	// quarantine as none); it has no effect on none. The Rspamd of the
	// data plane ignores t on the records of inbound mail (ADR 0012).
	// Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Testing *bool `json:"testing,omitempty"`
}

// SPF configures the SPF record of the domain. The record lists these
// sources, in the order ip4, ip6, include, each sorted, followed by the
// qualifier and "all". Without any source no record is published.
type SPF struct {
	// IPv4 addresses or CIDR ranges allowed to send for the domain
	// (ip4: mechanisms).
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="self.all(a, (isIP(a) && ip(a).family() == 4) || (isCIDR(a) && cidr(a).ip().family() == 4))",message="every entry must be an IPv4 address or CIDR"
	IP4 []string `json:"ip4,omitempty"`

	// IPv6 addresses or CIDR ranges allowed to send for the domain
	// (ip6: mechanisms).
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="self.all(a, (isIP(a) && ip(a).family() == 6) || (isCIDR(a) && cidr(a).ip().family() == 6))",message="every entry must be an IPv6 address or CIDR"
	IP6 []string `json:"ip6,omitempty"`

	// Domains whose SPF records are included (include: mechanisms), for
	// example the one of a mail provider. At most 5: SPF allows ten
	// DNS lookups in total (RFC 7208), and CNMS does not follow the
	// includes.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?[.])+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`
	Include []string `json:"include,omitempty"`

	// Result for senders not listed: "softfail" (~all, default), "fail"
	// (-all) or "neutral" (?all).
	// +optional
	// +kubebuilder:validation:Enum=softfail;fail;neutral
	// +kubebuilder:default=softfail
	Qualifier *string `json:"qualifier,omitempty"`
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

	// Mode of the reported policy: "testing" (default; senders report
	// failures but still deliver), "enforce" or "none".
	// +optional
	// +kubebuilder:validation:Enum=testing;enforce;none
	// +kubebuilder:default=testing
	Mode *string `json:"mode,omitempty"`
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

	// Conditions represent the current state of the MailDomain:
	// Authorized (the domain is granted to the namespace and unique),
	// DKIMKeysValid (every key is present and usable), DKIMRotation
	// (false while a new key waits for its DNS record),
	// RecordsVerified (a lookup found the required records),
	// DNSPublished (the DNSEndpoint is managed, externalDNS mode),
	// SPFReady (an SPF record can be generated), MTASTSReady (a policy
	// exists, when MTA-STS is enabled) and Ready. "Ready" is true when
	// the domain is authorized, the DKIM keys are valid and every
	// required record is verified.
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

// DKIMStatus reports the current DKIM signing state. A key is pending
// (published, not signing), active (signing) or retiring (still
// published, no longer signing).
type DKIMStatus struct {
	// Active keys: the ones that sign, at most one per algorithm for
	// generated keys; with your own keys every key with signing true.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Active []DKIMKeyStatus `json:"active,omitempty"`

	// Pending keys: published, not signing. A generated key waits for
	// its verified record; with your own keys these are the keys with
	// signing false, before they take over or after they stopped
	// signing.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Pending []DKIMKeyStatus `json:"pending,omitempty"`

	// Retiring keys: replaced by an active key, published until their
	// overlap ends.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Retiring []DKIMKeyStatus `json:"retiring,omitempty"`

	// Time the next generated key is created. Unset without rotation
	// and with your own keys.
	// +optional
	NextRotation *metav1.Time `json:"nextRotation,omitempty"`
}

// DKIMKeyStatus describes one DKIM key.
type DKIMKeyStatus struct {
	// Selector of the key.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	Selector string `json:"selector"`

	// Algorithm of the key.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=rsa;ed25519
	Algorithm string `json:"algorithm"`

	// Creation time of the key. The age is derived from it, so status
	// does not change on every reconcile. Unset for your own keys.
	// +optional
	KeyCreated *metav1.Time `json:"keyCreated,omitempty"`

	// Time a retiring key leaves DNS. Set only for retiring keys.
	// +optional
	Until *metav1.Time `json:"until,omitempty"`
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
// +kubebuilder:printcolumn:name="Authorized",type="string",JSONPath=".status.conditions[?(@.type=='Authorized')].status"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// The name is the value of a label on the DKIM key Secrets
// "<name>-dkim-<selector>" (at most 63 + 6 + 47 = 116 characters, within
// the 253 of a Secret name), so it must be a valid label value.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="the MailDomain name must be at most 63 characters: it is a label value on the DKIM key Secrets, whose names (<name>-dkim-<selector>) then stay within 116 characters"

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
