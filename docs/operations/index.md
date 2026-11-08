# Operations

Runbooks and operating procedures:

- One runbook per alert (alert rules land in Phase 9)
- Queue rescue
- Dovecot failover (RWO volume behaviour)
- [End the sessions of a revoked account](#end-the-sessions-of-a-revoked-account)
- Restore drill (mailboxes, DKIM keys)

Added as the corresponding features land. See the [roadmap](../roadmap.md).

## End the sessions of a revoked account

Disabling or deleting a `MailUser`, or changing its password, removes the
login from the live Dovecot files once the kubelet has projected the Secret
(a minute or two, see [users and aliases](../how-to/users-and-aliases.md)).
Dovecot checks the password at login only, so **a session that is open stays
open**, and an IDLE session can last for days. After the revocation:

```sh
kubectl exec -n <ns> <server>-dovecot-0 -c dovecot -- doveadm kick <account name>
```

with the account name (the object name of the `MailUser`). `doveadm` reaches the
master in the pod (checked on kind); that `kick` ends an open IMAP session is
**not verified**. If the session stays open, restart the Dovecot pod, which ends
every session and interrupts IMAP, LMTP and submission login for a moment
(mail waits in the Postfix queue):

```sh
kubectl rollout restart statefulset/<server>-dovecot -n <ns>
```
