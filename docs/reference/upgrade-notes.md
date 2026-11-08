# Upgrade notes

:::note
API version `v1alpha1`. No CNMS release exists yet, and the API may
change between any two snapshots. Once releases start, every release
gets its own section here.
:::

## Unreleased

Changes that an operator running an earlier snapshot of `main` notices.
Update the CRDs and the operator in the order the entries below give:
the new CRDs reject or prune the old field names.

### API fields

- **`MailServer.spec.domains` is removed; `MailDomain.spec.serverRef`
  replaces it** ([ADR 0014](../decisions/0014-domain-server-binding-and-hostname.md)).
  `serverRef` is required (same namespace) and mutable. Steps, in this
  order: scale the operator Deployment to 0 (the data plane keeps
  running), install the new CRDs, re-apply your objects (add
  `serverRef: {name: <server>}` to every `MailDomain` and drop
  `domains` from every `MailServer`), then deploy and start the new
  operator. Any other order leaves a running server without a domain:
  once the new CRD is installed, a stored `MailDomain` has no
  `serverRef` and the new CRD prunes `spec.domains`, so neither operator
  version finds a domain for the server. It is then `Ready=False` with
  reason `NoDomains` and its running data plane is re-rendered fail
  closed (Postfix rejects every recipient with a permanent `554 5.7.1`,
  so senders get a bounce; submission is off) until the objects are
  re-applied.
  In commands: `kubectl -n cloud-native-mail-service-system scale
  deployment cloud-native-mail-service-controller-manager --replicas=0`,
  then `make install` (CRDs only), then `kubectl apply` of the edited
  objects, then `make deploy IMG=<image>`. A `MailServer` that still
  contains `domains:` is rejected by `kubectl apply` with an "unknown
  field" error (strict field validation), so drop the field before
  applying. No release exists yet, so this only concerns clusters that
  run a snapshot of `main`. A domain belongs to one server; a second
  server of the namespace that served the same domain needs its own.
  New: `MailServer.status.domains`, the conditions `ServerFound` (on the
  `MailDomain`) and `DomainsListed`, the reason `NoDomains` and the
  printer column `Server` on `MailDomain`. The reason `DomainNotFound` is
  gone. Deleting a `MailDomain` now takes it off its server with the
  next validated rollout.
- **`MailServer.spec.hostname` is required** ([ADR
  0014](../decisions/0014-domain-server-binding-and-hostname.md), part B).
  It is the name the server presents: Postfix `myhostname` and HELO, the
  name of the certificate, the target of the MX records of its domains
  and the `mx` host of their MTA-STS policies. Before, that name was the
  first domain the server served, in sorted order. Same order of steps as
  above (operator to 0, CRDs, re-apply, start the operator); a stored
  `MailServer` without `hostname` is rejected on its next spec write.
  Per server: choose the hostname
  (for example `mail.<your domain>`), have the platform admin grant it
  with a `MailDomainGrant` of its own (or `includeSubdomains` on the
  grant of the parent domain), create its `A` and `AAAA` records at the
  inbound address (`status.records` lists them), and point the `MX`
  records of the domains at it (`status.records` of each `MailDomain`
  lists them). The hostname must be unique among the servers
  that compete for it, and a name below `cluster.local` is refused. What
  changes on a running server:
  - the certificate lists the hostname only, no longer the domains:
    submission, IMAP and LMTP clients must connect to the hostname (or
    verify it), not to the domain. The Certificate is reissued at once
    and the pods pick the new one up by reload, so there is a short
    window of the old names on a rolled-out revision;
  - the HELO name and the origin of bounces are the hostname, and its
    SPF record (`status.records`) should be published;
  - the `mx` host of an MTA-STS policy is the hostname: publish the new
    policy and `_mta-sts` record before the old server name stops
    answering;
  - an ACME issuer cannot validate the hostname with HTTP-01, because the
    mail load balancer does not serve HTTP; use DNS-01 or a private CA
    ([how-to](../how-to/dkim-keys-and-dns-records.mdx));
  - a hostname that is not authorized does not take the server down: it
    presents the in-cluster name of its Postfix Service, keeps serving
    its domains and is `Ready=False` with reason `HostnameNotAuthorized`
    or `DuplicateHostname`. The same holds for a rename: the domains stay
    served while the new revision rolls out, and the pods present the old
    name until then. A hostname change never fails the domains closed,
    also not while the server is blocked for another reason (for
    example a missing `MailDirectory`): only the Certificate is
    re-applied, and the pods keep the old name until the next validated
    rollout;
  - a server stored by an earlier snapshot of `main` has the domains on
    its Certificate and no hostname in its spec. If it is blocked at the
    time of the upgrade (directory, users Secret or issuer missing), its
    first reconcile does not fail closed: the domains stay served, the
    Certificate is re-applied with the authorized hostname (or the
    in-cluster Service name until `spec.hostname` is granted), and the
    pods change with the next validated rollout.
  New: `MailServer.status.records`, the conditions `HostnameAuthorized`,
  `AddressRecords` and `RecordsVerified` on the `MailServer`, the reason
  `HostnameNotAuthorized` of `MTASTSReady`, the `MX` record in
  `MailDomain.status.records` and the printer column `Hostname`. The
  `MailServer` controller now verifies DNS records too, so
  `--dns-resolver` applies to it as well.
