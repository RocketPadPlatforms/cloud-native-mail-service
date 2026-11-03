# Failure modes

The design rule for every component failure is the same: **mail must
never be lost because a component is down.** Outages produce
temporary failures (4xx responses and queueing), never permanent
rejections, and never lost messages. Outgoing mail must never leave
unsigned once DKIM signing exists.

:::danger
**DKIM signing is not implemented yet** (Phase 5): outbound mail
currently leaves unsigned. The "nothing unsigned is sent" statements
below describe the target behaviour and do not hold until then.
:::

This page describes the intended behaviour; the chaos e2e suite
(kill a Postfix pod, kill Rspamd, stop the operator, ...) proves it
from the hardening phase on.

## The short table

| Failure | Behaviour |
|---|---|
| Operator down | Mail keeps flowing; rollouts, rotation and status updates pause |
| Rspamd down (all replicas) | Mail tempfails into the Postfix queues (4xx to remote senders); nothing unsigned is sent (once DKIM signing exists) |
| Valkey down | Rspamd degrades (rate limits and learned state unavailable); mail still flows (and is signed once DKIM signing exists) |
| User directory down | Unknown-recipient lookups tempfail (4xx, not 5xx); Dovecot authentication fails so submission is deferred by the sender |
| Postfix replica lost | Queued mail survives on the per-replica queue PVC; remaining replicas keep serving; the lost replica is replaced |
| Scale-down of Postfix | Not supported yet: the queue move is not implemented, so decreasing `postfix.replicas` is rejected |
| Dovecot down | IMAP/LMTP unavailable until the pod reschedules onto a node that can attach the mail volume; mail is queued in Postfix meanwhile |
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
`internal_mail_filter_classes` is not set yet (backlog). Once DKIM signing exists (Phase 5), a missing signature
must also result in a tempfail for authenticated senders, so an
up-but-broken filter cannot release unsigned mail; that guard is not
implemented yet.

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
