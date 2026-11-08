# Use MailUsers and aliases

:::note
API version `v1alpha1`. Decisions and the evidence behind them: [ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md).
:::

A `MailUser` is an account that the operator manages itself, for an
environment without a directory service. It has one mailbox and any number
of addresses, in different domains of the same server: it receives on all of
them into the one mailbox, logs in as itself, and may send as each of them.
A server has **one** user source: `MailUser`s, a [directory](directories.md),
or the static test users. A `MailUser` on a server that has another source is
`Ready=False` (`DirectoryConfigured`, `UserSourceConflict`) and the server
keeps the revision that runs until you remove one of the sources.

## An account with aliases

The domains are `MailDomain` objects of the same namespace that name the same
server in `spec.serverRef` and are authorized (a grant covers them).

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailUser
metadata:
  name: alice                  # the login, and the local part of the primary address
spec:
  serverRef: {name: mail}
  domainRef: {name: a-example} # primary address: alice@<spec.domain of a-example>
  aliases:                     # at most 32
    - {localPart: alice, domainRef: {name: b-example}}
    - {localPart: sales, domainRef: {name: b-example}}
  passwordSecretRef: {name: alice-password}   # optional, key "password"
```

- The **name** is the login and the local part of the primary address; it is
  unique in the namespace, so two accounts never share a primary address.
- A user **logs in** with the name (`alice`) or the primary address
  (`alice@a.example`), in any case. An alias is **not** a login.
- Mail to any of the addresses is delivered to the mailbox of the account.
- An alias that another account claims, or that names another account's
  primary address, is not served: the primary address always wins, and two
  aliases of the same kind in a conflict are both lost (mail to them is
  deferred with `450 4.2.1` until you fix it, not refused). The reasons are in
  `status.addresses[]` of the account (`AddressConflict`,
  `AliasDomainNotFound`, `AliasDomainNotServed`, `AliasDomainNotAuthorized`)
  and in the `AddressesServed` warning of the account and of the server.
- `postmaster@` is not mapped automatically; mail to it is `550 5.1.1` unless
  an account claims it.

## Passwords

Without `passwordSecretRef` the operator generates a password (32 random
bytes) and keeps it in the Secret `<name>-credential`, key `password`, next to
the hash (`passwordHash`) and the `username`. With `passwordSecretRef` the
password is the value of the key `password` of that Secret (at least 12
characters, used byte for byte: `kubectl create secret generic
--from-file` keeps the trailing newline, so use `--from-literal`). A provided
password is read again every minute and only its hash is stored.

**Use `passwordSecretRef` in production.** Generated passwords exist only in
the cluster, and k8up does not back up Secrets: after a restore from GitOps
every user with a generated password has a new one. A provided password
comes back with your Secrets (SOPS, sealed secrets, an external store).

## What happens when an account changes

| Change | Postfix (recipients, aliases, senders) | Dovecot (logins, mailboxes) |
|---|---|---|
| user added or removed, alias added or removed, domain revoked | a new revision behind the validation Job and a rolling update, minutes | the live files change without a revision or restart, after the kubelet projection (a minute or two) |
| password changed | nothing | the live files change without a restart, after the kubelet projection |
| `disabled` | the user may not send; still a recipient | the login goes, the mailbox is still found, mail is still delivered |

The Dovecot files (`passdb`, `userdb`) are two keys of one Secret,
`<server>-dovecot-users`, which the operator updates in place. It is not part
of the config hash: a changed account is no revision, no Job and no pod
restart. The pod mounts it as a directory, and the **kubelet** projects a
changed Secret into the pod after its next sync, so a change reaches Dovecot
after the projection delay (the sync period plus the cache delay, typically a
minute or two, longer under load). Changes reach the operator in a few
seconds: a changed object after a short delay (three seconds) that merges a
burst of changes into one reconcile; a changed provided password after the
polling interval of the `MailUser` controller, one minute with up to 20 %
jitter, so about 72 s at worst (the `MailUser` then writes
`status.credentialRevision`, which wakes the server).

That delay is the **revocation latency**: a disabled or deleted account, or a
changed password, still works until the new file is projected and read. The
worst case on an idle node is therefore about 72 s of polling, 3 s of debounce
and 60 to 90 s of kubelet sync, roughly three minutes. On one kind
node (three runs, ADR row 41; the accounts had a `passwordSecretRef`, none had
a generated password) an added account logged in after 56 s to 1 min 21 s and
a removed one was refused after 1 min 6 s to 1 min 22 s (the object event
path), and a changed password worked after 1 min 3 s to 1 min 28 s (the
polling path). A loaded cluster is slower, and these numbers are no
bound.

If Postfix learns about a new user before Dovecot, mail for it is accepted and
the delivery to Dovecot is **deferred**, not bounced (`soft_bounce` on the
`lmtp` service), until Dovecot knows the user. The same holds for mail that
Postfix still accepts for a user that was just removed. Anything else that
makes the delivery fail permanently stays in the queue the same way, until
the queue lifetime ends.

### Open IMAP sessions

Dovecot checks the password at login only. **A session that is open when an
account is disabled, deleted or its password changed stays open**, and an IDLE
session can last for days. Try to end the sessions of an account after
revoking it:

```sh
kubectl exec -n <ns> <server>-dovecot-0 -c dovecot -- doveadm kick <account name>
```

`doveadm` reaches the master in the pod (checked on kind); that `kick` ends an
open IMAP session is **not verified**. If the session stays open, restart the
Dovecot pod, which ends every session: `kubectl rollout restart
statefulset/<server>-dovecot`. The operator cannot run `doveadm` itself (it has
no `pods/exec`); a sidecar that kicks revoked accounts is in the backlog.

## Sending

An authenticated account may send only as an address it owns, in a domain the
server serves:

- the **envelope sender** (`MAIL FROM`) must be one of its addresses
  (`553 5.7.1` otherwise, in the reply to the first `RCPT TO`, where Postfix
  judges the sender: another account's address, an address nobody owns, a
  foreign domain). This holds for every user source, directory users
  included;
- the **null sender** (`MAIL FROM:<>`) is refused (`554 5.7.1`). Delivery
  status notifications and message disposition notifications (RFC 8098) must
  not be submitted on port 587; whatever generates them (later: a Sieve
  vacation action) has to reach Postfix on another path;
- the header `From` must be an address in a domain the server serves (the
  guard of Rspamd). A check that it is **owned by the login** is not there
  yet: an account can put another served address in the header `From`, and
  the message is signed for that domain.

Each alias domain signs with the key of its own domain: sending as
`alice@b.example` while `b.example` has no usable key is deferred
(`451 4.7.1`; `DKIMKeysAvailable` names the key).

## Suspended and disabled accounts

- A **disabled** account (`spec.disabled: true`) has no login and no sender
  address; mail is still delivered, and its `Ready` is `True` with the reason
  `Disabled`.
- An account whose **primary domain** is not served or not authorized is
  **suspended**: no login, no sending, no new delivery. Its aliases in other
  domains are removed from the alias map and deferred (`450 4.2.1`), because
  Postfix would otherwise rewrite them to an address in a domain that it
  treats as remote. `Ready` is `False` with `DomainNotFound`,
  `DomainNotServed` or `DomainNotAuthorized`.
- A domain that loses its grant while the server runs makes the server render
  fail closed (no domain, no account), as for any revoked domain, and then
  the valid revision without it.
- A **deleted** account loses its login and its mail lines in the same pass as
  above. Its mailbox stays on the volume (`/srv/mail/<name>`); a new account of
  the same name gets it.
- An account whose credential Secret is missing has no login; it still
  receives mail.

## When the operator is down

Mail and logins keep working: the data plane runs from the rendered objects and
the live Secret. New accounts, changed passwords, and disabled or deleted
accounts take effect only once the operator runs again; until then a revoked
account can still log in.

## Restore

Custom resources come from GitOps. The credential Secrets of generated
passwords do not (see Passwords). The live Secret and the Postfix maps are
rendered again from the objects.