- **`MailServer.spec.spf` lists the sending sources of the server**
  (`ip4`, `ip6`), and `MailDomain.spec.spf` is additive to it: the SPF
  record of a domain lists the sources of its server and its own `ip4`,
  `ip6` and `include`. The sources every domain of a server shares move
  to the server; a `MailDomain` that keeps them lists the same address
  twice in the object, but once in the record. The server's sources do
  not apply to a domain it does not serve.
- **`MailDomain.spec.dkim.selector` is now `selectorPrefix`.** The
  selector of a key is `<prefix>-<yyyymmdd>`; the selectors in use are
  in `status.dkim`. Rename the field in your manifests. The CRD no
  longer defaults it (the operator uses `cnms`), because it must stay
  unset when `keys` is used.
- **`MailDomain.status.dkim` is a list of keys.** The fields `selector`,
  `keyAge` (a duration) and `pendingSelector` are replaced by `active`,
  `pending` and `retiring` (each key with `selector`, `algorithm` and a
  `keyCreated` timestamp; retiring keys also have `until`) and
  `nextRotation`.
- **`MailDomain.spec.dkim.rotation.overlap` defaults to 120h** (was
  24h), which covers the Postfix queue lifetime of 5 days. An interval
  of 5 days or less is rejected unless `overlap` is set to a shorter
  value. The overlap must be positive.
- **`MailDomain.spec.dmarc.testing` publishes `t=y`** with its RFC 9989
  meaning (receivers apply the policy one level lower), not "evaluated
  but not enforced".
- **`MailDomain.spec.tlsRpt.enabled` requires a report address** in
  `tlsRpt.rua` or `dmarc.rua`; such an object is rejected at admission.
- **A `MailDomain` name is limited to 63 characters.**
- **`MailDomain.status.mtaSts.policyId` is a string**, derived from the
  policy content (a truncated SHA-256), no longer an increasing integer.
- **`MailDomain.status.records` is an atomic list**, no longer a map
  keyed by name. Clients that patch single records must replace the
  whole list.
- **`MailDirectory.spec.ldap.userFilter` is renamed `activeFilter` and
  has a new meaning** ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.8). It is a static restriction **without** a placeholder (no
  `{user}`, no `%`; the characters `{`, `}` and `%` are rejected), for
  example `(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))`;
  unset means no restriction. The operator builds the lookups around it.
  The old name is not accepted: the CRD prunes a stored `userFilter`, and
  `kubectl apply` with strict field validation (the default) fails with an
  unknown-field error, so remove the field and set `activeFilter` if you
  want a restriction. A GitOps tool that does not validate strictly drops
  the field silently, and the account set then widens to every entry that
  has the attributes: check the objects after the upgrade. No release
  exists yet, so this only concerns clusters that run a snapshot of `main`.
  New: `spec.ldap.attributes` (`login`, default `uid`; `mail`, default
  `mail`; `aliases`, optional) names the attributes of an account, and
  `spec.sql.users` (default relation `cnms_users`) and `spec.sql.aliases`
  name the relations of a PostgreSQL directory, each with an optional
  `schema` (default `public`). The column names are fixed. The API takes no
  SQL and no filter fragments.
