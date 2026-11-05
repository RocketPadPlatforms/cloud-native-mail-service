# API Reference

## Packages
- [cnms.rpp.gmbh/v1alpha1](#cnmsrppgmbhv1alpha1)


## cnms.rpp.gmbh/v1alpha1

Package v1alpha1 contains API Schema definitions for the cnms v1alpha1 API group.

### Resource Types
- [MailDirectory](#maildirectory)
- [MailDomain](#maildomain)
- [MailDomainGrant](#maildomaingrant)
- [MailServer](#mailserver)



#### Availability



Availability configures disruption budgets and topology spread.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `postfixMinAvailable` _[IntOrString](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#intorstring-intstr-util)_ | Minimum available Postfix replicas (PodDisruptionBudget).<br />Defaults to 1. An integer or a percentage such as "50%"; a<br />string without "%" is rejected by the PodDisruptionBudget<br />validation, so it is rejected here as well. | 1 | Optional: \{\} <br /> |
| `topologySpread` _[TopologySpread](#topologyspread)_ | Topology spread constraints for mail server pods. |  | Optional: \{\} <br /> |


#### Backup



Backup configures backup annotations (k8up) on the managed volumes.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Annotate the managed PersistentVolumeClaims for k8up<br />(k8up.io/backup): the Dovecot mail volume is included, the<br />Postfix queue volumes and the Valkey volume are excluded.<br />Kubernetes cannot change the claim templates of an existing<br />StatefulSet, so the annotation reaches the Postfix and Dovecot<br />volumes created after the StatefulSet. Defaults to true. | true | Optional: \{\} <br /> |


#### DKIM



DKIM configures DKIM signing for the domain. A domain always signs
(ADR 0011): either with keys the operator generates (RSA-2048, plus
an optional Ed25519 key) or with the keys listed in keys.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `selectorPrefix` _string_ | Prefix of the selector names of generated keys. The RSA selector<br />is `<prefix>-<yyyymmdd>` and the Ed25519 selector<br />`<prefix>-ed25519-<yyyymmdd>`, the UTC date the key was created<br />(rotation is at most daily), so a rotation publishes a new<br />selector next to the old one. The selectors in use are reported<br />in status.dkim. Defaults to "cnms" when the operator generates<br />the keys; not allowed together with keys. |  | MaxLength: 30 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `ed25519` _boolean_ | Ed25519 adds a second signature with an Ed25519 key (RFC 8463)<br />next to the RSA one. Receivers that do not know Ed25519 ignore<br />it. Defaults to false; not allowed together with keys. |  | Optional: \{\} <br /> |
| `rotation` _[DKIMRotation](#dkimrotation)_ | Key rotation. When set, the operator rotates the generated keys<br />on the given interval and publishes the new selector with an<br />overlap so that both selectors stay valid during the switch.<br />Rotation progress (which keys are active, when a retiring key<br />ends) is recorded in the "cnms.rpp.gmbh/rotation" annotation on<br />the resource, because it must survive a restore without status.<br />Not allowed together with keys. |  | Optional: \{\} <br /> |
| `keys` _[DKIMKey](#dkimkey) array_ | Keys lists your own keys by reference (bring your own keys, the<br />recommended production setup: the keys live in GitOps or an<br />external secret store). It replaces key generation. Rotate by<br />publishing the new key with signing false, waiting until the<br />status shows its record verified, flipping the signing flags in<br />one change and removing the old key after the overlap. Each<br />Secret value is a PEM (or base64) private key: RSA of at least<br />2048 bits or Ed25519. |  | MaxItems: 8 <br />MinItems: 1 <br />Optional: \{\} <br /> |


#### DKIMKey



DKIMKey references one DKIM private key.



_Appears in:_
- [DKIM](#dkim)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `selector` _string_ | Selector of the key: a DNS label. The record is published at<br />`<selector>._domainkey.<domain>`. |  | MaxLength: 63 <br />Pattern: `^[a-z0-9]([a-z0-9-]\{0,61\}[a-z0-9])?$` <br />Required: \{\} <br /> |
| `secretRef` _[SecretKeyReference](#secretkeyreference)_ | SecretRef is the Secret key (same namespace) that holds the<br />private key. |  | Required: \{\} <br /> |
| `signing` _boolean_ | Signing is true when the key signs outgoing mail. A key with<br />signing false is only published, so that receivers know it<br />before it takes over. Defaults to true. | true | Optional: \{\} <br /> |


#### DKIMKeyStatus



DKIMKeyStatus describes one DKIM key.



_Appears in:_
- [DKIMStatus](#dkimstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `selector` _string_ | Selector of the key. |  | MaxLength: 63 <br />Required: \{\} <br /> |
| `algorithm` _string_ | Algorithm of the key. |  | Enum: [rsa ed25519] <br />Required: \{\} <br /> |
| `keyCreated` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | Creation time of the key. The age is derived from it, so status<br />does not change on every reconcile. Unset for your own keys. |  | Optional: \{\} <br /> |
| `until` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | Time a retiring key leaves DNS. Set only for retiring keys. |  | Optional: \{\} <br /> |


#### DKIMRotation



DKIMRotation configures DKIM key rotation. The overlap defaults to
120h, so an interval of 5 days or less is rejected unless the overlap
is set explicitly to a shorter value.



_Appears in:_
- [DKIM](#dkim)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#duration-v1-meta)_ | Interval after which the key is rotated. At least 24h. |  | MaxLength: 32 <br />Required: \{\} <br />Type: string <br /> |
| `overlap` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#duration-v1-meta)_ | Overlap during which the old and the new selector are both<br />valid. It has to cover the Postfix queue lifetime (5 days):<br />deferred mail is verified on delivery, possibly days after it was<br />signed. Defaults to 120h; must be shorter than the interval, so<br />an interval of 5 days or less needs an explicit overlap. | 120h0m0s | MaxLength: 32 <br />Type: string <br />Optional: \{\} <br /> |


#### DKIMStatus



DKIMStatus reports the current DKIM signing state. A key is pending
(published, not signing), active (signing) or retiring (still
published, no longer signing).



_Appears in:_
- [MailDomainStatus](#maildomainstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `active` _[DKIMKeyStatus](#dkimkeystatus) array_ | Active keys: the ones that sign, at most one per algorithm for<br />generated keys; with your own keys every key with signing true. |  | MaxItems: 8 <br />Optional: \{\} <br /> |
| `pending` _[DKIMKeyStatus](#dkimkeystatus) array_ | Pending keys: published, not signing. A generated key waits for<br />its verified record; with your own keys these are the keys with<br />signing false, before they take over or after they stopped<br />signing. |  | MaxItems: 8 <br />Optional: \{\} <br /> |
| `retiring` _[DKIMKeyStatus](#dkimkeystatus) array_ | Retiring keys: replaced by an active key, published until their<br />overlap ends. |  | MaxItems: 8 <br />Optional: \{\} <br /> |
| `nextRotation` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | Time the next generated key is created. Unset without rotation<br />and with your own keys. |  | Optional: \{\} <br /> |


#### DMARC



DMARC configures the DMARC policy of the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policy` _string_ | Enforcement policy. Defaults to "none". | none | Enum: [none quarantine reject] <br />Optional: \{\} <br /> |
| `subdomainPolicy` _string_ | Policy for existing subdomains (sp). Omitted by default, so the<br />subdomains follow policy. |  | Enum: [none quarantine reject] <br />Optional: \{\} <br /> |
| `nonExistentSubdomainPolicy` _string_ | Policy for non-existent subdomains (np, RFC 9989). Omitted by<br />default, so they follow subdomainPolicy, then policy. |  | Enum: [none quarantine reject] <br />Optional: \{\} <br /> |
| `rua` _string_ | Reporting address for aggregate reports (rua): a plain email<br />address without the "mailto:" scheme, which the renderer adds.<br />Characters that would end the URI or the tag (":", ";", ",", quotes,<br />"!", whitespace) are rejected. |  | MaxLength: 254 <br />Pattern: `^[^\x00-\x20\x7f:;,"'!@<>?%\\]+@[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?)+$` <br />Optional: \{\} <br /> |
| `ruf` _string_ | Reporting address for failure reports (ruf): a plain email<br />address without the "mailto:" scheme, which the renderer adds.<br />Characters that would end the URI or the tag (":", ";", ",", quotes,<br />"!", whitespace) are rejected. |  | MaxLength: 254 <br />Pattern: `^[^\x00-\x20\x7f:;,"'!@<>?%\\]+@[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?)+$` <br />Optional: \{\} <br /> |
| `testing` _boolean_ | Testing mode: publishes t=y (RFC 9989). Receivers that implement<br />it apply the policy one level lower (reject as quarantine,<br />quarantine as none); it has no effect on none. The Rspamd of the<br />data plane ignores t on the records of inbound mail (ADR 0012).<br />Defaults to false. | false | Optional: \{\} <br /> |


#### DNSRecord



DNSRecord is the desired state of one DNS record and its
verification state.



_Appears in:_
- [MailDomainStatus](#maildomainstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Fully qualified record name, e.g. "_dmarc.example.org". |  | MaxLength: 253 <br />Required: \{\} <br /> |
| `type` _string_ | Record type (TXT, A, ...). |  | MaxLength: 10 <br />Required: \{\} <br /> |
| `value` _string_ | Expected record value (full string for TXT records). |  | Required: \{\} <br /> |
| `verified` _boolean_ | Verified is true when the last lookup matched the expected<br />value. |  | Optional: \{\} <br /> |
| `lastChecked` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastChecked is the time of the last lookup. |  | Optional: \{\} <br /> |


#### DirectOutbound



DirectOutbound delivers mail directly.



_Appears in:_
- [Outbound](#outbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipPoolClaimRef` _[NameReference](#namereference)_ | Reference to a MailIPPoolClaim providing the sending addresses.<br />Optional: without a claim, mail leaves via the pods' source<br />addresses. |  | Optional: \{\} <br /> |


#### DirectoryType

_Underlying type:_ _string_

DirectoryType is the type of an external user directory.



_Appears in:_
- [MailDirectorySpec](#maildirectoryspec)

| Field | Description |
| --- | --- |
| `ldap` | DirectoryTypeLDAP is an LDAP directory.<br /> |
| `sql` | DirectoryTypeSQL is a SQL (PostgreSQL) directory.<br /> |


#### Inbound



Inbound configures the inbound (port 25) Service.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serviceType` _string_ | Service type for inbound mail. Defaults to LoadBalancer. With<br />ClusterIP the service is not reachable from outside the cluster<br />and externalTrafficPolicy has no effect. | LoadBalancer | Enum: [LoadBalancer ClusterIP] <br />Optional: \{\} <br /> |
| `externalTrafficPolicy` _string_ | externalTrafficPolicy of the inbound service. Defaults to Local<br />so that client addresses are preserved for postscreen and<br />rate limiting. Only used with serviceType LoadBalancer; it is<br />ignored (not rejected, because the default is always present)<br />with ClusterIP. | Local | Enum: [Local Cluster] <br />Optional: \{\} <br /> |


#### InsecureStaticUsers



InsecureStaticUsers points at a Secret with static test users.



_Appears in:_
- [Submission](#submission)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `secretName` _string_ | Name of the Secret in the same namespace. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Required: \{\} <br /> |


#### IssuerReference



IssuerReference references a cert-manager Issuer or ClusterIssuer.



_Appears in:_
- [TLS](#tls)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind of the issuer. |  | Enum: [Issuer ClusterIssuer] <br />Required: \{\} <br /> |
| `name` _string_ | Name of the issuer. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Required: \{\} <br /> |


#### LDAP



LDAP configures an LDAP user directory.



_Appears in:_
- [MailDirectorySpec](#maildirectoryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | Server host name or IPv4 address, without a port (see port). |  | MaxLength: 253 <br />Pattern: `^[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?)*$` <br />Required: \{\} <br /> |
| `port` _integer_ | Server port. Unset means the operator picks 389, or 636 when<br />tls.mode is LDAPS. |  | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `tls` _[LDAPTLS](#ldaptls)_ | Transport security. Defaults to StartTLS with verification. |  | Optional: \{\} <br /> |
| `baseDN` _string_ | Base DN for user searches. Control characters are rejected. |  | MaxLength: 256 <br />Pattern: `^[^\x00-\x1f\x7f]*$` <br />Required: \{\} <br /> |
| `bindDN` _string_ | Bind DN used for searches. Empty means an anonymous bind.<br />Control characters are rejected. |  | MaxLength: 256 <br />Pattern: `^[^\x00-\x1f\x7f]*$` <br />Optional: \{\} <br /> |
| `bindSecretRef` _[SecretReference](#secretreference)_ | Secret with the bind password (key "password"). Required when<br />bindDN is set. |  | Optional: \{\} <br /> |
| `userFilter` _string_ | Search filter for users. It must contain the engine-neutral<br />placeholder \{user\} exactly once; it stands for the user's mail<br />address. The renderer translates the placeholder and escapes<br />the value for each engine (Postfix %s, Dovecot %\{user\}). Control<br />characters and "%" are rejected. Defaults to "(mail=\{user\})". |  | MaxLength: 256 <br />Pattern: `^[^\x00-\x1f\x7f%]*$` <br />Optional: \{\} <br /> |


#### LDAPTLS



LDAPTLS configures transport security for the LDAP connection.



_Appears in:_
- [LDAP](#ldap)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode of the connection: "StartTLS" upgrades an existing<br />connection (default), "LDAPS" connects directly over TLS, "None"<br />disables transport security. With "None", bindDN must not be<br />set (cleartext bind). | StartTLS | Enum: [StartTLS LDAPS None] <br />Optional: \{\} <br /> |
| `caSecretRef` _[SecretReference](#secretreference)_ | CASecretRef references a Secret with the CA bundle (key<br />"ca.crt") used to verify the server certificate. |  | Optional: \{\} <br /> |


#### MTAStS



MTAStS configures MTA-STS for the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Report the MTA-STS policy for this domain. Defaults to false.<br />MTA-STS is report-only: CNMS does not serve the policy. The<br />status holds the exact policy text, its id and the required<br />"_mta-sts" TXT record; the policy has to be served by the<br />operator of the domain at<br />`https://mta-sts.<domain>/.well-known/mta-sts.txt` with a valid<br />public certificate. | false | Optional: \{\} <br /> |
| `mode` _string_ | Mode of the reported policy: "testing" (default; senders report<br />failures but still deliver), "enforce" or "none". | testing | Enum: [testing enforce none] <br />Optional: \{\} <br /> |


#### MTAStSStatus



MTAStSStatus reports the MTA-STS policy state.



_Appears in:_
- [MailDomainStatus](#maildomainstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policyId` _string_ | Policy id of the "_mta-sts" TXT record. It is derived from the<br />policy content (a truncated SHA-256), so it changes exactly<br />when the policy changes and can be re-derived without status. |  | MaxLength: 32 <br />Pattern: `^[a-zA-Z0-9]+$` <br />Optional: \{\} <br /> |
| `policy` _string_ | Exact policy content to publish at the policy endpoint. |  | Optional: \{\} <br /> |


#### MailDirectory



MailDirectory is the Schema for the cnms mail directories.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cnms.rpp.gmbh/v1alpha1` | | |
| `kind` _string_ | `MailDirectory` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[MailDirectorySpec](#maildirectoryspec)_ | spec defines the desired state of the MailDirectory |  | Required: \{\} <br /> |
| `status` _[MailDirectoryStatus](#maildirectorystatus)_ | status defines the observed state of the MailDirectory |  | Optional: \{\} <br /> |


#### MailDirectorySpec



MailDirectorySpec defines the desired state of a MailDirectory.

A MailDirectory is an external user directory. Mail is delivered to
users and authenticated against it; the directory and its accounts
are managed outside of CNMS.



_Appears in:_
- [MailDirectory](#maildirectory)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[DirectoryType](#directorytype)_ | Directory type. "sql" means PostgreSQL; other SQL servers are<br />not supported. |  | Enum: [ldap sql] <br />Required: \{\} <br /> |
| `ldap` _[LDAP](#ldap)_ | LDAP settings. Required when type is "ldap". |  | Optional: \{\} <br /> |
| `sql` _[SQL](#sql)_ | SQL settings. Required when type is "sql". |  | Optional: \{\} <br /> |


#### MailDirectoryStatus



MailDirectoryStatus defines the observed state of a MailDirectory.



_Appears in:_
- [MailDirectory](#maildirectory)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation of the spec last applied. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the current state of the MailDirectory.<br />The "Ready" condition is true when the directory is reachable<br />and answers queries. |  | Optional: \{\} <br /> |


#### MailDomain



MailDomain is the Schema for the cnms mail domains.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cnms.rpp.gmbh/v1alpha1` | | |
| `kind` _string_ | `MailDomain` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[MailDomainSpec](#maildomainspec)_ | spec defines the desired state of the MailDomain |  | Required: \{\} <br /> |
| `status` _[MailDomainStatus](#maildomainstatus)_ | status defines the observed state of the MailDomain |  | Optional: \{\} <br /> |


#### MailDomainGrant



MailDomainGrant grants one namespace the right to use one domain for
mail: sending, receiving, DKIM signing, certificates and DNS records.

Grants are cluster-scoped, admin-owned resources (GitOps). The object
name is the domain itself, so the API server guarantees exactly one
grant per exact domain and normalizes the name (lowercase, no
trailing dot, no underscores). Domain authorization is a pure
function of the grants and the claimed `MailDomain`s; see the domain
authorization documentation for the uniqueness and wildcard rules.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cnms.rpp.gmbh/v1alpha1` | | |
| `kind` _string_ | `MailDomainGrant` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[MailDomainGrantSpec](#maildomaingrantspec)_ | spec defines the desired state of the MailDomainGrant |  | Required: \{\} <br /> |
| `status` _[MailDomainGrantStatus](#maildomaingrantstatus)_ | status defines the observed state of the MailDomainGrant |  | Optional: \{\} <br /> |


#### MailDomainGrantSpec



MailDomainGrantSpec defines the desired state of a MailDomainGrant.



_Appears in:_
- [MailDomainGrant](#maildomaingrant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespace` _string_ | Namespace that is allowed to use this domain. The grant binds to<br />an explicit namespace name (not a label selector, which a tenant<br />could influence). |  | MaxLength: 63 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |
| `includeSubdomains` _boolean_ | IncludeSubdomains also grants all subdomains of the domain, at<br />any depth. The apex domain is always covered. A more specific<br />grant for a subdomain takes precedence over this grant. Defaults<br />to false. | false | Optional: \{\} <br /> |


#### MailDomainGrantStatus



MailDomainGrantStatus defines the observed state of a MailDomainGrant.

Conditions are informational only; they never feed back into
authorization decisions.



_Appears in:_
- [MailDomainGrant](#maildomaingrant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation of the spec last applied. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the current state of the MailDomainGrant. |  | Optional: \{\} <br /> |


#### MailDomainSpec



MailDomainSpec defines the desired state of a MailDomain.

A MailDomain owns the public DNS records of one domain: DKIM, SPF,
DMARC, MTA-STS and TLS-RPT. The status lists every required record
and whether a lookup verified it.



_Appears in:_
- [MailDomain](#maildomain)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `domain` _string_ | Domain name, e.g. "example.org". Lowercase DNS name with at<br />least two labels of up to 63 characters (A-labels only, no<br />trailing dot). The last label starts with a letter, so an IP<br />address is not a domain. The same rule applies to the name of a<br />MailDomainGrant; keep the two patterns in sync. Immutable:<br />DKIM keys, DNS records and the domain authorization belong to<br />one domain; create a new MailDomain for another one. |  | MaxLength: 253 <br />Pattern: `^([a-z0-9]([a-z0-9-]\{0,61\}[a-z0-9])?[.])+[a-z]([a-z0-9-]\{0,61\}[a-z0-9])?$` <br />Required: \{\} <br /> |
| `serverRef` _[NameReference](#namereference)_ | ServerRef references the MailServer in the same namespace that<br />serves the domain: it signs, accepts and delivers the mail of<br />the domain (ADR 0014). The server need not exist yet; the<br />ServerFound condition says whether it does. Mutable, which moves<br />the domain to another server: the DKIM keys and DNS records<br />stay with the MailDomain, but the mail data on the old server<br />(mailboxes, queued mail) does not move (see the how-to guide<br />for the risks). |  | Required: \{\} <br /> |
| `dnsMode` _string_ | DNS mode: "manual" (default) reports the required records in<br />the status for the operator to create; "externalDNS" creates<br />DNSEndpoint resources. | manual | Enum: [manual externalDNS] <br />Optional: \{\} <br /> |
| `dkim` _[DKIM](#dkim)_ | DKIM signing configuration. |  | Optional: \{\} <br /> |
| `dmarc` _[DMARC](#dmarc)_ | DMARC policy. Records published by CNMS use DMARC1 semantics<br />(RFC 9989-9991). |  | Optional: \{\} <br /> |
| `spf` _[SPF](#spf)_ | SPF record generation: the sending sources of the domain. |  | Optional: \{\} <br /> |
| `mtaSts` _[MTAStS](#mtasts)_ | MTA-STS policy reporting. CNMS does not serve the policy. |  | Optional: \{\} <br /> |
| `tlsRpt` _[TLSRPT](#tlsrpt)_ | TLS-RPT reporting. |  | Optional: \{\} <br /> |


#### MailDomainStatus



MailDomainStatus defines the observed state of a MailDomain.



_Appears in:_
- [MailDomain](#maildomain)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation of the spec last applied. |  | Optional: \{\} <br /> |
| `records` _[DNSRecord](#dnsrecord) array_ | Records lists every DNS record required by this domain and<br />whether a lookup verified it. The list is atomic: neither the<br />name nor name and type identify a record, because one name can<br />hold several records of the same type (MX, A). |  | MaxItems: 64 <br />Optional: \{\} <br /> |
| `dkim` _[DKIMStatus](#dkimstatus)_ | DKIM reports the current signing state. |  | Optional: \{\} <br /> |
| `mtaSts` _[MTAStSStatus](#mtastsstatus)_ | MTAStS reports the published policy state. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the current state of the MailDomain:<br />Authorized (the domain is granted to the namespace and unique),<br />ServerFound (the MailServer in spec.serverRef exists; a warning<br />that never feeds into Ready), DKIMKeysValid (every key is present and usable), DKIMRotation<br />(false while a new key waits for its DNS record),<br />RecordsVerified (a lookup found the required records),<br />DNSPublished (the DNSEndpoint is managed, externalDNS mode),<br />SPFReady (an SPF record can be generated), MTASTSReady (a policy<br />exists, when MTA-STS is enabled) and Ready. "Ready" is true when<br />the domain is authorized, the DKIM keys are valid and every<br />required record is verified. |  | Optional: \{\} <br /> |


#### MailServer



MailServer is the Schema for the cnms mail servers.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cnms.rpp.gmbh/v1alpha1` | | |
| `kind` _string_ | `MailServer` | | |
| `kind` _string_ | Kind is a string value representing the REST resource this object represents.<br />Servers may infer this from the endpoint the client submits requests to.<br />Cannot be updated.<br />In CamelCase.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds |  | Optional: \{\} <br /> |
| `apiVersion` _string_ | APIVersion defines the versioned schema of this representation of an object.<br />Servers should convert recognized schemas to the latest internal value, and<br />may reject unrecognized values.<br />More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources |  | Optional: \{\} <br /> |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[MailServerSpec](#mailserverspec)_ | spec defines the desired state of the MailServer |  | Required: \{\} <br /> |
| `status` _[MailServerStatus](#mailserverstatus)_ | status defines the observed state of the MailServer |  | Optional: \{\} <br /> |


#### MailServerSpec



MailServerSpec defines the desired state of a MailServer.

A MailServer is a tenant-facing mail system: inbound and submission
services, outbound delivery and mailboxes. The domains it handles
are the MailDomains of its namespace that reference it in
spec.serverRef (status.domains lists them). Each MailServer is
isolated from all others: own workloads, queues, IPs, DKIM keys and
Rspamd/Valkey instances.



_Appears in:_
- [MailServer](#mailserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `directoryRef` _[NameReference](#namereference)_ | Directory used for recipient validation and user authentication.<br />References a MailDirectory in the same namespace. Optional: a<br />server without a directory needs another user source, which is<br />MailUser accounts (ADR 0008, not implemented yet) or, for tests,<br />submission.insecureStaticUsers. Without any user source the<br />server is not Ready (reason NoUserSource). |  | Optional: \{\} <br /> |
| `outbound` _[Outbound](#outbound)_ | Outbound selects the outbound delivery mode. |  | Required: \{\} <br /> |
| `tls` _[TLS](#tls)_ | TLS for inbound, submission and outbound mail. The certificate<br />is managed by cert-manager from the referenced issuer. |  | Required: \{\} <br /> |
| `inbound` _[Inbound](#inbound)_ | Inbound mail (port 25) service settings. |  | Optional: \{\} <br /> |
| `submission` _[Submission](#submission)_ | Submission (port 587) settings. |  | Optional: \{\} <br /> |
| `mailboxes` _[Mailboxes](#mailboxes)_ | Mailboxes configures Dovecot mail storage (IMAP, LMTP delivery).<br />When disabled while submission is enabled, Dovecot runs in<br />auth-only mode. |  | Optional: \{\} <br /> |
| `postfix` _[Postfix](#postfix)_ | Postfix workload settings. |  | Optional: \{\} <br /> |
| `rspamd` _[Rspamd](#rspamd)_ | Rspamd workload settings. |  | Optional: \{\} <br /> |
| `valkey` _[Valkey](#valkey)_ | Valkey (Rspamd state) settings. |  | Optional: \{\} <br /> |
| `availability` _[Availability](#availability)_ | Availability settings (disruption budget, topology spread). |  | Optional: \{\} <br /> |
| `observability` _[Observability](#observability)_ | Observability settings. |  | Optional: \{\} <br /> |
| `backup` _[Backup](#backup)_ | Backup settings (k8up annotations on the managed volumes). |  | Optional: \{\} <br /> |


#### MailServerStatus



MailServerStatus defines the observed state of a MailServer.



_Appears in:_
- [MailServer](#mailserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation of the spec last applied. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the current state of the MailServer. The<br />"Ready" condition is true when all managed workloads are<br />running with validated configuration. |  | Optional: \{\} <br /> |
| `domains` _[ServerDomainStatus](#serverdomainstatus) array_ | Domains lists the MailDomains of the namespace that reference<br />this MailServer in spec.serverRef: those that are not served<br />first, then by name, at most 128 entries (the DomainsListed<br />condition says when the list is cut). It is derived from the<br />MailDomains and holds no state of its own. |  | MaxItems: 128 <br />Optional: \{\} <br /> |


#### Mailboxes



Mailboxes configures Dovecot mail storage.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enable mail storage (IMAP access, LMTP delivery). Defaults to<br />false. | false | Optional: \{\} <br /> |
| `storage` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the mail storage volume. Defaults to 10Gi. Immutable<br />once set: the volume claim template of a StatefulSet cannot<br />change, and existing volumes are resized manually (ADR 0010). | 10Gi | Optional: \{\} <br /> |
| `quota` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Per-user quota. Unset means no quota. |  | Optional: \{\} <br /> |


#### NameReference



NameReference references a CNMS resource in the same namespace.

All references between CNMS resources are namespace-scoped: a resource
may only reference resources in its own namespace. This keeps tenant
isolation simple and requires no cross-namespace RBAC.



_Appears in:_
- [DirectOutbound](#directoutbound)
- [MailDomainSpec](#maildomainspec)
- [MailServerSpec](#mailserverspec)
- [RelayOutbound](#relayoutbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the referenced resource. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Required: \{\} <br /> |


#### Observability



Observability configures monitoring integrations.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `prometheusOperator` _string_ | Create Prometheus Operator ServiceMonitors: "auto" creates them<br />when the CRDs are present, "enabled" requires them, "disabled"<br />never creates them. Defaults to auto. | auto | Enum: [auto enabled disabled] <br />Optional: \{\} <br /> |


#### Outbound



Outbound selects the outbound delivery mode.
Exactly one of direct or relay must be set.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `direct` _[DirectOutbound](#directoutbound)_ | Deliver directly from the mail server's own sending addresses<br />(reputation is tied to those addresses). |  | Optional: \{\} <br /> |
| `relay` _[RelayOutbound](#relayoutbound)_ | Deliver via a smart host. |  | Optional: \{\} <br /> |


#### Postfix



Postfix configures the Postfix workload.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Number of replicas. Each replica runs with its own queue<br />volume. Defaults to 2. | 2 | Maximum: 10 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `queueStorage` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the per-replica queue volume. Defaults to 1Gi. Immutable<br />once set: the volume claim template of a StatefulSet cannot<br />change, and existing volumes are resized manually (ADR 0010). | 1Gi | Optional: \{\} <br /> |


#### RelayOutbound



RelayOutbound delivers mail via a smart host.



_Appears in:_
- [Outbound](#outbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `relayRef` _[NameReference](#namereference)_ | Reference to a MailRelay in the same namespace. |  | Required: \{\} <br /> |


#### Rspamd



Rspamd configures the Rspamd workload.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Number of replicas. Defaults to 2. | 2 | Maximum: 10 <br />Minimum: 1 <br />Optional: \{\} <br /> |


#### SPF



SPF configures the SPF record of the domain. The record lists these
sources, in the order ip4, ip6, include, each sorted, followed by the
qualifier and "all". Without any source no record is published.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ip4` _string array_ | IPv4 addresses or CIDR ranges allowed to send for the domain<br />(ip4: mechanisms). |  | MaxItems: 16 <br />items:MaxLength: 64 <br />Optional: \{\} <br /> |
| `ip6` _string array_ | IPv6 addresses or CIDR ranges allowed to send for the domain<br />(ip6: mechanisms). |  | MaxItems: 16 <br />items:MaxLength: 64 <br />Optional: \{\} <br /> |
| `include` _string array_ | Domains whose SPF records are included (include: mechanisms), for<br />example the one of a mail provider. At most 5: SPF allows ten<br />DNS lookups in total (RFC 7208), and CNMS does not follow the<br />includes. |  | MaxItems: 5 <br />items:MaxLength: 253 <br />items:Pattern: `^([a-z0-9_]([a-z0-9_-]\{0,61\}[a-z0-9_])?[.])+[a-z]([a-z0-9-]\{0,61\}[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `qualifier` _string_ | Result for senders not listed: "softfail" (~all, default), "fail"<br />(-all) or "neutral" (?all). | softfail | Enum: [softfail fail neutral] <br />Optional: \{\} <br /> |


#### SQL



SQL configures a SQL user directory (PostgreSQL).



_Appears in:_
- [MailDirectorySpec](#maildirectoryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | Server host name or IPv4 address, without a port (see port). |  | MaxLength: 253 <br />Pattern: `^[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?)*$` <br />Required: \{\} <br /> |
| `port` _integer_ | Server port. Defaults to 5432. | 5432 | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `database` _string_ | Database name: letters, digits, "_", "-" and "." only, so that<br />the value cannot add keywords to the connection string. |  | MaxLength: 63 <br />Pattern: `^[A-Za-z0-9_][A-Za-z0-9_.-]*$` <br />Required: \{\} <br /> |
| `username` _string_ | Database user: letters, digits, "_", "-" and "." only, so that<br />the value cannot add keywords to the connection string. |  | MaxLength: 63 <br />Pattern: `^[A-Za-z0-9_][A-Za-z0-9_.-]*$` <br />Required: \{\} <br /> |
| `passwordSecretRef` _[SecretReference](#secretreference)_ | Secret with the database password (key "password"). |  | Required: \{\} <br /> |
| `tls` _[SQLTLS](#sqltls)_ | Transport security. Defaults to verify-full. |  | Optional: \{\} <br /> |


#### SQLTLS



SQLTLS configures transport security for the SQL connection.



_Appears in:_
- [SQL](#sql)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode of the connection: "verify-full" encrypts and verifies the<br />host name (default), "require" encrypts without host name<br />verification, "disable" sends cleartext. | verify-full | Enum: [verify-full require disable] <br />Optional: \{\} <br /> |
| `caSecretRef` _[SecretReference](#secretreference)_ | CASecretRef references a Secret with the CA bundle (key<br />"ca.crt") used to verify the server certificate. |  | Optional: \{\} <br /> |


#### SecretKeyReference



SecretKeyReference references one key of a Secret in the same
namespace.



_Appears in:_
- [DKIMKey](#dkimkey)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Required: \{\} <br /> |
| `key` _string_ | Key in the Secret data. |  | MaxLength: 253 <br />Pattern: `^[-._a-zA-Z0-9]+$` <br />Required: \{\} <br /> |


#### SecretReference



SecretReference references a Secret in the same namespace.



_Appears in:_
- [LDAP](#ldap)
- [LDAPTLS](#ldaptls)
- [SQL](#sql)
- [SQLTLS](#sqltls)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Required: \{\} <br /> |


#### ServerDomainStatus



ServerDomainStatus reports one MailDomain that references the
MailServer.



_Appears in:_
- [MailServerStatus](#mailserverstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the MailDomain object. |  | MaxLength: 253 <br />Required: \{\} <br /> |
| `domain` _string_ | Domain is the domain name of the MailDomain (spec.domain). |  | MaxLength: 253 <br />Required: \{\} <br /> |
| `served` _boolean_ | Served is true when the domain is authorized and part of the<br />configuration the operator renders for the server. Whether the<br />pods already run it is reported by the WorkloadsReady condition. |  |  |
| `reason` _string_ | Reason why the domain is served or not (a reason of the<br />DomainsAuthorized condition, or the reason that blocks the<br />whole server). |  | MaxLength: 128 <br />Required: \{\} <br /> |


#### Submission



Submission configures authenticated submission (port 587).



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enable submission. Defaults to true. | true | Optional: \{\} <br /> |
| `insecureStaticUsers` _[InsecureStaticUsers](#insecurestaticusers)_ | InsecureStaticUsers provides test users from a Secret instead<br />of the MailDirectory for authentication (Phase 2 test source).<br />The Secret must contain the key "users.yaml" with password<br />hashes, never plaintext passwords. The operator only accepts it<br />when started with --enable-insecure-test-features. |  | Optional: \{\} <br /> |


#### TLS



TLS configures the certificate of a MailServer via cert-manager.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `issuerRef` _[IssuerReference](#issuerreference)_ | Issuer used to request the certificate. |  | Required: \{\} <br /> |
| `secretName` _string_ | Name of the Secret the certificate is written to. Defaults to<br />`"<name>-tls"`. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` <br />Optional: \{\} <br /> |


#### TLSRPT



TLSRPT configures TLS-RPT for the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enable TLS-RPT for this domain. Defaults to false. | false | Optional: \{\} <br /> |
| `rua` _string_ | Reporting address: a plain email address without the "mailto:"<br />scheme, which the renderer adds. Characters that would end the<br />URI or the tag (":", ";", ",", quotes, "!", whitespace) are<br />rejected. Defaults to the DMARC rua when set. |  | MaxLength: 254 <br />Pattern: `^[^\x00-\x20\x7f:;,"'!@<>?%\\]+@[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([-a-zA-Z0-9]\{0,61\}[a-zA-Z0-9])?)+$` <br />Optional: \{\} <br /> |


#### TopologySpread



TopologySpread configures topology spread constraints.
WhenUnsatisfiable is always ScheduleAnyway.



_Appears in:_
- [Availability](#availability)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `maxSkew` _integer_ | Maximum allowed skew between topology domains. Defaults to 1. | 1 | Minimum: 1 <br />Optional: \{\} <br /> |
| `topologyKey` _string_ | Topology key, a label key such as "topology.kubernetes.io/zone".<br />Defaults to kubernetes.io/hostname. | kubernetes.io/hostname | MaxLength: 317 <br />Pattern: `^([a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]\{0,61\}[A-Za-z0-9])?$` <br />Optional: \{\} <br /> |


#### Valkey



Valkey configures the Rspamd key-value store.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `persistence` _boolean_ | Persist Valkey data (Bayes, fuzzy and rate-limit state).<br />Defaults to false. | false | Optional: \{\} <br /> |
| `storage` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the persistence volume. Required when persistence is<br />enabled. |  | Optional: \{\} <br /> |


