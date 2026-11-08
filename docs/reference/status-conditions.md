# Status conditions

:::note
API version `v1alpha1`. The tables list what the operator sets today;
they are maintained by hand from the controllers
(`internal/controller`). The Go constants are the source of truth.
:::

All conditions are `metav1.Condition`s. `kubectl get mailserver` shows
the `Ready` column; `kubectl describe` shows every condition with its
reason and message.

## MailServer

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `Ready` | `True` | `Ready` | All of the conditions below that feed into it are `True` |
| `Ready` | `False` or `Unknown` | the reason of the first failing condition (order: `CertificateReady`, `ConfigValidated`, `WorkloadsReady`, `DomainsAuthorized`, `HostnameAuthorized`, `Observability`) | The rollup; see the rows below for those reasons |
| `Ready` | `False` | `DirectoryNotFound` | `spec.directoryRef` names a `MailDirectory` that does not exist in the namespace |
| `Ready` | `False` | `DirectoryInvalid` | the `MailDirectory` cannot be rendered: a Secret it names is missing or incomplete (or a password holds a control character), or the active filter does not compile, or the engines refuse a setting (`InvalidSettings` of the `MailDirectory`); the message says which. The running revision keeps running |
| `Ready` | `False` | `HostnameNotAuthorized`, `DuplicateHostname` | the hostname is not authorized (see `HostnameAuthorized`); the server runs with the name of its Postfix Service and serves its domains, but is not ready for what its spec asks for |
| `Ready` | `False` | `NoDomains` | no `MailDomain` of the namespace references the server in `spec.serverRef`; nothing is deployed, and a running data plane is re-rendered fail closed ([ADR 0014](../decisions/0014-domain-server-binding-and-hostname.md)) |
| `Ready` | `False` | `NoUserSource` | no `spec.directoryRef` and no other user source |
| `Ready` | `False` | `UserSourceConflict` | the spec configures more than one user source, today `spec.directoryRef` together with `spec.submission.insecureStaticUsers`; the message names them. A server has one user source, so none is picked silently. The running revision keeps running, and an Event says so; remove one source to resolve it ([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md), section 3) |
| `Ready` | `False` | `InsecureStaticUsersDisabled` | `spec.submission.insecureStaticUsers` is set but the operator runs without `--enable-insecure-test-features` |
| `Ready` | `False` | `StaticUsersInvalid` | the users Secret is missing, has no `users.yaml` key, or does not parse |
| `Ready` | `False` | `ClusterIssuerNotAllowed` | `spec.tls.issuerRef` is a `ClusterIssuer` and the operator runs without `--allow-cluster-issuers` |
| `Ready` | `False` | `RelayNotAvailable` | `outbound.relay` is set; relay mode arrives in Phase 6 |
| `Ready` | `False` | `IpPoolClaimNotAvailable` | `outbound.direct.ipPoolClaimRef` is set; IP management arrives in Phase 7 |
| `Ready` | `False` | `OwnershipConflict` | an object the data plane needs exists and is not controlled by this `MailServer` (also reported by `Observability`) |
| `Ready` | `False` | `ApplyFailed` | applying the data plane failed; the message has the error |
| `DomainsAuthorized` | `True` | `Authorized` | every `MailDomain` that references the server is covered by a `MailDomainGrant` for the namespace |
| `DomainsAuthorized` | `True` | `NoDomains` | no `MailDomain` references the server: nothing is unauthorized and nothing is served |
| `DomainsAuthorized` | `True` | `AuthorizationDisabled` | the operator runs with `--domain-authorization=unrestricted` |
| `DomainsAuthorized` | `False` | `NotGranted` | no grant covers a domain for the namespace |
| `DomainsAuthorized` | `False` | `DuplicateDomain` | several `MailDomain` objects claim the domain among the competing claimants; all are blocked |
| `DomainsAuthorized` | `False` | `PublicSuffix` | a referenced domain is a public suffix such as `co.uk` |
| `DomainsAuthorized` | `False` | `PartiallyAuthorized` | some, not all, domains are authorized; only those are rendered and the message names the blocked ones |
| `HostnameAuthorized` | `True` | `Authorized` | a `MailDomainGrant` covers `spec.hostname` for the namespace and no other `MailServer` among the competing ones uses it |
| `HostnameAuthorized` | `True` | `AuthorizationDisabled` | the operator runs with `--domain-authorization=unrestricted` (the hostname must still be unique and not a public suffix) |
| `HostnameAuthorized` | `False` | `HostnameNotAuthorized` | no grant covers the hostname for the namespace, or it is a public suffix such as `co.uk`; the message says which. The server presents the in-cluster name of its Postfix Service as HELO and certificate name, never lists the hostname in MX records or an MTA-STS policy, and keeps serving its authorized domains ([ADR 0014](../decisions/0014-domain-server-binding-and-hostname.md)) |
| `HostnameAuthorized` | `False` | `DuplicateHostname` | several `MailServer` objects use the hostname among the competing ones (the owner namespace in allowlist mode, every namespace in unrestricted mode); all are blocked from it, with the same effect as `HostnameNotAuthorized` |
| `CertificateReady` | `True` | `Issued` | the TLS Secret carries `tls.crt` and `tls.key` |
| `CertificateReady` | `False` | `SecretNotFound` | the TLS Secret does not exist yet |
| `CertificateReady` | `False` | `SecretIncomplete` | the TLS Secret lacks `tls.crt` or `tls.key` |
| `CertificateReady` | `False` | `SecretReadError` | reading the Secret failed |
| `ConfigValidated` | `True` | `Validated` | the configuration of every component passed its validation Job (ADR 0009) |
| `ConfigValidated` | `Unknown` | `ValidatingConfig` | a validation Job is still running; the pod templates are unchanged |
| `ConfigValidated` | `False` | `ConfigRejected` | a validation Job failed; the running pods keep their configuration |
| `WorkloadsReady` | `True` | `AllWorkloadsReady` | every workload runs its desired template with all replicas ready |
| `WorkloadsReady` | `False` | `Progressing` | a rollout or the first start is converging |
| `WorkloadsReady` | `False` | `RolloutStalled` | a Deployment exceeded its progress deadline, or the workloads were not ready for 10 minutes |
| `Observability` | `True` | `MonitorsCreated` | the ServiceMonitors are managed |
| `Observability` | `True` | `CRDsUnavailable` | `auto` and the ServiceMonitor CRDs are not installed; metrics stay on the component endpoints |
| `Observability` | `True` | `Disabled` | `prometheusOperator: disabled`; no ServiceMonitors |
| `Observability` | `False` | `CRDsRequired` | `prometheusOperator: enabled` but the CRDs are not installed |
| `Observability` | `False` | `CRDCheckFailed`, `ListFailed`, `DeleteFailed`, `ApplyFailed`, `OwnershipConflict` | the integration could not be checked or applied; the message has the error |
| `DomainsListed` | `True` | `AllListed` | `status.domains` lists every `MailDomain` that references the server |
| `DomainsListed` | `False` | `Truncated` | more than 128 `MailDomain`s reference the server; `status.domains` lists 128 of them, those not served first, then by name |
| `AddressRecords` | `True` | `AddressesListed` | `status.records` lists an `A` or `AAAA` record for every ingress address of the inbound `LoadBalancer` Service |
| `AddressRecords` | `False` | `NoExternalAddress` | the inbound Service is of type `ClusterIP`: it has no address outside the cluster, so no `A` or `AAAA` record is required |
| `AddressRecords` | `False` | `LoadBalancerHostname` | the load balancer reports a DNS name instead of an address, so no `A` or `AAAA` record is listed; point the hostname at that name with a `CNAME` or an alias record |
| `AddressRecords` | `False` | `AddressPending` | the Service does not exist yet, or the load balancer has no address yet |
| `RecordsVerified` | `True` | `AllVerified`, `NoRecords` | every record of `status.records` was found, or none is required |
| `RecordsVerified` | `False` | `RecordsNotVerified` | a record is missing or different; the message names the first three with the reason |
| `RecordsVerified` | `Unknown` | `Checking`, `LookupFailing` | a record has not been looked up successfully yet, or three lookup rounds in a row failed with a temporary error; a failing lookup is never `False` |
| `ClientIPPreserved` | `True` | `Preserved` | client addresses reach Postfix unchanged |
| `ClientIPPreserved` | `False` | `ClusterTrafficPolicy` | `externalTrafficPolicy: Cluster` on a LoadBalancer source-NATs clients, so the per-client rate limits count all senders together |
| `DedicatedSendingAddress` | `True` | `Dedicated` | outbound mail has its own address or goes through a relay |
| `DedicatedSendingAddress` | `False` | `NodeSourceAddress` | `outbound.direct` without `ipPoolClaimRef` sends from the node address (ADR 0002) |
| `NodeDrainable` | `True` | `Drainable` | the Postfix disruption budget allows evicting one pod |
| `NodeDrainable` | `False` | `BudgetBlocksDrain` | `postfixMinAvailable` keeps every Postfix replica, so a node drain waits |
| `DKIMKeysAvailable` | `True` | `KeysAvailable` | every served domain has all its active signing keys, and each key was read and parsed (also when no domain is served) |
| `DKIMKeysAvailable` | `False` | `KeysMissing` | a served domain has no active key yet, or a key Secret is missing, has no such key, or does not parse; the message lists each domain and key. Outbound mail of those domains is deferred (451 4.7.1), inbound mail is not affected, and the rollout does not wait. Looked up again every minute ([ADR 0011](../decisions/0011-dkim-keys-and-rotation.md), [ADR 0013](../decisions/0013-never-unsigned-guard.md)) |