- **New resource `MailUser`** ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md)):
  an operator-managed account with a login (the object name), a primary
  address in `domainRef`, up to 32 `aliases` and an optional
  `passwordSecretRef`. Nothing existing changes. The operator reconciles a
  `MailUser`: it writes its status and its credential Secret (see the
  entry on `update` on Secrets below), and the `MailServer` renders the
  accounts of its `MailUser`s (the entries below). Install the
  new CRD (`make install`) before the new operator: the operator watches
  `MailUser`s and exits at startup while the CRD is missing (the data plane
  keeps running). The operator role gains `get`, `list` and `watch` on
  `mailusers`, `patch` on `mailusers/status`, `update` on
  `mailusers/finalizers` and `update` on `secrets`.
- **New resource `MailServiceAccount` and new field
  `MailServer.spec.serviceAccounts.allowedNamespaces`** ([ADR
  0008](../decisions/0008-user-sources-and-service-accounts.md), [ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.9). A `MailServiceAccount` lives in the namespace of an
  application, names a `MailServer` in `spec.serverRef` (with an optional
  `namespace`) and the one address it may send as in `spec.sender`.
  `allowedNamespaces` is a list of objects with a `name` and optional
  `senderDomains`; the names are unique and there are at most 64 of them.
  Nothing existing changes. The operator reconciles a `MailServiceAccount`
  ([how-to](../how-to/service-accounts.md)): it checks the consent, the sender
  and, for a server with a directory, the directory, writes the status, and
  makes a credential Secret `<name>-credential` and a NetworkPolicy
  `<name>-submission` **in the namespace of the account**, which for an account of
  another namespace is a namespace the `MailServer` does not live in. Install the new CRDs
  (`make install`) before the new operator: the operator
  watches `MailServiceAccount`s and exits at startup while the CRD is missing
  (the data plane keeps running). The operator role gains `get`, `list` and
  `watch` on `mailserviceaccounts`, `patch` on `mailserviceaccounts/status`
  and `update` on `mailserviceaccounts/finalizers`; it already had `create`,
  `update` and `delete` on `networkpolicies` and `secrets` in every namespace,
  and uses them in the namespaces of the accounts now, and it reads the
  `ca.crt` key of the TLS Secret of the `MailServer` for the credential. The
  controller also reads the `MailDirectory` of a server that has one (with the
  Secrets it names), and asks that directory about the sender of an account.
  The `MailServer` controller now also watches and reads the accounts and the
  credential Secrets of its accounts (`get`, `list` and `watch` on
  `mailserviceaccounts`; a label-selected `list` of Secrets in the namespace of
  each account, which its role already allowed).
- **The `MailServer` serves service accounts** ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md), sections
  4.2 and 4.9; [how-to](../how-to/service-accounts.md)). An account that the
  server accepts becomes a login (by the name `sa.` and 16 hexadecimal
  characters, in the passdb file of the live Dovecot Secret, with no userdb
  line), the owner of its sender in the Postfix sender table and in the table
  of the owned header `From` addresses of Rspamd, and nothing else. Its sender
  takes part in the decision about the addresses of the server at the rank below
  the aliases, so it can never take the address of a user. A server whose only
  accounts are service accounts is a send-only server and no longer
  `NoUserSource`. Existing servers change in three ways: the senders of the
  accounts count as addresses toward the limit of 5,000; **the `dovecot.conf` of
  a server with a directory gains the passdb of the passwd-file (asked before the
  directory) and the limit of four auth workers**, which is a validated revision
  and restarts Dovecot once; and a server with a directory **and** a service
  account reads `smtpd_sender_login_maps` as the union of its table and the
  directory's (`unionmap`), which is a new Postfix revision when the first
  account is served. A server without service accounts keeps its Postfix files.
