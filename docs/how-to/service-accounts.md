# Let an application send mail with a MailServiceAccount

:::note
API version `v1alpha1`. Decisions: [ADR
0008](../decisions/0008-user-sources-and-service-accounts.md) and [ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md), section
4.9.
:::

A `MailServiceAccount` is the sending credential of an application. It lives in
the namespace of the application and may name a `MailServer` of another
namespace. The operator makes a Secret in the namespace of the application with
everything the application needs to connect, and binds the credential to one
sender address.

## The server consents

A server accepts accounts of its own namespace. Accounts of other namespaces
need the consent of whoever may edit the `MailServer`: the namespace is listed in
`spec.serviceAccounts.allowedNamespaces`. A tenant cannot grant itself access to
another tenant's server.

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailServer
metadata:
  name: mail
spec:
  # ...
  serviceAccounts:
    allowedNamespaces:
      - name: shop                   # may use any domain the server serves
      - name: newsletter
        senderDomains: [news.example.org]   # only these domains
```

- Without `senderDomains` a namespace may send from every domain the server
  serves. `senderDomains` lists domain names (the `spec.domain` of the
  `MailDomain`s of the server's namespace); an empty list is refused at
  admission, so that nobody writes it to mean "none" and gets "all". To allow
  nothing, leave the namespace out.
- Removing a namespace revokes its accounts in the next reconcile: the
  credential Secret and the NetworkPolicy are deleted. A namespace that is not
  listed gets `Ready=False`, reason `NamespaceNotAllowed`, and nothing is made.
  An account that names a server of **another** namespace that does not exist
  gets the same answer, so a namespace cannot find out whether a server exists
  in another one before it is consented to.
- The namespace of the server needs no entry. If you do list it, the entry
  applies to it like to any other namespace: its `senderDomains` limit the
  accounts of that namespace too.
- **Give every namespace its own `senderDomains`.** An address that nobody owns,
  such as `ceo@a.example`, goes to the one account that claims it, and when two
  accounts claim the same address **both lose it**. Two consented namespaces
  that share a domain can therefore deny each other an address by naming it
  (a cross-tenant denial of service), and a namespace can take an unowned name
  in any domain it is allowed. Distinct `senderDomains` close this at the
  domain level. The local part inside a granted domain is not restricted yet
  (a grant for local parts is in the [backlog](../backlog.md)).

## The account

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailServiceAccount
metadata:
  name: notifications            # at most 63 characters
  namespace: shop
spec:
  serverRef:
    name: mail
    namespace: mail-system       # optional; the namespace of the account by default
  sender: notifications@example.org
```

