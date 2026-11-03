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

package controller

// MailDomain status conditions (ADR 0011 and ADR 0012). The MailDomain
// controller of Phase 5 sets them; the Ready condition rolls them up
// and uses the name conditionReady.
const (
	// ConditionAuthorized is true when a MailDomainGrant covers the
	// domain for the namespace and no other MailDomain claims it
	// (ADR 0003). It reuses the reasons of DomainsAuthorized and adds
	// ReasonPublicSuffix.
	ConditionAuthorized = "Authorized"
	// ConditionDKIMKeysValid is true when every DKIM key is present and
	// parses as RSA of at least 2048 bits or Ed25519.
	ConditionDKIMKeysValid = "DKIMKeysValid"
	// ConditionDKIMRotation is false while a pending key waits for its
	// DNS record (ReasonAwaitingDNS); the old key keeps signing.
	ConditionDKIMRotation = "DKIMRotation"
	// ConditionRecordsVerified is true when a lookup found every
	// required record; Unknown with ReasonLookupFailing when lookups
	// fail temporarily.
	ConditionRecordsVerified = "RecordsVerified"
	// ConditionDNSPublished reports the DNSEndpoint of externalDNS
	// mode; false with ReasonExternalDNSNotInstalled without the CRD.
	ConditionDNSPublished = "DNSPublished"
	// ConditionSPFReady is false with ReasonNoSendingSources when no
	// sending source is known, so no SPF record is generated.
	ConditionSPFReady = "SPFReady"
	// ConditionMTASTSReady is true when the MTA-STS policy and its
	// record are available (MTA-STS enabled).
	ConditionMTASTSReady = "MTASTSReady"
	// ConditionReportAuthorization is false, as a warning that never
	// feeds into Ready, when a DMARC report address is in another
	// organizational domain: its owner has to publish an authorization
	// record before receivers send reports there (RFC 9990).
	ConditionReportAuthorization = "ReportAuthorization"
)

// MailDomain condition reasons that the MailServer conditions do not
// have.
const (
	// ReasonPublicSuffix is set when the domain is a public suffix.
	ReasonPublicSuffix = "PublicSuffix"
	// ReasonAwaitingDNS is set while the record of a pending DKIM key
	// is not verified.
	ReasonAwaitingDNS = "AwaitingDNS"
	// ReasonKeysValid is set when every DKIM key is present and usable.
	ReasonKeysValid = "KeysValid"
	// ReasonInvalidKey is set when a key does not parse as RSA of at
	// least 2048 bits or Ed25519.
	ReasonInvalidKey = "InvalidKey"
	// ReasonKeySecretConflict is set when the Secret that a generated
	// key would use belongs to another MailDomain of the namespace.
	ReasonKeySecretConflict = "KeySecretConflict"
	// ReasonKeySecretNotFound is set when the Secret or the Secret key
	// of a DKIM key is missing.
	ReasonKeySecretNotFound = "SecretNotFound"
	// ReasonNoPendingKey is set when no generated key waits for its
	// DNS record.
	ReasonNoPendingKey = "NoPendingKey"
	// ReasonUserManaged is set for your own keys: spec.dkim.keys is the
	// rotation state.
	ReasonUserManaged = "UserManaged"
	// ReasonLookupFailing is set when DNS lookups keep failing with
	// temporary errors.
	ReasonLookupFailing = "LookupFailing"
	// ReasonExternalDNSNotInstalled is set when dnsMode is externalDNS
	// and the DNSEndpoint CRD is not installed.
	ReasonExternalDNSNotInstalled = "ExternalDNSNotInstalled"
	// ReasonNoSendingSources is set when the domain has no SPF source.
	ReasonNoSendingSources = "NoSendingSources"
	// ReasonSPFGenerated is set when an SPF record can be generated.
	ReasonSPFGenerated = "SPFGenerated"
	// ReasonNoMailServer is set when MTA-STS is enabled and no
	// MailServer lists the domain, so the policy has no mx host.
	ReasonNoMailServer = "NoMailServer"
	// ReasonPolicyAvailable is set when the MTA-STS policy is in the
	// status.
	ReasonPolicyAvailable = "PolicyAvailable"
	// ReasonNotRequired is set when no report address needs an
	// authorization record at its receiver.
	ReasonNotRequired = "NotRequired"
	// ReasonReceiverRecordRequired is set when a report address needs
	// an authorization record at its receiver.
	ReasonReceiverRecordRequired = "ReceiverRecordRequired"
	// ReasonNoRecords is set when no DNS record is required.
	ReasonNoRecords = "NoRecords"
	// ReasonRecordsNotVerified is set when a lookup found a record
	// missing or different.
	ReasonRecordsNotVerified = "RecordsNotVerified"
	// ReasonChecking is set while the first lookups have not answered.
	ReasonChecking = "Checking"
	// ReasonAllVerified is set when every required record is verified.
	ReasonAllVerified = "AllVerified"
)
