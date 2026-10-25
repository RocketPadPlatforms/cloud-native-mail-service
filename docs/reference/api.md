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
| `postfixMinAvailable` _[IntOrString](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#intorstring-intstr-util)_ | Minimum available Postfix replicas (PodDisruptionBudget).<br />Defaults to 1. | 1 | Optional: \{\} <br /> |
| `topologySpread` _[TopologySpread](#topologyspread)_ | Topology spread constraints for mail server pods. |  | Optional: \{\} <br /> |


#### Backup



Backup configures backup annotations (k8up) on managed resources.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Annotate managed volumes and pods for k8up. Defaults to true. | true | Optional: \{\} <br /> |


#### DKIM



DKIM configures DKIM signing for the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `selector` _string_ | Selector of the current signing key. Defaults to "cnms". | cnms | MaxLength: 30 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br />Optional: \{\} <br /> |
| `rotation` _[DKIMRotation](#dkimrotation)_ | Key rotation. When set, the operator rotates the key on the<br />given interval and publishes the new selector with an overlap<br />so that both selectors stay valid during the switch. Rotation<br />progress (schedule, start times) is recorded in the<br />"cnms.rpp.gmbh/rotation" annotation on the resource, because it<br />must survive a restore without status. |  | Optional: \{\} <br /> |


#### DKIMRotation



DKIMRotation configures DKIM key rotation.



_Appears in:_
- [DKIM](#dkim)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#duration-v1-meta)_ | Interval after which the key is rotated. |  | Required: \{\} <br /> |
| `overlap` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#duration-v1-meta)_ | Overlap during which the old and the new selector are both<br />valid. Defaults to 24h. | 24h0m0s | Optional: \{\} <br /> |


#### DKIMStatus



DKIMStatus reports the current DKIM signing state.



_Appears in:_
- [MailDomainStatus](#maildomainstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `selector` _string_ | Selector of the active key. |  | Optional: \{\} <br /> |
| `keyAge` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#duration-v1-meta)_ | Age of the active key. |  | Optional: \{\} <br /> |
| `pendingSelector` _string_ | Selector of the key being rolled in, during an overlap. |  | Optional: \{\} <br /> |


#### DMARC



DMARC configures the DMARC policy of the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policy` _string_ | Enforcement policy. Defaults to "none". | none | Enum: [none quarantine reject] <br />Optional: \{\} <br /> |
| `rua` _string_ | Reporting address for aggregate reports (rua). |  | MaxLength: 254 <br />Optional: \{\} <br /> |
| `ruf` _string_ | Reporting address for failure reports (ruf). |  | MaxLength: 254 <br />Optional: \{\} <br /> |
| `testing` _boolean_ | Testing mode: the t= tag is set, so policies are evaluated but<br />not enforced. Defaults to false. | false | Optional: \{\} <br /> |


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
| `serviceType` _string_ | Service type for inbound mail. Defaults to LoadBalancer. | LoadBalancer | Enum: [LoadBalancer ClusterIP] <br />Optional: \{\} <br /> |
| `externalTrafficPolicy` _string_ | externalTrafficPolicy of the inbound service. Defaults to Local<br />so that client addresses are preserved for postscreen and<br />rate limiting. | Local | Enum: [Local Cluster] <br />Optional: \{\} <br /> |


#### InsecureStaticUsers



InsecureStaticUsers points at a Secret with static test users.



_Appears in:_
- [Submission](#submission)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `secretName` _string_ | Name of the Secret in the same namespace. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |


#### IssuerReference



IssuerReference references a cert-manager Issuer or ClusterIssuer.



_Appears in:_
- [TLS](#tls)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind of the issuer. |  | Enum: [Issuer ClusterIssuer] <br />Required: \{\} <br /> |
| `name` _string_ | Name of the issuer. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |


#### LDAP



LDAP configures an LDAP user directory.



_Appears in:_
- [MailDirectorySpec](#maildirectoryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | Server host. |  | MaxLength: 253 <br />Required: \{\} <br /> |
| `port` _integer_ | Server port. Unset means the operator picks 389, or 636 when<br />tls.mode is LDAPS. |  | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `tls` _[LDAPTLS](#ldaptls)_ | Transport security. Defaults to StartTLS with verification. |  | Optional: \{\} <br /> |
| `baseDN` _string_ | Base DN for user searches. |  | MaxLength: 256 <br />Required: \{\} <br /> |
| `bindDN` _string_ | Bind DN used for searches. Empty means an anonymous bind. |  | MaxLength: 256 <br />Optional: \{\} <br /> |
| `bindSecretRef` _[SecretReference](#secretreference)_ | Secret with the bind password (key "password"). Required when<br />bindDN is set. |  | Optional: \{\} <br /> |
| `userFilter` _string_ | Search filter for users; %s is replaced with the user's mail<br />address. Defaults to "(mail=%s)". |  | MaxLength: 256 <br />Optional: \{\} <br /> |


#### LDAPTLS



LDAPTLS configures transport security for the LDAP connection.



_Appears in:_
- [LDAP](#ldap)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode of the connection: "StartTLS" upgrades an existing<br />connection (default), "LDAPS" connects directly over TLS, "None"<br />disables transport security. With "None", bindDN must not be<br />set (cleartext bind). | StartTLS | Enum: [StartTLS LDAPS None] <br />Optional: \{\} <br /> |
| `caSecretRef` _[SecretReference](#secretreference)_ | CASSecretRef references a Secret with the CA bundle (key<br />"ca.crt") used to verify the server certificate. |  | Optional: \{\} <br /> |


#### MTAStS



MTAStS configures MTA-STS for the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Publish an MTA-STS policy for this domain. Defaults to false.<br />The policy endpoint (`https://mta-sts.<domain>/.well-known/<br />mta-sts.txt`) is served by the CNMS policy server and requires a<br />valid public certificate for the domain. | false | Optional: \{\} <br /> |


#### MTAStSStatus



MTAStSStatus reports the MTA-STS policy state.



_Appears in:_
- [MailDomainStatus](#maildomainstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policyId` _integer_ | Monotonically increasing policy id. It changes whenever the<br />policy content changes. |  | Optional: \{\} <br /> |
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
| `type` _[DirectoryType](#directorytype)_ | Directory type. |  | Enum: [ldap sql] <br />Required: \{\} <br /> |
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
| `domain` _string_ | Domain name, e.g. "example.org". Lowercase DNS name with at<br />least two labels (A-labels only, no trailing dot). |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$` <br />Required: \{\} <br /> |
| `dnsMode` _string_ | DNS mode: "manual" (default) reports the required records in<br />the status for the operator to create; "externalDNS" creates<br />DNSEndpoint resources. | manual | Enum: [manual externalDNS] <br />Optional: \{\} <br /> |
| `dkim` _[DKIM](#dkim)_ | DKIM signing configuration. |  | Optional: \{\} <br /> |
| `dmarc` _[DMARC](#dmarc)_ | DMARC policy. Records published by CNMS use DMARC1 semantics<br />(RFC 9989-9991). |  | Optional: \{\} <br /> |
| `mtaSts` _[MTAStS](#mtasts)_ | MTA-STS policy publication. |  | Optional: \{\} <br /> |
| `tlsRpt` _[TLSRPT](#tlsrpt)_ | TLS-RPT reporting. |  | Optional: \{\} <br /> |


#### MailDomainStatus



MailDomainStatus defines the observed state of a MailDomain.



_Appears in:_
- [MailDomain](#maildomain)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation of the spec last applied. |  | Optional: \{\} <br /> |
| `records` _[DNSRecord](#dnsrecord) array_ | Records lists every DNS record required by this domain and<br />whether a lookup verified it. |  | Optional: \{\} <br /> |
| `dkim` _[DKIMStatus](#dkimstatus)_ | DKIM reports the current signing state. |  | Optional: \{\} <br /> |
| `mtaSts` _[MTAStSStatus](#mtastsstatus)_ | MTAStS reports the published policy state. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the current state of the MailDomain. The<br />"Ready" condition is true when the required records are<br />published and verified. |  | Optional: \{\} <br /> |


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
services, outbound delivery, mailboxes, and the set of domains it
handles. Each MailServer is isolated from all others: own workloads,
queues, IPs, DKIM keys and Rspamd/Valkey instances.



_Appears in:_
- [MailServer](#mailserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `directoryRef` _[NameReference](#namereference)_ | Directory used for recipient validation and user authentication.<br />References a MailDirectory in the same namespace. |  | Required: \{\} <br /> |
| `outbound` _[Outbound](#outbound)_ | Outbound selects the outbound delivery mode. |  | Required: \{\} <br /> |
| `domains` _[NameReference](#namereference) array_ | Domains handled by this mail server. They are signed with DKIM<br />(when enabled on the domain) and accepted for submission.<br />Each entry references a MailDomain in the same namespace. |  | MaxItems: 50 <br />MinItems: 1 <br />Required: \{\} <br /> |
| `tls` _[TLS](#tls)_ | TLS for inbound, submission and outbound mail. The certificate<br />is managed by cert-manager from the referenced issuer. |  | Required: \{\} <br /> |
| `inbound` _[Inbound](#inbound)_ | Inbound mail (port 25) service settings. |  | Optional: \{\} <br /> |
| `submission` _[Submission](#submission)_ | Submission (port 587) settings. |  | Optional: \{\} <br /> |
| `mailboxes` _[Mailboxes](#mailboxes)_ | Mailboxes configures Dovecot mail storage (IMAP, LMTP delivery).<br />When disabled while submission is enabled, Dovecot runs in<br />auth-only mode. |  | Optional: \{\} <br /> |
| `postfix` _[Postfix](#postfix)_ | Postfix workload settings. |  | Optional: \{\} <br /> |
| `rspamd` _[Rspamd](#rspamd)_ | Rspamd workload settings. |  | Optional: \{\} <br /> |
| `valkey` _[Valkey](#valkey)_ | Valkey (Rspamd state) settings. |  | Optional: \{\} <br /> |
| `availability` _[Availability](#availability)_ | Availability settings (disruption budget, topology spread). |  | Optional: \{\} <br /> |
| `observability` _[Observability](#observability)_ | Observability settings. |  | Optional: \{\} <br /> |
| `backup` _[Backup](#backup)_ | Backup settings (k8up annotations on managed resources). |  | Optional: \{\} <br /> |


#### MailServerStatus



MailServerStatus defines the observed state of a MailServer.



_Appears in:_
- [MailServer](#mailserver)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | ObservedGeneration is the generation of the spec last applied. |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the current state of the MailServer. The<br />"Ready" condition is true when all managed workloads are<br />running with validated configuration. |  | Optional: \{\} <br /> |


#### Mailboxes



Mailboxes configures Dovecot mail storage.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enable mail storage (IMAP access, LMTP delivery). Defaults to<br />false. | false | Optional: \{\} <br /> |
| `storage` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the mail storage volume. Defaults to 10Gi. | 10Gi | Optional: \{\} <br /> |
| `quota` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Per-user quota. Unset means no quota. |  | Optional: \{\} <br /> |


#### NameReference



NameReference references a CNMS resource in the same namespace.

All references between CNMS resources are namespace-scoped: a resource
may only reference resources in its own namespace. This keeps tenant
isolation simple and requires no cross-namespace RBAC.



_Appears in:_
- [DirectOutbound](#directoutbound)
- [MailServerSpec](#mailserverspec)
- [RelayOutbound](#relayoutbound)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the referenced resource. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |


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
| `queueStorage` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the per-replica queue volume. Defaults to 1Gi. | 1Gi | Optional: \{\} <br /> |


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


#### SQL



SQL configures a SQL user directory (PostgreSQL).



_Appears in:_
- [MailDirectorySpec](#maildirectoryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `host` _string_ | Server host. |  | MaxLength: 253 <br />Required: \{\} <br /> |
| `port` _integer_ | Server port. Defaults to 5432. | 5432 | Maximum: 65535 <br />Minimum: 1 <br />Optional: \{\} <br /> |
| `database` _string_ | Database name. |  | MaxLength: 63 <br />Required: \{\} <br /> |
| `username` _string_ | Database user. |  | MaxLength: 63 <br />Required: \{\} <br /> |
| `passwordSecretRef` _[SecretReference](#secretreference)_ | Secret with the database password (key "password"). |  | Required: \{\} <br /> |
| `tls` _[SQLTLS](#sqltls)_ | Transport security. Defaults to verify-full. |  | Optional: \{\} <br /> |


#### SQLTLS



SQLTLS configures transport security for the SQL connection.



_Appears in:_
- [SQL](#sql)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode of the connection: "verify-full" encrypts and verifies the<br />host name (default), "require" encrypts without host name<br />verification, "disable" sends cleartext. | verify-full | Enum: [verify-full require disable] <br />Optional: \{\} <br /> |
| `caSecretRef` _[SecretReference](#secretreference)_ | CASSecretRef references a Secret with the CA bundle (key<br />"ca.crt") used to verify the server certificate. |  | Optional: \{\} <br /> |


#### SecretReference



SecretReference references a Secret in the same namespace.



_Appears in:_
- [LDAP](#ldap)
- [LDAPTLS](#ldaptls)
- [SQL](#sql)
- [SQLTLS](#sqltls)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Required: \{\} <br /> |


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
| `secretName` _string_ | Name of the Secret the certificate is written to. Defaults to<br />`"<name>-tls"`. |  | MaxLength: 253 <br />Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` <br />Optional: \{\} <br /> |


#### TLSRPT



TLSRPT configures TLS-RPT for the domain.



_Appears in:_
- [MailDomainSpec](#maildomainspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enable TLS-RPT for this domain. Defaults to false. | false | Optional: \{\} <br /> |
| `rua` _string_ | Reporting address. Defaults to the DMARC rua when set. |  | MaxLength: 254 <br />Optional: \{\} <br /> |


#### TopologySpread



TopologySpread configures topology spread constraints.
WhenUnsatisfiable is always ScheduleAnyway.



_Appears in:_
- [Availability](#availability)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `maxSkew` _integer_ | Maximum allowed skew between topology domains. Defaults to 1. | 1 | Optional: \{\} <br /> |
| `topologyKey` _string_ | Topology key. Defaults to kubernetes.io/hostname. | kubernetes.io/hostname | MaxLength: 316 <br />Optional: \{\} <br /> |


#### Valkey



Valkey configures the Rspamd key-value store.



_Appears in:_
- [MailServerSpec](#mailserverspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `persistence` _boolean_ | Persist Valkey data (Bayes, fuzzy and rate-limit state).<br />Defaults to false. | false | Optional: \{\} <br /> |
| `storage` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the persistence volume. Required when persistence is<br />enabled. |  | Optional: \{\} <br /> |