- **Storage sizes are immutable:** `spec.postfix.queueStorage` and
  `spec.mailboxes.storage` cannot be edited once set, and decreasing
  `spec.postfix.replicas` is rejected. Resize the PersistentVolumeClaims
  by hand ([ADR 0010](../decisions/0010-storage-immutability-and-scale-down.md)).

### MailDomain controller

- **A `MailDomain` is reconciled now.** An authorized domain gets an
  immutable DKIM key Secret `<name>-dkim-<selector>` (labelled
  `cnms.rpp.gmbh/maildomain`, no owner reference, kept when the
  `MailDomain` is deleted) and the annotation `cnms.rpp.gmbh/rotation`.
  Tell your GitOps tool to ignore that annotation. An unauthorized
  domain gets neither. Its status lists the required DNS records.
- **The MTA-STS policy lists the hostname of the `MailServer`** in
  `spec.serverRef` when it serves the domain and the hostname is
  authorized. Without such a server `MTASTSReady` is `False` with reason
  `NoMailServer` (or `HostnameNotAuthorized`) and there is no policy. The `MailDomain` controller needs `get`, `list` and `watch` on
  `mailservers` (the operator role has them already).
- **New operator flag `--dns-resolver=host:port`** (default: the system
  resolver of the pod) for the verification of the records.
- **New RBAC:** `patch` on `maildomains` (the annotation), and, for the
  optional `externalDNS` mode, `get`, `list`, `watch`, `create`,
  `update`, `patch` and `delete` on `dnsendpoints.externaldns.k8s.io`.
  The Secret permissions are the ones of the `MailServer` controller.
  Install the CRDs before the new operator starts: `status.dkim.active`
  allows 8 entries and `kubectl get maildomains` has an `Authorized`
  column.
- **`externalDNS` mode needs `--managed-record-types=TXT`** on the
  external-dns instance and a tenant-proof setup, see
  [DKIM keys and DNS records](../how-to/dkim-keys-and-dns-records.mdx).
- **A selector with more than one DKIM TXT record is not verified.**
  Receivers cannot tell which record applies, so the operator no longer
  switches signing to a key that they may not find. Remove the extra
  records.
- **A `MailServer` leaves out a domain that is a public suffix** such as
  `co.uk` (`DomainsAuthorized`, reason `PublicSuffix`), as the
  `MailDomain` controller already did.

### Data plane

- **The envelope sender of an authenticated user is bound to the addresses
  it owns** ([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.4). On port 587 Postfix now refuses a `MAIL FROM` that the
  logged-in account does not own (`553 5.7.1`), one that nobody owns (a
  served domain, an unknown local part, a foreign domain) and the null sender
  (`554 5.7.1`; a bounce or a disposition notice cannot be submitted on 587).
  Before, any authenticated user could send as any address. Owned means: for
  a static test user its mailbox address, if its domain is authorized; for a
  directory user the addresses the directory returns for it (`mail` and the
  alias attribute for LDAP, the `mail` column and the aliases relation for
  PostgreSQL) in an authorized domain, so a directory user without a value in
  the `mail` attribute cannot send, and an application that sends as an
  address its login does not own is refused: check the `From` of your
  applications before the Postfix pods roll. A lookup error in the directory
  is `451 4.3.0`. Port 25 is not affected. Every Postfix StatefulSet rolls
  once on this upgrade, because the new tables and settings change the
  configuration hash of every server. Rendering: the
  Postfix pods mount four more table files (`valias`, `sender_login`,
  `recipient_defer`, `null_sender`; `main.cf` sets `smtpd_sender_login_maps`
  and the `587` entry carries the restrictions in the braces form),
  `virtual_alias_domains` is set to empty for a server with static users
  too, and a server with a directory and submission but no mailboxes gets the
  sender table of the directory. The `lmtp` service gets
  `-o soft_bounce=yes`: a permanent reply of Dovecot to mail that Postfix
  accepted is now a deferral, so the mail stays queued and is retried
  instead of bouncing. The price is that any permanent LMTP error, for
  example a mailbox that is missing for another reason, is retried until the
  queue lifetime (5 days) ends and only then bounces ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.7). Postfix still refuses an unknown recipient at `RCPT` with a
  permanent `550 5.1.1`.
