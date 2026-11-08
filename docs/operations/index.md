# Operations

Runbooks and operating procedures:

- One runbook per alert (alert rules land in Phase 9)
- Queue rescue
- Dovecot failover (RWO volume behaviour)
- [End the sessions of a revoked account](#end-the-sessions-of-a-revoked-account)
- [A server is blocked by a limit](#a-server-is-blocked-by-a-limit)
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

## A server is blocked by a limit

`kubectl get mailserver <server> -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'`
shows `Ready=False` with the reason `UserLimitExceeded`, `AddressLimitExceeded`
or `ConfigTooLarge`. The message has the counts, or the object that is too
large and its size. A Warning Event with the same reason is emitted when it
changes. The server is not broken: the revision that runs keeps serving, and a
change that adds something waits (see [limits](../how-to/users-and-aliases.md#limits)).

1. Read the message. With `UserLimitExceeded` the server has more than 1,000
   `MailUser`s; with `AddressLimitExceeded` more than 5,000 addresses (or an
   account with more than 32 aliases); with `ConfigTooLarge` a rendered object
   reached 1,000,000 bytes (the live Dovecot Secret is
   `<server>-dovecot-users`).
2. Remove accounts or aliases (or move accounts to another `MailServer`) until
   the numbers are under the limits. The next reconcile, within a few seconds of
   the change to a `MailUser` and otherwise within 30 seconds, applies the whole
   revision as usual: the held accounts and aliases appear, the validation Job
   runs and the pods roll.
3. While the block lasts, removing or disabling an account takes its login
   away, but not its address: mail for it is still accepted and delivered to the
   retained mailbox. Open IMAP sessions stay open (see above).

There is nothing to restart and nothing to delete: the state of the block is
the live Secret and the objects themselves, so a restart of the operator does
not change it. One thing the block does not do: it never creates the live
Secret `<server>-dovecot-users`. If that Secret is deleted while a limit
blocks, it stays missing until the block clears; a Dovecot pod that is
rescheduled meanwhile stays in `ContainerCreating` and mail waits in the
Postfix queue, so clear the block first (step 2).
