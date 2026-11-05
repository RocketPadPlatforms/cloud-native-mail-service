# Failure modes

The design rule for every component failure is the same: **mail must
never be lost because a component is down.** Outages produce
temporary failures (4xx responses and queueing), never permanent
rejections, and never lost messages. Mail of authenticated senders
and Postfix's own non-SMTP mail must never leave unsigned: it is
signed or deferred ([ADR 0013](../decisions/0013-never-unsigned-guard.md)).

This page describes the intended behaviour; the chaos e2e suite
(kill a Postfix pod, kill Rspamd, stop the operator, ...) proves it
from the hardening phase on.

## The short table

| Failure | Behaviour |
|---|---|
| Operator down | Mail keeps flowing; rollouts, rotation and status updates pause |
| Rspamd down (all replicas) | Mail tempfails into the Postfix queues (4xx to remote senders); nothing unsigned is sent |
| Rspamd up, but a domain has no usable DKIM key | Outbound mail of that domain is deferred (`451 4.7.1`, the sender keeps it); inbound mail and the other domains are not affected; `DKIMKeysAvailable=False` names the key |
| Valkey down | Rspamd degrades (rate limits and learned state unavailable); mail still flows and is still signed |
| User directory down | Unknown-recipient lookups tempfail (4xx, not 5xx); Dovecot authentication fails so submission is deferred by the sender |
| Postfix replica lost | Queued mail survives on the per-replica queue PVC; remaining replicas keep serving; the lost replica is replaced |
| Scale-down of Postfix | Not supported yet: the queue move is not implemented, so decreasing `postfix.replicas` is rejected |
| Dovecot down | IMAP/LMTP unavailable until the pod reschedules onto a node that can attach the mail volume; mail is queued in Postfix meanwhile |
| Hostname of the server not authorized (no grant, public suffix, used by another server) | The server keeps serving its domains but presents the in-cluster name of its Postfix Service as HELO and certificate name; no MX record or MTA-STS policy names it; `HostnameAuthorized=False` and `Ready=False` with the reason of the failure |
| Certificate renewed | Reload, not rollout — the cert Secret is excluded from the pod-template content hash |
| Bad configuration | The validation Job fails: `Ready=False, reason=ConfigRejected`; the running pod templates and their (immutable) configuration stay untouched |
| Rollout does not converge | `WorkloadsReady=False, reason=RolloutStalled` (Deployment past its progress deadline, or not ready for ten minutes); `Ready` stays false |

## Details

### Bad configuration

Every new rendered configuration is checked by a validation Job with
the engine's own tooling before any pod template changes
([ADR 0009](../decisions/0009-config-validation-gate.md)).
While the Jobs run, `ConfigValidated` is `Unknown`; if one fails,
`ConfigValidated` and `Ready` are `False` with reason
`ConfigRejected`, the message names the failed Job, and a Warning
Event is emitted. The existing workloads keep running the previous
configuration, which cannot change under them because the
ConfigMaps and Secrets are immutable. Reverting the spec clears the
state; deleting the failed Job retries the same configuration. The
validator's output is in the Job's pod log.

### Rspamd down

`milter_default_action = tempfail` with deliberate milter timeouts
(ADR 0006) means an unreachable or timing-out filter makes Postfix
queue the message and answer 4xx to remote senders, who retry —
the mail is not accepted unsigned and not rejected. Mail that
Postfix receives locally (`sendmail`, `postdrop`) is filtered through
`non_smtpd_milters`, so it is held the same way. Mail that Postfix
generates itself (bounces, notifications) skips the filter, because
`internal_mail_filter_classes` is not set (Postfix 3.11.7 cannot tell
that mail from `sendmail` mail, [ADR 0013](../decisions/0013-never-unsigned-guard.md)),
so bounces are unsigned.

### Rspamd up, but nothing to sign with