- **Dovecot's users are two passwd-files in a live Secret, outside the config
  hash** ([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.7). For static test users and `MailUser`s the passdb and userdb are
  the keys `passdb` and `userdb` of the Secret `<server>-dovecot-users` (before:
  the key `users` of the hash-named Secret `<server>-dovecot-<hash>`), which the
  operator creates and updates in place and the pod mounts as a directory at
  `/etc/dovecot-users`. `dovecot.conf` points the passwd-file passdb and userdb
  at them, checks hashes in an auth worker (`passdb_use_worker = yes`) and
  limits the workers to four (`service auth-worker { process_limit = 4 }`). The
  passdb lines carry `user=<username>`, so a login by address and by username is
  the same account. The first reconcile after the upgrade changes the Dovecot
  pod template (a new volume; for a server with a directory the directory
  password and CA are mounted at `/etc/dovecot-secret`, which changes the
  rendered `dovecot.conf`), so every server gets **one validated rollout of
  Dovecot (one restart)**. After that a changed, added or removed account is no
  rollout. The superseded Secrets of the old scheme are removed by the cleanup
  of the rollout; Postfix and Rspamd are not affected by the move. A static
  user whose mailbox domain is not an authorized domain of the server can no
  longer log in or send (it is suspended: it still has its userdb line, so mail
  that is already accepted is delivered), and while no domain is authorized no
  static user can log in. Before, Dovecot rendered every static user whatever
  its domain. The Secret is owned by the `MailServer`; a Secret of that name
  that it does not own is never overwritten (`OwnershipConflict`). The live
  Secret is not watched: a deleted one is written again by the next reconcile.
  The `MailServer` role gains `list` on `mailusers` and `update` on `secrets`
  (see the entry above); an operator with a `ClusterRole` of its own needs
  them too.
- **Limits on accounts and on the size of the rendered objects** (ADR 0015,
  section 4.7). A server with `MailUser`s or static test users is blocked with
  `Ready=False` and the reason `UserLimitExceeded` (more than 1,000 accounts),
  `AddressLimitExceeded` (more than 5,000 addresses, or more than 32 aliases
  for an account) or `ConfigTooLarge` (a rendered object of 1,000,000 bytes or
  more), and keeps its running revision. This also applies to existing servers
  with static test users.
- **A `MailServer` with a `directoryRef` now uses the directory** ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  [how-to](../how-to/directories.md)). Before, the `MailDirectory` was
  only checked for existence: Postfix rendered an empty recipient table
  (every recipient refused with `550 5.1.1`) and Dovecot had no users. Now
  Postfix looks recipients up in the directory (`ldap:` or `pgsql:`
  tables, `virtual_alias_domains` is set to empty) and Dovecot
  authenticates and finds the mailbox there. What you notice on a running
  server: the Postfix and Dovecot pods roll once, they mount a Secret
  with the directory password and CA (`/etc/postfix-secret`,
  `/etc/dovecot-secret`), and the Postfix container gets `PGSSLMODE` (and
  `PGSSLROOTCERT`) for a PostgreSQL directory. The `MailDirectory` Secrets
  must exist and be valid, or the server is `Ready=False` with the new
  reason `DirectoryInvalid` and keeps its running revision; a password with
  a trailing newline is now refused there and in the `MailDirectory`
  (`SecretIncomplete`). The Postfix image must be
  `cnms-postfix:3.11.7-1` or later (the `pgsql` map type).
- **A `MailServer` has one user source.** A server that sets both
  `directoryRef` and `submission.insecureStaticUsers` is now
  `Ready=False` with the reason `UserSourceConflict`, an Event says so, and
  its running revision keeps running; before, the static users won silently.
  Remove one of the two. The data plane of such a server does not change
  until you do.
