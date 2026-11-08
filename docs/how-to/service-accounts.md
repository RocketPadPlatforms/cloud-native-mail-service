# Let an application send mail with a MailServiceAccount

:::note
API version `v1alpha1`. Decisions: [ADR
0008](../decisions/0008-user-sources-and-service-accounts.md) and [ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md), section
4.9.
:::

:::caution
A `MailServiceAccount` has **no effect on mail yet**. The controller makes the
credential Secret and the NetworkPolicy and reports the status, but the
`MailServer` does not serve the account: the login is not in its Dovecot files
and the sender is not in its sender maps, so a connection with the credential
is refused. This page describes what exists now.
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

An application that cannot use the credential before the server serves the
account will see the connection refused; the `Ready` condition does not say
whether the server serves it yet.

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
as the lookups of Postfix. That the directory lookup works against a real LDAP
or PostgreSQL server is not verified yet (the specs use a fake).