A reachable filter is not enough: Rspamd passes a message through
unsigned when its key is missing. A guard in the Rspamd configuration
therefore checks that every message of an authenticated sender, or of
Postfix itself, from a served domain carries the signature of every
active key of the domain. If one is missing, Rspamd answers soft reject,
Postfix replies `451 4.7.1` at the end of DATA, and the sending client
keeps the mail and retries. Unauthenticated mail (including mail from
other pods), mail of domains the server does not serve, and mail
that is rejected anyway are not touched. A message with several `From`
addresses, one of them in a served domain, can never be signed and is
rejected with `554 5.7.1`. A guard that fails with an error logs it and
defers the mail of authenticated senders and Postfix's own `sendmail(1)`
mail (loopback with a sender) too. The key Secrets are read through
a mounted volume, so a deleted Secret defers the domain's mail instead of
sending it unsigned or crashing the pod.

### Hostname not authorized

The hostname of a server is judged on its own, apart from its domains
([ADR 0014](../decisions/0014-domain-server-binding-and-hostname.md)).
When it is not granted to the namespace, is a public suffix or a
cluster-internal name (`cluster.local`), or is used
by another `MailServer` among the competing ones, the server does not
fail closed: it presents the name of its in-cluster Postfix Service (the
HELO name and the only name on the certificate, which is not something a
public CA would sign) and keeps serving the authorized domains. Mail to
and from those domains keeps flowing; what suffers is the identity: remote
servers see an HELO name without records, and TLS clients that verify the
hostname fail. No MX record or MTA-STS policy points at the fallback name,
and `Ready` stays `False` until the grant or the duplicate is fixed. A
hostname that is **revoked or renamed** does not fail closed for the
domains either, also while the server is blocked for another reason
(directory or users Secret missing): the certificate loses the old name
at once, and the running pods keep serving their domains, still
presenting the old name, until the new configuration is validated and
rolled out. Only a revoked domain gets the fail-closed configuration.
The accepted exposure is that window: the old name stays in HELO (it is
self-asserted) and, until the issuer replaces the certificate Secret,
in the certificate the pods mount. With a public ACME issuer the
fallback name cannot be issued, so the old Secret stays mounted until
it expires.

### Valkey down

Rspamd's state (rate limits, reputation, Bayes) lives in the
per-server Valkey. Without it, the affected modules degrade
according to their configured behaviour; the DKIM private keys are
served from mounted Secrets, not from Valkey, so signing does not
depend on the key-value store.

### User directory down

Recipient validation and authentication query the external
directory (LDAP/SQL). A lookup failure produces a tempfail:
Postfix defers instead of rejecting unknown recipients, and
Dovecot refuses to guess — the sending MTA retries later. This is
the directory phase's e2e contract: valid and invalid recipients
behave correctly, and a stopped directory shows as tempfail, not
550.

### Postfix replicas

Each replica owns its queue on a PVC; updates are ordered
one-at-a-time, gated on readiness, behind a PodDisruptionBudget.
Scale-up works. Scale-down does not yet: moving a replica's queue to
the remaining replicas before its PVC is released is not
implemented, so the API rejects decreasing `spec.postfix.replicas`
instead of stranding queued mail
([ADR 0010](../decisions/0010-storage-immutability-and-scale-down.md)).
The queue move and the queue-rescue job (re-attaching an orphaned
PVC with live mail to flush it) are planned for the hardening phase.

The queue and mail volumes cannot be resized through the
`MailServer`: `postfix.queueStorage` and `mailboxes.storage` are
immutable because a StatefulSet cannot change its volume claim
templates. Grow the existing PersistentVolumeClaims directly (the
StorageClass must allow expansion); Kubernetes never shrinks a
volume. An operator-driven resize is in the backlog. Valkey's volume
is a plain PVC: a larger `valkey.storage` is applied in place.

### Dovecot

Dovecot runs a single replica on a read-write-once volume (active/
standby via rescheduling). After a node failure the volume can stay
attached to the old node for minutes unless the node is
tainted or removed — the realistic failover time, and how to speed
it up, will be in the [operations](../operations/index.md) runbook (not
written yet).

## Observing failures

Status conditions on the custom resources, Kubernetes Events and the
per-component metrics (ADR 0005) all reflect these states, so the
behaviour above is observable without any monitoring stack:
`kubectl get` on the resources shows conditions such as
`Ready`, and the queue metrics show growth while a dependency is
down.