- **A directory needs an egress allow of your own: add it before you
  upgrade.** The NetworkPolicies of the data plane (default-deny egress, DNS
  and the pods of the server only) also stop Postfix and Dovecot from
  reaching the directory. In a cluster that enforces NetworkPolicies a server
  that rolls to this version without the allow answers `451 4.3.0` to every
  recipient and fails every login. Add a policy that selects the Postfix and
  Dovecot pods of the server (`cnms.rpp.gmbh/mailserver=<server>` with
  `app.kubernetes.io/component` in `postfix`, `dovecot`; the first label
  alone also matches Rspamd and Valkey) and allows the directory port (389 or
  636, 5432), as the e2e suite does.
- **The Postfix validation Job mounts the Secret with the directory tables**
  so that `postfix check` and `postconf -n` can open them (they warn about a
  table file that is missing, which fails the gate); no lookup is made, so
  the gate needs no directory. `postmap -s` parses the `texthash` tables
  only.
- **What else changes for a server with a directory.** A `MailDirectory` with
  LDAP `tls.mode: None` is `Ready=False` (`InvalidSettings`) and a server that
  uses it is `DirectoryInvalid`: Dovecot would send the password of every
  login in clear text. The same holds for `%{` in the base DN or the bind DN
  and for blanks around either. A PostgreSQL alias counts only when its
  target is the primary address of an account in the users relation whose
  login passes the login guard; other aliases were accepted at `RCPT` and
  bounced, and now get `550 5.1.1`. The Postfix container gets
  `PGCONNECT_TIMEOUT` (5 s). The Postfix image installs `ca-certificates`, so
  a directory without a `caSecretRef` is verified against public roots; the
  image tag is unchanged because `3.11.7-1` was not released before.
- **The header `From` of authenticated mail must be owned by the login, on a
  server with `MailUser`s or static test users** ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
  section 4.4, check 2). Rspamd now rejects such a message with `554 5.7.1`
  (`From address is not owned by the login`) when its `From` is another
  account's address or an address nobody owns, even in a served domain; before,
  only the envelope sender was bound. Mail clients that set a `From` other than
  the account owns, "send as" features and mailing-list software must use an
  alias of the account, or the account that owns the address. A server with a
  `directoryRef` is not bound this way (the owned addresses are outside the
  cluster). The table of the owned addresses is a new ConfigMap
  `<server>-rspamd-d-<hash8>`, so every server rolls Rspamd once with this
  version (a validated rolling update with `maxUnavailable: 0`), and a new,
  removed, disabled or re-enabled account, or a changed alias, is a Rspamd
  revision from then on, next to the Postfix one (a password change is
  none). Check the `From` your applications send before the Rspamd pods roll. The guard-matrix script and its fixture changed with it
  (`make test-rspamd-guard`).
- **The hash of the secret files is keyed, for every server that has some.**
  The hash that names the Secrets of a revision and enters the config hash
  is an HMAC under the new Secret `<server>-hash-key` (created once, owned by
  the server, when a render holds secret content). This holds for a server
  with a directory (the Secret with its password and CA): its Secrets and
  pods roll once with this version, and once more if a restore from GitOps
  loses the key Secret. The password hashes of static test users and
  `MailUser`s are live data and need no key; a server without secret content
  needs none.
