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

`ClientIPPreserved`, `DedicatedSendingAddress` and `NodeDrainable` are
warnings: they never feed into `Ready`.

## MailDomainGrant

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `NamespaceExists` | `True` | `NamespaceExists` | the namespace in `spec.namespace` exists |
| `NamespaceExists` | `False` | `NamespaceNotFound` | the namespace does not exist; a Warning Event is emitted |

The condition is informational: it never feeds back into authorization.

## MailDomain and MailDirectory

No controller sets conditions on these resources yet. The API
reserves `Ready` on both (`MailDomain`: records published and verified,
Phase 5; `MailDirectory`: directory reachable, Phase 3), and the
`Ready` printer column stays empty until then. Their domains are
authorized and reported on the `MailServer` (`DomainsAuthorized`).
