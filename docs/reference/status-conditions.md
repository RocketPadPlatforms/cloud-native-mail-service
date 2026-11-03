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
| `Ready` | `False` or `Unknown` | the reason of the first failing condition (order: `CertificateReady`, `ConfigValidated`, `WorkloadsReady`, `DomainsAuthorized`, `Observability`) | The rollup; see the rows below for those reasons |
| `Ready` | `False` | `DirectoryNotFound` | `spec.directoryRef` names a `MailDirectory` that does not exist in the namespace |
| `Ready` | `False` | `NoUserSource` | no `spec.directoryRef` and no other user source |
| `Ready` | `False` | `InsecureStaticUsersDisabled` | `spec.submission.insecureStaticUsers` is set but the operator runs without `--enable-insecure-test-features` |
| `Ready` | `False` | `StaticUsersInvalid` | the users Secret is missing, has no `users.yaml` key, or does not parse |
| `Ready` | `False` | `ClusterIssuerNotAllowed` | `spec.tls.issuerRef` is a `ClusterIssuer` and the operator runs without `--allow-cluster-issuers` |
| `Ready` | `False` | `RelayNotAvailable` | `outbound.relay` is set; relay mode arrives in Phase 6 |
| `Ready` | `False` | `IpPoolClaimNotAvailable` | `outbound.direct.ipPoolClaimRef` is set; IP management arrives in Phase 7 |
| `Ready` | `False` | `OwnershipConflict` | an object the data plane needs exists and is not controlled by this `MailServer` (also reported by `Observability`) |
| `Ready` | `False` | `ApplyFailed` | applying the data plane failed; the message has the error |
| `DomainsAuthorized` | `True` | `Authorized` | every referenced domain is covered by a `MailDomainGrant` for the namespace |
| `DomainsAuthorized` | `True` | `AuthorizationDisabled` | the operator runs with `--domain-authorization=unrestricted` |
| `DomainsAuthorized` | `False` | `NotGranted` | no grant covers a domain for the namespace |
| `DomainsAuthorized` | `False` | `DuplicateDomain` | several `MailDomain` objects claim the domain among the competing claimants; all are blocked |
| `DomainsAuthorized` | `False` | `DomainNotFound` | a `spec.domains` entry names a `MailDomain` that does not exist |
| `DomainsAuthorized` | `False` | `PublicSuffix` | a referenced domain is a public suffix such as `co.uk` |
| `DomainsAuthorized` | `False` | `PartiallyAuthorized` | some, not all, domains are authorized; only those are rendered and the message names the blocked ones |
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
| `ClientIPPreserved` | `True` | `Preserved` | client addresses reach Postfix unchanged |
| `ClientIPPreserved` | `False` | `ClusterTrafficPolicy` | `externalTrafficPolicy: Cluster` on a LoadBalancer source-NATs clients, so the per-client rate limits count all senders together |
| `DedicatedSendingAddress` | `True` | `Dedicated` | outbound mail has its own address or goes through a relay |
| `DedicatedSendingAddress` | `False` | `NodeSourceAddress` | `outbound.direct` without `ipPoolClaimRef` sends from the node address (ADR 0002) |
| `NodeDrainable` | `True` | `Drainable` | the Postfix disruption budget allows evicting one pod |
| `NodeDrainable` | `False` | `BudgetBlocksDrain` | `postfixMinAvailable` keeps every Postfix replica, so a node drain waits |
| `DKIMKeysAvailable` | `True` | `KeysAvailable` | every served domain has all its active signing keys, and each key was read and parsed (also when no domain is served) |
| `DKIMKeysAvailable` | `False` | `KeysMissing` | a served domain has no active key yet, or a key Secret is missing, has no such key, or does not parse; the message lists each domain and key. Outbound mail of those domains is deferred (451 4.7.1), inbound mail is not affected, and the rollout does not wait. Looked up again every minute ([ADR 0011](../decisions/0011-dkim-keys-and-rotation.md), [ADR 0013](../decisions/0013-never-unsigned-guard.md)) |

`ClientIPPreserved`, `DedicatedSendingAddress`, `NodeDrainable` and
`DKIMKeysAvailable` are warnings: they never feed into `Ready`.

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
| `SPFReady` | `True` | `SPFGenerated` | an SPF record is generated from `spec.spf` |
| `SPFReady` | `False` | `NoSendingSources` | no source is known, so no SPF record is generated |
| `MTASTSReady` | `True` | `PolicyAvailable` | MTA-STS is enabled and the policy is in `status.mtaSts` |
| `MTASTSReady` | `False` | `NoMailServer` | MTA-STS is enabled and no `MailServer` provides an `mx` host |
| `ReportAuthorization` | `True` | `NotRequired` | every DMARC report address is in the organizational domain of the policy |
| `ReportAuthorization` | `False` | `ReceiverRecordRequired` | a report address is in another organizational domain; its owner has to publish the record named in the message |

`DNSPublished` exists only in `externalDNS` mode, `MTASTSReady` only
with `spec.mtaSts.enabled`, and `ReportAuthorization` only with
`spec.dmarc`. `SPFReady`, `MTASTSReady` and `ReportAuthorization` are
informational: they never feed into `Ready`.

## MailDirectory

No controller sets conditions on this resource yet. The API reserves
`Ready` (directory reachable, Phase 3), and the `Ready` printer column
stays empty until then. Its domains are authorized and reported on the
`MailServer` (`DomainsAuthorized`).