`sender` is the one address the application may send as. It is lower case,
the local part has up to 64 ASCII letters, digits and `.` `_` `-` (no `+`, no
`..`), and its domain must be a domain the server serves and authorizes for its
namespace. The status says what is wrong otherwise, see the [status
conditions](../reference/status-conditions.md#mailserviceaccount).

An address has one owner. A `MailUser` of the server with that primary address or
alias keeps it (`SenderConflict`); so does an account of the directory of the
server. Two accounts that name the same sender both lose it.

## What the application mounts

The Secret `<name>-credential` in the namespace of the account:

| Key | Value |
|---|---|
| `username` | The login, `sa.` and 16 hexadecimal characters derived from the namespace and the name of the account |
| `password` | A generated password (32 random bytes, base64url) |
| `server` | The in-cluster name of the submission Service (`<server>-postfix.<namespace>.svc.cluster.local`); the port is 587 |
| `tls` | `mandatory`: connect with STARTTLS, the server takes AUTH only over TLS |
| `helo` | The name to present in the HELO: the hostname of the server |
| `tlsServerName` | The name the certificate of the server is for; verify against it, not against `server` |
| `ca.crt` | The CA chain of the certificate of the server, when its Secret has one (cert-manager issuers with a CA); left out otherwise, and the status says so |
| `passwordHash` | Kept by the operator. Ignore it and do not edit it |

The Secret is owned by the account and made once; the generated password is
never replaced while it matches its hash. A hash that does not belong to the
password (an edit of one of the two) is replaced, with a new password,
together with it. A password and a hash that do belong together are kept,
whoever wrote them: a tenant who can edit Secrets in its namespace can choose
the password of the login of **its own account**, and of no other, because the
login comes from the account object and the `username` key is written again at
every reconcile. Keep the right to edit these Secrets with the people who run
the application. If you delete the Secret, the operator makes it again with a
**new password**; the account has no `passwordSecretRef`, so there is nothing
to bring from GitOps. After a restore from GitOps, which holds no Secrets, the
application gets a new password: mount the Secret rather than copy it, so the
application sees the new one.

## What the server does with the account

The `MailServer` serves an account that it accepts: the server consents to its
namespace and the domain of its sender, and the account controller admitted
the sender (`status.admittedSender` and `status.admittedFor` match). Whether it
is served is decided again by the server in every pass, with every other claimant
of its addresses (the `MailUser`s, the static test users and the other accounts),
not taken from the status of the account: a conflict, a revoked domain or a
withdrawn consent stops the account in the next pass of the server, whatever the
status still says. The account is then:

- a **login** in the passwd-file of the live Dovecot Secret (`<server>-dovecot-users`),
  by the name alone (`sa.` and 16 hexadecimal characters), with the hash of the
  credential Secret. The login is derived from the account, never read from the
  Secret, and the hash is taken only from a Secret the account controls and only
  in the form the operator writes. There is no line for it in the userdb file,
  so nothing is delivered to it;
- the **owner of its sender** in the sender table of Postfix
  (`smtpd_sender_login_maps`): it may send as `spec.sender` and as no other
  address, and not with the null sender;
- the owner of its sender in the table of the owned header `From` addresses of
  Rspamd (check 2 of the guard): the header `From` of its mail must be its sender
  too. On a server **with a directory** check 2 is off for every login, the
  service accounts included, because the addresses of the directory are not in
  the cluster; the residual risk is the one of the directory users (a login can put
  any served address in the header `From`, and it is signed);
- no recipient: it has no mailbox and no alias, and mail for its sender is
  `550 5.1.1` unless somebody else owns the address.

A server whose only accounts are service accounts (no directory, no `MailUser`
and no static test users) is a **send-only server**: it is `Ready` and not
`NoUserSource`. With a directory the passwd-file is asked first and a login that is
not in it falls through to the directory, and the sender table is the union of
the table of the service accounts and the directory's: a lookup error in either
one is a temporary failure (`451`), so a directory that is down also stops the
submission of service accounts.

**The senders count as addresses.** At most 5,000 addresses (primary addresses,
aliases and senders of service accounts) belong to one server, and the senders of
**every consented namespace** count, also those that lose a conflict. A
namespace that the server consents to can therefore fill the limit and block the
new revisions of the server (`AddressLimitExceeded`; the running revision keeps
serving, and a login that is removed or changed still takes effect). Consent only
to namespaces you trust with that, and see [failure modes](../concepts/failure-modes.md).

**Revocation takes time.** A withdrawn consent, a deleted account or a change
of the spec of an account reaches the server in a few seconds (a change of an
account is debounced for three seconds), and the login leaves the live Dovecot
Secret in the same pass; the pod sees the change after the kubelet has projected
the Secret, typically a minute or two, like for the users. The Postfix and
Rspamd tables follow with a validated revision. A change of the credential
Secret itself (an edit by the tenant, a deleted Secret) is not watched by either
controller: it takes effect at the next pass of the server or at the ten-minute
requeue of the account, whichever comes first. A submission session that is
open when the account is revoked stays authenticated until it closes; the
password is checked when it starts.

## The egress policy

The operator makes a NetworkPolicy `<name>-submission` in the namespace of the
account. It selects the pods of the application that carry the label
`cnms.rpp.gmbh/mailserviceaccount: <name>` and allows them to reach the
Postfix pods of the server on port 587, and DNS (the cluster DNS of
`kube-system`, and port 53 elsewhere, like the policies of the server).

Label the pods of the application that send mail, and only those. A
NetworkPolicy isolates every pod it selects: **a labelled pod can reach nothing
else** unless another policy of the namespace allows it, so give such pods a
policy for the rest of their traffic. The policy does not select pods without
the label, so an application namespace without any policy is not cut off by it.
That the policy works with your CNI is not verified here; it is a plain
`networking.k8s.io/v1` egress policy.

## Directory servers

A server with a `directoryRef` is checked once per account, sender, server and
directory: the operator asks the directory whether the sender is already an
address of one of its accounts, and records what it checked against in
`status.admittedSender` and `status.admittedFor`. A hit is `SenderConflict`: a
new account gets no credential, and after a change of the sender or of the
server the old Secret stays but the server does not serve the account. If the
directory cannot be asked, a **new** account, a **new** sender, an account
that names **another server**, or a server that **gained or changed its
directory** is not admitted (`DirectoryUnavailable`, asked again after about a
minute); an account that was admitted for this sender, server and directory
keeps its credential and is not asked again, so a directory that is down never
takes a working account away. The lookup uses the same restriction and guards
as the lookups of Postfix. The lookup against a real PostgreSQL directory is
covered by the e2e suite (ADR 0015, row 47); against LDAP it is not verified
(the specs use a fake).