- **Outbound mail now fails closed without a DKIM key.** Rspamd signs
  the mail of authenticated senders and Postfix's own non-SMTP mail with
  the active keys of the `MailDomain`s a server serves, and a guard defers
  such mail (`451 4.7.1`) when a signature is missing. Before, nothing was
  signed and everything left unsigned. After the upgrade, a served domain
  that has no usable key (a `MailDomain` that is not reconciled yet, your
  own key Secret missing or invalid) sends nothing until it has one;
  `DKIMKeysAvailable` on the `MailServer` names it. A `MailDomain` that is
  not authorized is not a served domain: see the `554 5.7.1` rule below. Check
  that every `MailDomain` of a server is `Authorized` and has its key
  before the upgrade rolls Rspamd. Mail from a signed sender with several
  `From` addresses, one of them in a served domain, is rejected with
  `554 5.7.1` (locally submitted mail of that kind bounces to its
  sender); it can never be signed. An error in the guard defers the mail
  of authenticated senders and loopback mail with a sender. Authenticated
  mail whose header `From` is missing, has no domain or is not in a
  domain the server serves (a subdomain of a served domain counts as
  unserved) is now rejected with `554 5.7.1` instead of leaving
  unsigned; check that your applications send with a served `From`
  address before the Rspamd pods roll
  ([ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md)).
  Unauthenticated mail from other pods and the bounces Postfix generates are not signed.
  The Rspamd pods roll once (a new `dkim_signing.conf` and
  `rspamd.local.lua`, validated by a Job first), and mount the keys in a
  read-only volume at `/etc/rspamd/dkim`.
- **Dovecot listens on the container ports 31024 (LMTP), 31143 (IMAP)
  and 31993 (IMAPS)**; the Service ports stay 24, 143 and 993. The auth
  port is 12345. When the Dovecot pod rolls to the new ports there is a
  short LMTP and IMAP interruption; Postfix defers LMTP mail and does
  not lose it. Custom NetworkPolicies or probes that name the old
  container ports must follow.
- **Configuration lives in immutable ConfigMaps named
  `<server>-<component>-<hash8>`** (and an immutable Secret for the directory
  password and CA of Dovecot; the Dovecot users are a live Secret, see above).
  The operator creates them, validates the configuration in a Job before the
  rollout and deletes the ones no workload references after the rollout; a
  configuration object of the previous naming that carries the CNMS labels
  falls under the same cleanup ([ADR 0009](../decisions/0009-config-validation-gate.md)).
- **The images are named `ghcr.io/rocketpadplatforms/cnms-postfix` and
  `ghcr.io/rocketpadplatforms/cnms-e2e-client`** (before `cnms/postfix`
  and `cnms/e2e-client`). Clusters without access to ghcr.io need a
  mirror.

### Network and operator

- **The operator can update Secrets** (`update` on `secrets`, new). The
  `MailUser` controller makes the credential Secret `<name>-credential`
  and rewrites it in place when a password changes ([ADR
  0015](../decisions/0015-user-sources-directory-users-and-aliases.md), section 3,
  point 4, and 4.7 for the verb). The role is a `ClusterRole`, so the operator can overwrite any Secret
  it can reach; it could already delete and create any Secret, so `update`
  adds the overwrite without the delete. An operator that runs with a
  `ClusterRole` of its own (not the generated one) needs the verb `update` on
  `secrets` before this version starts, and `update` on
  `mailusers/finalizers` (for the owner reference of the credential Secret
  where the OwnerReferencesPermissionEnforcement plugin is on). Nothing else
  changes for a cluster without `MailUser`s.
  The password hash is computed inside the operator (Argon2id, 19 MiB per
  hash, one at a time), so the memory limit of the manager container is
  raised from 128Mi to 192Mi and the container gets `GOMEMLIMIT=160MiB`
  (`config/manager/manager.yaml`); an operator deployed from your own
  manifests needs both, or a limit of its own that leaves room for 19 MiB of
  garbage per hash.
  The hardening that limits the verb to Secrets with the managed-by label (a
  `ValidatingAdmissionPolicy`) is in the [backlog](../backlog.md).
- **The operator no longer renders `cnms-no-port25-egress`.** Port-25
  egress for other pods is denied by an admin-owned
  `CiliumClusterwideNetworkPolicy`; start from the example in
  `config/network-policy/smtp-egress-deny` ([how-to](../how-to/block-direct-smtp-egress.md)).
  Delete the old per-namespace policies, which the operator no longer
  manages.
- **A `ClusterIssuer` in `spec.tls.issuerRef` needs the operator flag
  `--allow-cluster-issuers`.** Without it such a `MailServer` is
  `Ready=False, reason=ClusterIssuerNotAllowed`
  ([install and operate](../how-to/02-install-and-operate.mdx)).
