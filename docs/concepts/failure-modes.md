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
| User directory down | Recipient lookups tempfail (`451 4.3.0`, not 5xx), also for known recipients; Dovecot cannot authenticate (`454 4.7.0` with LDAP; `535 5.7.8` with PostgreSQL, see the backlog) so submission fails for the client, and LMTP delivery waits in the Postfix queue |
| Postfix replica lost | Queued mail survives on the per-replica queue PVC; remaining replicas keep serving; the lost replica is replaced |
| Scale-down of Postfix | Not supported yet: the queue move is not implemented, so decreasing `postfix.replicas` is rejected |
| Dovecot down | IMAP/LMTP unavailable until the pod reschedules onto a node that can attach the mail volume; mail is queued in Postfix meanwhile |
| Account changed, disabled or removed (`MailUser`) | The Dovecot files are updated in place and reach the pod after the kubelet has projected the Secret: typically a minute or two, longer under load. Until then the old password or login still works; open IMAP sessions stay open (see [users and aliases](../how-to/users-and-aliases.md); `doveadm kick` ended one in a plain container, not verified through `kubectl exec` in a pod). Postfix follows with a validated rollout; mail it accepts for a user that Dovecot does not know yet is deferred, not bounced |
| Operator down, with MailUsers | The live Secret and the Postfix maps stay as they are: logins and mail keep working. New accounts, changed passwords, and disabled or deleted accounts take effect only once the operator runs again; until then a revoked account can still log in |
| Server blocked for another reason (a render error, `UserSourceConflict`, `DirectoryInvalid`, `ClusterIssuerNotAllowed`, relay or IP pool claim) | The running revision and the live users Secret stay as they are, so an account that is disabled or deleted meanwhile keeps its login until the block clears. The revocation-only rule of ADR 0015, section 4.7 (logins are never blocked) applies to the limit blocks only (next row); extending it to these blocks is in the [backlog](../backlog.md) |
| Server over a limit (more than 1,000 accounts, 5,000 addresses, or an object of 1,000,000 bytes or more: `UserLimitExceeded`, `AddressLimitExceeded`, `ConfigTooLarge`) | A blocked revision: the new revision is not applied as a whole, Postfix and Rspamd keep the running data, `Ready=False` names the counts or the object. Removing or disabling an account and a new password still apply to the live Dovecot files, new accounts and aliases wait; a removed account keeps its mailbox and is still a recipient until the block clears. It clears by itself once the numbers are under the limits (see [limits](../how-to/users-and-aliases.md#limits) and the [runbook](../operations/index.md#a-server-is-blocked-by-a-limit)) |
| Credential Secret of a `MailUser` deleted | The account has no login (it is left out of the passdb) and still receives mail. A generated password is replaced by a new one on the next reconcile of the `MailUser`; a provided one is hashed again |
| Service account withdrawn (consent removed, account deleted, sender revoked or lost to a conflict) | The login leaves the live Dovecot Secret in the next pass of the server (a few seconds, debounced) and reaches the pod after the kubelet projection, a minute or two like a user; the sender leaves the Postfix and Rspamd tables with the next validated revision. A submission session that is open stays authenticated until it closes. The credential Secret and the NetworkPolicy of a revoked account are deleted by its controller ([service accounts](../how-to/service-accounts.md)) |
| A consented namespace fills the address limit with the senders of its service accounts | The senders of every consented namespace count toward the 5,000 addresses of a server, also those that lose a conflict: the server is `AddressLimitExceeded`, no new revision is applied (the running one keeps serving, and a login that is removed or changed still takes effect), and the message says the count, not whose accounts. Accepted risk of consenting to a namespace; remove the namespace from `allowedNamespaces` to clear it |
| Brute force on the logins of the service accounts of a server with a directory | The four auth workers of Dovecot are shared (ADR 0015, section 3, point 4) and the passwd-file passdb of the service accounts is asked **first** for every login: each attempt with an `sa.` name costs an Argon2id verification, and a flood of them queues the step that every directory login passes before it reaches the directory. The workers bound the memory, not the latency; the rate limits of Postfix on the submission port are the only brake, and failed attempts are not banned |
| Credential Secret of a service account deleted or edited | The account has no login until its controller has made the Secret again (a new password; at most its ten-minute requeue, or at once for a change of the account); a hash that is not the operator's form, or a Secret the account does not control, gives no login. The server never reads the login from the Secret |
| Directory of a server with service accounts down | The sender table is the union of the service accounts' table and the directory's, and a lookup error in either member is a temporary failure: submission of service accounts stops with `451` while the directory is down; the logins themselves come from the file and do not need it. An account that was admitted is not asked again, a new one waits (`DirectoryUnavailable`) |
| Live users Secret `<server>-dovecot-users` deleted or edited | Secrets are not watched, so the next reconcile of the server writes it again: at once for any change of an account or of the server, otherwise within ten minutes (30 s while the server is not ready). Not while a limit blocks the server: then a missing Secret stays missing until the block clears (a blocked revision only revokes, it never creates the Secret). A Dovecot pod that is rescheduled meanwhile stays in `ContainerCreating` until the Secret is there, and mail waits in the Postfix queue. That a running pod keeps the files it last got is expected of the kubelet and not verified |
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
`internal_mail_filter_classes` is not set (Postfix 3.11 cannot tell
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
other pods) and mail that is rejected anyway are not touched. A message
of an authenticated sender whose `From` header is missing, has no domain
or is not in a served domain, and a message with several `From`
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
directory (LDAP/PostgreSQL, [how-to](../how-to/directories.md)). A lookup
failure produces a tempfail and never a rejection; only a missing entry
gives `550 5.1.1`. Verified against Postfix 3.11.7 and Dovecot 2.4.5
([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
Verification):

- **Receiving.** A directory that is down, refuses the bind or fails the TLS
  verification (a wrong CA) gives `451 4.3.0 Temporary lookup failure` at
  `RCPT`, for known and unknown recipients alike; the sending server
  retries. A directory that refuses connections is answered at once; one
  whose packets are dropped costs the lookup timeout (5 s): the `RCPT` was
  answered after about 5 s with LDAP and about 10 s with PostgreSQL (the
  `pgsql` client connects twice, and the operator sets `PGCONNECT_TIMEOUT`
  to the same 5 s; without it the `RCPT` was not answered within 300 s).
  Dovecot has no such bound for PostgreSQL: a login against dropped packets
  takes about two minutes. The `pgsql` client of Postfix does not try to
  connect again for 60 seconds after a failure, so a returning PostgreSQL is
  used after at most a minute.
- **Submission.** Dovecot cannot check the password: the client gets a
  temporary failure (`454 4.7.0` on port 587) with LDAP and keeps the mail. With
  PostgreSQL that refuses connections the reply was `535 5.7.8` in the e2e
  suite, which the client may take for a wrong password.
- **Delivery from Postfix.** Dovecot cannot look the recipient up: LMTP
  answers `451 4.3.0`, after up to about two and a half minutes with LDAP
  that cannot be resolved; Postfix keeps the mail in its queue.
- **Control plane.** The operator being down or the `MailDirectory` being
  unreachable from the operator pod does not change this: `Reachable` is a
  warning and never feeds into `Ready`. A `MailDirectory` that is deleted
  or not valid blocks new revisions (`DirectoryNotFound`,
  `DirectoryInvalid`), and the running one keeps running.

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
standby via rescheduling). When its node fails, **the pod is not replaced
automatically**: Kubernetes never replaces a StatefulSet pod on an
unreachable node by itself, and the volume stays attached to the old node.
You release the pod (taint the node `node.kubernetes.io/out-of-service`,
delete the Node object, or force-delete the pod) after you have confirmed
that the node is down. Where something deletes the Node objects of failed
nodes, the pod is replaced after about 11 minutes, a figure derived from
Kubernetes defaults and not verified on a cluster. The steps are in the
[Dovecot failover](../operations/index.md#dovecot-failover) runbook. IMAP is
down meanwhile; Postfix queues the mail and nothing is lost.

## Observing failures

Status conditions on the custom resources, Kubernetes Events and the
per-component metrics (ADR 0005) all reflect these states, so the
behaviour above is observable without any monitoring stack:
`kubectl get` on the resources shows conditions such as
`Ready`, and the queue metrics show growth while a dependency is
down.
