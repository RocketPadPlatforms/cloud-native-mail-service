# Use an LDAP or PostgreSQL directory

:::note
API version `v1alpha1`. Decisions and the evidence behind them: [ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md).
:::

A `MailServer` that references a `MailDirectory` takes its recipients and
its logins from that directory. CNMS only reads the directory: the accounts
are managed outside of it. Postfix asks the directory whether a recipient
exists, Dovecot checks the password and finds the mailbox.

## What the directory has to hold

| Meaning | LDAP | PostgreSQL |
|---|---|---|
| login name | `attributes.login` (default `uid`) | column `login` of the users relation |
| primary address | `attributes.mail` (default `mail`, one value) | column `mail` |
| further addresses (optional) | `attributes.aliases` (many values) | columns `alias`, `target` of the aliases relation |
| password | checked by the directory (an authentication bind) | column `password`, a `{SCHEME}hash` value |
| switched-off accounts | `activeFilter`, for example `(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))` | leave them out of the relation (use a view) |

The column names of a PostgreSQL directory are fixed; an existing schema is
adapted with views. A read-only database role is enough.

The lookups compare `lower(mail)`, `lower(login)` and `lower(alias)`, which a
plain index does not serve. For a large relation add expression indexes, for
example `CREATE INDEX ON cnms_users (lower(mail));`, the same for
`lower(login)`, and `CREATE INDEX ON cnms_aliases (lower(alias));`.

An account is a login that is lower case, starts with a letter or digit,
and has no `/`. A login that starts with a dot, holds a slash or starts
with `sa.` is never an account, in either kind of directory: the login
becomes the name of the mailbox directory. A directory whose login
attribute cannot do substring matching therefore has no accounts, which the
`MailDirectory` shows as `Reachable=False` with reason `NoMatchingEntries`.

## Configure it

```yaml
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDirectory
metadata:
  name: corp-ldap
spec:
  type: ldap
  ldap:
    host: ldap.example.com
    baseDN: ou=people,dc=example,dc=com
    bindDN: cn=lookup,ou=services,dc=example,dc=com
    bindSecretRef: {name: ldap-bind}      # key "password"
    tls:
      mode: StartTLS                      # default, or LDAPS; None is refused (see below)
      caSecretRef: {name: ldap-ca}        # key "ca.crt"
    attributes: {login: uid, mail: mail, aliases: mailAlias}
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailServer
metadata:
  name: mail
spec:
  directoryRef: {name: corp-ldap}
  # ...
```

The `MailServer` controller reads the `MailDirectory` and its Secrets and
copies the password and the CA into Secrets of its own (one per revision).
Nothing secret is in a ConfigMap, and Postfix and Dovecot run from those
Secrets without the operator.

- A change of the `MailDirectory` or of a Secret it names is a new,
  validated revision like any other configuration change. The operator does
  not watch Secrets: a changed password is picked up at the next re-check of
  the server (at most ten minutes), or at once when you touch the
  `MailServer` or the `MailDirectory`.
- While the `MailDirectory` is missing or not valid (a Secret is missing, a
  password holds a newline, the filter does not compile) the server is
  `Ready=False` with reason `DirectoryNotFound` or `DirectoryInvalid`, and the
  revision that runs keeps running.
- Postfix and Dovecot refuse some settings the probe accepts, and the server
  is then `Ready=False` with reason `DirectoryInvalid` (the revision that
  runs keeps running): `tls.mode: None` for LDAP, because Dovecot checks a
  password by binding to the directory as the user and every IMAP and
  submission login would send its password in clear text (Postfix alone
  could look up without TLS, but a `MailServer` always runs Dovecot); `%{`
  in the base DN or the bind DN, which Dovecot would expand as a variable;
  and a base DN or bind DN with a blank at either end or a control
  character, which the engines trim or break at where the probe does not.
- A password must not start or end with a blank or hold a control
  character: Postfix reads it from a line of a file. Create the Secret with
  `kubectl create secret generic ... --from-file=password=<file>` where the
  file has no trailing newline (`printf %s`), or with `stringData`.
- The PostgreSQL password is read from a file by Dovecot and written into a
  Postfix table; TLS settings and the connect timeout reach `libpq`
  through `PGSSLMODE`, `PGSSLROOTCERT` and `PGCONNECT_TIMEOUT` in the
  environment of the Postfix container. A server
  with a PostgreSQL directory therefore shows the connection mode in the
  pod template.
- Without a `caSecretRef` the engines verify the directory against the system
  roots of their images, as the operator does when it probes; both the
  Postfix and the Dovecot image carry a root bundle.
- The hash that names the Secrets of a revision and enters the config hash is
  keyed (HMAC) with a random key in the Secret `<server>-hash-key`, so the
  pod template and the Secret names do not let anyone with read access to
  them confirm a guess of the bind password offline. The operator creates
  the key once, when a render holds secret content; a server without any
  needs no key. A restore from GitOps
  has no such Secret; the operator creates a new key, the names and hashes
  change once, and the pods roll once.

## What is looked up

- **Receiving.** Postfix asks for `mail=<recipient>` (the primary address)
  and, if there is an alias attribute or relation, for the alias; an alias is
  rewritten to the primary address, and only the primary address goes to
  Dovecot. An address that is not in the directory gets `550 5.1.1`. Aliases
  are only accepted in domains the server serves. With PostgreSQL an alias
  counts only when its target is an account of the users relation that
  passes the login guard: an alias of a removed or guarded login gets `550
  5.1.1` at once, as in LDAP, and not a bounce later.
  An alias whose target is an account in a domain the server does not serve
  is accepted too, and the mail is delivered outward: Postfix rewrites the
  address and treats it as a remote one. Keep the targets in served
  domains.
- **Logging in.** By login name or by primary address, in any case, never by
  an alias. The session runs as the login name, so both forms reach the same
  mailbox, `/srv/mail/<login>`.
- **Quoting.** The lookup value is quoted by the engine (`%s` for Postfix,
  `%{user}` for Dovecot): a recipient such as `*)(uid=*` finds nothing.

## When the directory is down

Nothing is rejected and nothing is lost; see [failure
modes](../concepts/failure-modes.md#user-directory-down). Postfix answers
`451 4.3.0` to the sender, who retries; SASL on port 587 answers `454
4.7.0`.

## Limits of this version

- The envelope sender of an authenticated user is not yet bound to the
  addresses the directory returns: an authenticated user can submit mail as
  any address of a served domain. The binding for every user source comes
  with `MailUser` ([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.4).
- A directory with an address that two accounts share (or an alias that is
  another account's address, or two values in `mail`) expands that address
  to both: keep the directory consistent. CNMS cannot see its content.
- A `MailServer` that sets both `directoryRef` and
  `submission.insecureStaticUsers` (a test feature) uses the static users.
- Only PostgreSQL is supported for `type: sql`.