`ClientIPPreserved`, `DedicatedSendingAddress`, `NodeDrainable`,
`DomainsListed`, `DKIMKeysAvailable`, `AddressRecords` and `RecordsVerified`
are warnings: they never feed into `Ready`. `HostnameAuthorized` does: a
server with a hostname it is not allowed to present is not ready, although
its data plane runs.

`RecordsVerified` and `AddressRecords` exist only while the hostname is
authorized; the status lists no record without it. `status.records` holds
the `A` and `AAAA` records of the hostname for the addresses of the inbound
Service, and the `SPF` `TXT` record of the hostname built from
`spec.spf` (left out while a `MailDomain` of the hostname itself carries
it). The records are reported and verified, not published.

`status.domains` has one entry per `MailDomain` that references the server
(`name`, `domain`, `served`, `reason`). `served` is true when the domain is
authorized and part of the rendered configuration; `reason` is the reason
of `DomainsAuthorized` for that domain, or the reason that blocks the whole
server.

## MailDomainGrant

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `NamespaceExists` | `True` | `NamespaceExists` | the namespace in `spec.namespace` exists |
| `NamespaceExists` | `False` | `NamespaceNotFound` | the namespace does not exist; a Warning Event is emitted |

The condition is informational: it never feeds back into authorization.

## MailDomain

