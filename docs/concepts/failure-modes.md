# Failure modes

The design rule for every component failure is the same: **mail must
never be lost because a component is down.** Outages produce
temporary failures (4xx responses and queueing), never permanent
rejections, never lost messages, and never unsigned outgoing mail.

This page describes the intended behaviour; the chaos e2e suite
(kill a Postfix pod, kill Rspamd, stop the operator, ...) proves it
from the hardening phase on.

## The short table

| Failure | Behaviour |
|---|---|
| Operator down | Mail keeps flowing; rollouts, rotation and status updates pause |
| Rspamd down (all replicas) | Mail tempfails into the Postfix queues (4xx to remote senders); nothing unsigned is sent |
| Valkey down | Rspamd degrades (rate limits and learned state unavailable); mail still flows and is signed |
| User directory down | Unknown-recipient lookups tempfail (4xx, not 5xx); Dovecot authentication fails so submission is deferred by the sender |
| Postfix replica lost | Queued mail survives on the per-replica queue PVC; remaining replicas keep serving; the lost replica is replaced |
| Scale-down of Postfix | Operator-orchestrated: the replica's queue is moved to the remaining replicas before its PVC is released |
| Dovecot down | IMAP/LMTP unavailable until the pod reschedules onto a node that can attach the mail volume; mail is queued in Postfix meanwhile |
| Certificate renewed | Reload, not rollout — the cert Secret is excluded from the pod-template content hash |

## Details

### Rspamd down

`milter_default_action = tempfail` with deliberate milter timeouts
(ADR 0006) means an unreachable or timing-out filter makes Postfix
queue the message and answer 4xx to remote senders, who retry —
the mail is not accepted unsigned and not rejected. Locally
generated mail (for example bounces) is filtered through
`non_smtpd_milters`, so it is held the same way instead of skipping
the filter. While Rspamd is up, a missing DKIM key must also result
in a tempfail for authenticated senders, so an up-but-broken filter
cannot release unsigned mail.

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
On scale-down the operator moves a replica's queue to the remaining
replicas before its PVC can be released; an orphaned PVC with live
mail is never deleted automatically and can be re-attached and
flushed by the queue-rescue job (disaster recovery).

### Dovecot

Dovecot runs a single replica on a read-write-once volume (active/
standby via rescheduling). After a node failure the volume can stay
attached to the old node for minutes unless the node is
tainted or removed — the realistic failover time, and how to speed
it up, is in the operations runbook.

## Observing failures

Status conditions on the custom resources, Kubernetes Events and the
per-component metrics (ADR 0005) all reflect these states, so the
behaviour above is observable without any monitoring stack:
`kubectl get` on the resources shows conditions such as
`Ready`, and the queue metrics show growth while a dependency is
down.