Set by the `MailDomain` controller ([ADR 0011](../decisions/0011-dkim-keys-and-rotation.md),
[ADR 0012](../decisions/0012-dns-records-and-dmarc.md)). `kubectl get
maildomains` shows `Authorized` and `Ready`. An unauthorized domain has
only `Authorized` and `Ready` (and `DNSPublished` while its
`DNSEndpoint` waits for removal); the others are dropped, and
`status.records` and `status.dkim` are empty.

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `Ready` | `True` | `Ready` | authorized, keys valid, and every required record verified |
| `Ready` | `False` or `Unknown` | the reason of the first condition that is not `True` (order: `Authorized`, `DKIMKeysValid`, `DNSPublished`, `RecordsVerified`) | The rollup. In `manual` mode `Ready` stays `False` until you publish the records |
| `Authorized` | `True` | `Authorized` | a `MailDomainGrant` covers the domain for the namespace and no other `MailDomain` of the owner claims it |
| `Authorized` | `True` | `AuthorizationDisabled` | the operator runs with `--domain-authorization=unrestricted` |
| `Authorized` | `False` | `NotGranted` | no grant covers the domain for the namespace |
| `Authorized` | `False` | `DuplicateDomain` | several `MailDomain` objects claim the domain; all are blocked |
| `Authorized` | `False` | `PublicSuffix` | the domain is a public suffix such as `co.uk` |
| `ServerFound` | `True` | `ServerFound` | the `MailServer` in `spec.serverRef` exists |
| `ServerFound` | `False` | `ServerNotFound` | the `MailServer` in `spec.serverRef` does not exist; keys and records are still generated. A warning: it never feeds into `Ready` |
| `DKIMKeysValid` | `True` | `KeysValid` | every key is present and usable |
| `DKIMKeysValid` | `False` | `InvalidKey` | a key does not parse as RSA of at least 2048 bits or Ed25519; the message names the selector |
| `DKIMKeysValid` | `False` | `SecretNotFound` | the Secret or Secret key of one of your own keys is missing; it is read again after a minute |
| `DKIMKeysValid` | `False` | `KeySecretConflict` | a key Secret of that name belongs to another `MailDomain`; the message names it |
| `DKIMRotation` | `True` | `NoPendingKey` | no generated key waits for its DNS record |
| `DKIMRotation` | `True` | `UserManaged` | your own keys: the rotation is in `spec.dkim.keys` |
| `DKIMRotation` | `False` | `AwaitingDNS` | a pending key waits for its verified DNS record; the old key keeps signing |
| `RecordsVerified` | `True` | `AllVerified` | a lookup found every required record |
| `RecordsVerified` | `True` | `NoRecords` | no record is required (no usable key) |
| `RecordsVerified` | `False` | `RecordsNotVerified` | a record is missing or different; the message names the first three with the reason |
| `RecordsVerified` | `Unknown` | `Checking` | a record has not been looked up successfully yet |
| `RecordsVerified` | `Unknown` | `LookupFailing` | three lookup rounds in a row failed with a temporary error; a failing lookup is never `False` |
| `DNSPublished` | `True` | `DNSEndpointApplied` | `externalDNS` mode: the `DNSEndpoint` `<name>-dns` is managed |
| `DNSPublished` | `False` | `ExternalDNSNotInstalled` | `externalDNS` mode and the `DNSEndpoint` CRD is missing; the records are listed in the status and the CRD is looked up again every five minutes |
| `DNSPublished` | `False` | `OwnershipConflict` | a `DNSEndpoint` of that name exists and is not controlled by the `MailDomain` |
| `DNSPublished` | `False` | `ApplyFailed`, `CRDCheckFailed` | applying the `DNSEndpoint` or looking up the CRD failed; the message has the error |
| `DNSPublished` | `False` | `AuthorizationRevoked` | the domain lost its authorization; the `DNSEndpoint` is removed 15 minutes after the `Authorized` transition |
| `SPFReady` | `True` | `SPFGenerated` | an SPF record is generated from `spec.spf` and the sources of the `MailServer` (`MailServer.spec.spf`) that serves the domain |
| `SPFReady` | `False` | `NoSendingSources` | no source is known, in the domain or in its server, so no SPF record is generated |
| `MTASTSReady` | `True` | `PolicyAvailable` | MTA-STS is enabled and the policy is in `status.mtaSts` |
| `MTASTSReady` | `False` | `NoMailServer` | MTA-STS is enabled and the `MailServer` in `spec.serverRef` does not exist or does not serve the domain, so there is no `mx` host and no policy |
| `MTASTSReady` | `False` | `HostnameNotAuthorized` | MTA-STS is enabled and the `MailServer` serves the domain, but its hostname is not authorized (not granted, or used by several servers): there is no `mx` host and no policy, and the in-cluster name of the Service is never published |
| `ReportAuthorization` | `True` | `NotRequired` | every DMARC report address is in the organizational domain of the policy |
| `ReportAuthorization` | `False` | `ReceiverRecordRequired` | a report address is in another organizational domain; its owner has to publish the record named in the message |

`status.records` of a `MailDomain` also holds the `MX` record of the domain,
pointing at the hostname of the `MailServer` that serves it (`10 <hostname>`),
while that hostname is authorized. Backup exchangers at the name are not
compared: the record is verified when one `MX` record points at the hostname.
The `MX` record is reported and verified, never published by the
`DNSEndpoint`.

`DNSPublished` exists only in `externalDNS` mode, `MTASTSReady` only
with `spec.mtaSts.enabled`, and `ReportAuthorization` only with
`spec.dmarc`. `SPFReady`, `MTASTSReady` and `ReportAuthorization` are
informational: they never feed into `Ready`.

## MailDirectory

The `MailDirectory` controller checks the configuration and probes the
directory ([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
section 5). It changes no data plane: nothing reads these conditions yet.

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `Ready` | `True` | `Ready` | The configuration is valid: the referenced Secrets exist and carry their keys (`password`, `ca.crt`), the CA bundle holds a certificate, and the LDAP filter compiles |
| `Ready` | `False` | `SecretNotFound` | A Secret named in the spec does not exist (read again every minute) |
| `Ready` | `False` | `SecretIncomplete` | A Secret lacks the key that is read from it, or `ca.crt` holds no certificate |
| `Ready` | `False` | `InvalidFilter` | `ldap.activeFilter` is not one complete LDAP filter, or the filter the operator composes from it does not compile |
| `Ready` | `False` | `InvalidSettings` | The engines refuse a setting the probe would accept: LDAP `tls.mode: None` (Dovecot checks a password by binding as the user, so every login would send it in clear text), `%{` in the base DN or the bind DN (Dovecot would expand it), or a base DN, bind DN or password with a blank at either end or a control character. A `MailServer` that references the directory is `DirectoryInvalid` and keeps its running revision |
| `Reachable` | `True` | `Reachable` | The probe connected, bound and found at least one account with the composed filter (LDAP), or read a row of the users relation (SQL) |
| `Reachable` | `False` | `Unreachable` | The directory did not answer |
| `Reachable` | `False` | `BindFailed` | The directory rejected the credentials (LDAP bind, PostgreSQL login), or the user may not read the relation |
| `Reachable` | `False` | `TLSFailed` | The TLS handshake or the verification of the server certificate failed |
| `Reachable` | `False` | `NoMatchingEntries` | The lookup worked and found no account. A directory that is still empty gets this too, and so does an LDAP login attribute without substring matching, because the login guard (no leading dot, no `/`, not `sa.*`) then excludes every entry |
| `Reachable` | `False` | `RelationMissing` | A schema, a relation or a fixed column of the PostgreSQL directory does not exist |
| `Reachable` | `Unknown` | the reason of `Ready` | Not probed while `Ready` is `False` |

`Reachable` is a warning. It never feeds into the `Ready` of a
`MailServer` and never takes a rollout away: the operator pod may see a
different network than the data plane, and an outage of the directory must
not stop mail (it tempfails). The probe runs on every spec change and every
five minutes. The messages are fixed texts: the error text of a driver can
carry a credential, so it is never copied into a condition or an Event.
Warning Events are emitted when `Ready` or `Reachable` change for the worse;
a Normal Event is emitted when `Reachable` becomes `True` again after it was
`False`.

## MailUser

The `MailUser` controller decides the addresses of the account together with
the other accounts of its server and makes the credential Secret with the
password hash ([ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md), sections
3, 4 and 8). **A `MailUser` has no effect on mail yet**: the `MailServer` does
not read it, so no mailbox, login or sender comes of it. `kubectl get
mailusers` shows `Address`, `Server` and `Ready`.

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `Ready` | `True` | `Ready` | The server exists, the account is on it alone, the primary domain is served and authorized, and the credential is made |
| `Ready` | `True` | `Disabled` | `spec.disabled` is true: no login and no sending, mail is still delivered |
| `Ready` | `False` | `ServerNotFound` | `spec.serverRef` names no `MailServer` of the namespace |
| `Ready` | `False` | `DirectoryConfigured` | The server uses a `MailDirectory`: a server has either a directory or `MailUser`s. No credential is made |
| `Ready` | `False` | `UserSourceConflict` | The server has `submission.insecureStaticUsers`. No credential is made |
| `Ready` | `False` | `DomainNotFound`, `DomainNotServed`, `DomainNotAuthorized` | The primary domain (`spec.domainRef`) does not exist, is served by another server, or is not authorized (grant, duplicate, public suffix). The account is suspended; its aliases in a served domain show the same reason on their rows |
| `Ready` | `False` | the reason of `CredentialReady` | The credential is not ready (below) |
| `CredentialReady` | `True` | `Ready` | The hash is in the Secret `<name>-credential` |
| `CredentialReady` | `False` | `SecretNotFound` | `spec.passwordSecretRef` names a Secret that does not exist or has no key `password` (read again every minute). A credential that exists stays |
| `CredentialReady` | `False` | `PasswordInvalid` | The provided password has fewer than 12 characters. A credential that exists stays |
| `CredentialReady` | `False` | `HashFailed` | Reserved; not expected in practice. The random source of the operator (which does not fail in current Go versions) could not give a salt or a generated password |
| `CredentialReady` | `False` | `CredentialSecretConflict` | A Secret `<name>-credential` exists and the `MailUser` does not own it: it is never taken over, because its author would choose the hash |
| `AddressesServed` | `True` | `AllServed` | Every address of the account is served |
| `AddressesServed` | `False` | `PartiallyServed` | Some address is not served; the message lists them with their reasons. A warning: it never feeds `Ready`, and the other addresses keep working |
| `AddressesServed` | `Unknown` | `ServerNotFound`, `DirectoryConfigured` or `UserSourceConflict` | Nothing serves the account (the reason of `Ready`), so the addresses are not decided and `status.addresses` is empty |

`status.addresses[]` has one row per address, with `kind` (`primary` or
`alias`), `served` and `reason`: `Served`, `AliasDomainNotFound`,
`AliasDomainNotServed`, `AliasDomainNotAuthorized` or `AddressConflict` for an
alias; the primary address and the aliases of a suspended account show the
domain reason above instead. An address that two accounts of the same rank
claim (two aliases) is lost by both; an alias that names another account's
primary address loses to it.

The password rules: a provided password has at least 12 characters and is used
as it is, byte for byte. A Secret made with `kubectl create secret generic
--from-file` keeps the trailing newline of the file as part of the password, so
the login must send it too; use `--from-literal` or a file without the
newline. If the credential Secret of an account is deleted, a generated
password is replaced by a new one, silently: use `passwordSecretRef` for a
password that must survive a restore.

`status.credentialSecretName` and `status.credentialRevision` are set when the
credential exists. The revision (the `resourceVersion` of the Secret) changes
only when the password hash changes, not when the Secret is written for another
reason. A provided password is read again every minute, a generated one is
checked every ten, because Secrets are not watched. Warning Events are emitted
when `Ready` or `CredentialReady` change for the worse; a Normal Event when
`Ready` returns. The messages are fixed texts: no password or hash is ever in
a condition, an Event or the log.
