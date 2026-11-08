# Backlog

Ideas parked outside the current phase, grouped by the phase that would pick
them up. Items move into a phase when they are scoped; nothing here is
committed.

## Phase 3: users and service accounts

Scoped in [ADR 0015](decisions/0015-user-sources-directory-users-and-aliases.md)
(three pull requests, see the roadmap):

- Apply the revocation-only rule of ADR 0015, section 4.7 (logins are never
  blocked) to every block that has `MailUser`s, not only to a limit: while
  a render error, `UserSourceConflict`, `DirectoryInvalid`,
  `ClusterIssuerNotAllowed`, a relay or an IP pool claim keeps the running
  revision, the live users Secret is not written, so an account that is
  disabled or deleted meanwhile keeps its login until the block clears.
- The `MailUser` controller runs the address decision over the whole server in
  every reconcile, so a restart at 1,000 accounts costs about a million
  account visits (the list is read without copies, and the decision is
  O(n log n) per pass). If that shows, decide once per server and share the
  result, for example through the `MailServer` controller.
- Directory lookup timeouts: the Postfix tables bound a lookup against a
  directory that drops packets (`timeout = 5` for LDAP, `PGCONNECT_TIMEOUT`
  for PostgreSQL; a `451` after 5 to 10 s, ADR 0015 row 27), and the
  per-client limits of ADR 0006 apply on top. Dovecot has no connect
  timeout for `pgsql` in 2.4.5: a login against dropped packets takes about
  two minutes. Look for a bound, for example a shorter query timeout of the
  SQL pool, if that matters.

Not scoped (ADR 0015, "Limits" and section 4.7):

- SASL during a PostgreSQL outage. With a PostgreSQL directory that refuses
  connections, Dovecot logs "Not connected to database" and Postfix answers a
  login with `535 5.7.8` (the e2e suite measured it), not the `454 4.7.0` of
  ADR 0015 (row 16: a stopped container, and LDAP in the e2e suite). A client
  may take the 535 for a wrong password. Find out why the internal failure
  does not reach Postfix as a temporary one, and fix or document it.
- Generate the egress allow to the directory. The default-deny egress
  NetworkPolicy of the data plane also blocks Postfix and Dovecot from the
  directory, so a cluster that enforces NetworkPolicies needs one of your own
  today (found with the e2e fixtures). The target is a host name that a
  NetworkPolicy cannot select; options: an allow for the directory port to
  any address (broad), or a field on `MailDirectory` that lists the
  addresses or a pod selector for an in-cluster directory.
- OIDC directory backend for `MailDirectory` (after LDAP/SQL).
- MySQL/MariaDB for the `sql` directory type (PostgreSQL only for now).
- An address shared by several users, distribution lists, catch-all and
  wildcard addresses, forwarding to external addresses, per-alias
  send-only, plus-addressing (`recipient_delimiter`).
- Aliases as login identifiers (only the account name and the primary
  address log in).
- `postmaster@` for every served domain: RFC 5321 expects the mailbox, and
  nothing maps it automatically (a `MailUser` or alias can claim it).
- Header From check for directory users: the owned addresses are not in the
  cluster, so a directory user can put any served address in the header
  From. Options: a directory lookup from Rspamd, or a policy service.
- Per-namespace sender allowlist for `MailServiceAccount`s beyond
  `senderDomains`: the local parts a consented namespace may use inside a
  granted domain, so it cannot claim an unowned address such as `ceo@`, and
  two consented namespaces that share a domain cannot make each other lose
  the same sender (ADR 0015, sections 4.2 and 4.9; until then every
  namespace gets its own `senderDomains`).
- A per-namespace cap on the senders of service accounts, or counting only the
  senders that win their conflict: today every sender of every consented
  namespace counts toward the 5,000 addresses of a server, those that lose a
  conflict too, so one consented namespace can fill the cap and block every new
  revision of the server, the revisions of the `MailUser`s of its owner
  included. The ADR calls it an accepted risk of consent (section 4.9,
  point 8); the cap would remove the risk.
- A service account that is removed while the server is blocked by a limit keeps
  its login until the block clears, like any account that is only in the live
  files: a spec for it (`dovecot.ParseLive` reads the empty primary address of a
  send-only account) would pin the revocation-only rule for them.
- Check 2 of the Rspamd guard (the header `From` must be owned by the login) is
  off on a server with a directory, also for the service accounts, whose senders
  the operator knows. It could be on for the logins of the form `sa.` alone, with
  a table of the senders of the accounts and a mode of its own in the guard; the
  guard matrix would grow with it (ADR 0015, sections 4.4 and 4.9).
- The `MailServiceAccount` controller does not read the static test users
  (`submission.insecureStaticUsers`), so a sender that equals the address of a
  static user is reported `Ready` while the `MailServer` (which decides with all
  claimants) does not serve it; the CA chain in the credential (`ca.crt`) and a Secret that was
  deleted come back with the ten-minute requeue, because Secrets are not
  watched; and a directory that gains the address of an admitted sender later
  is not asked again (ADR 0015, section 4.9, points 3 and 7).
- A `ValidatingAdmissionPolicy` that lets the operator update only Secrets
  that carry the CNMS managed-by label, to narrow the `update` verb on
  Secrets (ADR 0015, section 4.7).
- A restart-based rollout of user changes (a new immutable users Secret and
  a Dovecot restart per change) as the option for clusters that cannot grant
  the operator `update` on Secrets; the live users Secret is the decision
  (ADR 0015, section 4.7).
- End the open IMAP sessions of a revoked account automatically, with
  `doveadm kick` from a sidecar: a change of the live Dovecot files does not
  end them. PR 3b documents the manual procedure (`doveadm kick` in the
  Dovecot pod, or a restart of its StatefulSet).
- Split the Postfix address tables into one object each. A modest gain, not
  a fix: `valias` is 638 of the 1,022 worst-case bytes per alias, so the
  ceiling moves from about 978 to about 1,567 aliases, and at the caps the
  worst case still exceeds 1 MiB per table; the byte guard stays the limit
  (ADR 0015, section 4.7).
- Mailbox retention when a `MailUser` is deleted: the data stays on the
  volume and a new user of the same name would inherit it. Phase 4 adds an
  explicit purge; nothing is deleted automatically.
- A path for mail with a null sender that Dovecot generates (message
  disposition notifications, Sieve vacation replies): port 587 refuses it
  (ADR 0015, section 4.4); Phase 4 decides the route.

## Phase 5: `MailDomain` and domain authorization

- Derived-name conflicts (`DerivedNameConflict`, ADR 0003): a `MailDomain`
  whose own name is a derived name of another active domain. Not detected
  by the `MailDomain` controller yet.
- Publish the records of the server hostname and the `MX` records of the
  domains through external-dns: `A`/`AAAA` (from the inbound Service
  address), SPF and `MX` (ADR 0014). They are reported and verified today,
  and the `DNSEndpoint` of a `MailDomain` holds TXT records only. The
  external-dns record types and the ownership of the hostname records need
  a design (the `MailServer` would own a `DNSEndpoint`).
- Per-pod HELO names from the PTR of a bound `MailIPPoolClaim` (Phase 7,
  ADR 0014 section 8): the hostname stays the MX target and the TLS name,
  while each Postfix pod presents the PTR name of its own sending address.
  The SPF record of the hostname then lists those addresses.
- Make the fail-closed render defer (`soft_bounce` / a 450 reply)
  instead of rejecting with a permanent `554 5.7.1`: the render has
  `mydestination =` and `reject_unauth_destination` and no
  `unauth_destination_reject_code`, so a running server that is re-rendered
  fail closed (ADR 0003, 0014) bounces inbound mail instead of letting the
  sender retry.
- Close the exposure window of a revoked hostname (ADR 0014): the pods
  keep presenting the old name in HELO and the old certificate Secret
  until the next validated rollout, which a blocked server does not reach,
  and a public ACME issuer cannot issue the Service-name fallback. Options:
  roll the hostname alone through the validation gate while blocked, or
  report the stale Secret in a condition.
- `NetResolver.LookupHost` also reads `/etc/hosts` of the operator pod (the
  Go resolver has no DNS-only lookup), so a hosts entry can verify an
  address record that the zone does not hold. Use a DNS-only client for the
  address records.
- Graceful domain move (ADR 0014): changing `MailDomain.spec.serverRef`
  moves the signing and the records at once, but not the mailboxes or the
  queued mail of the old server, and the old server may briefly serve the
  domain while the new one already does. Tooling could drain the queue,
  copy the mailboxes and order the MX and MTA-STS changes.
- Reconsider the cap of 128 entries in `MailServer.status.domains`
  (ADR 0014); a server with more domains lists those not served first and
  sets `DomainsListed=False`.
- Avoid the double rollout at first install: a `MailDomain` without the
  rotation annotation has no active key, so the `MailServer` it references
  renders the domain without keys (fail closed) and again once the
  `MailDomain` controller has written the state (ADR 0011, section 5). The
  e2e suite creates the domains first and the server afterwards.
- Check that a `DNSEndpoint` target longer than 255 octets (the RSA-2048
  DKIM value) is published as one TXT record by the common external-dns
  providers (ADR 0012 left it unverified); split it into character-strings
  in the target if one needs it.
- Re-verify a pending DKIM key right before it becomes active when the
  last lookup is older than the hold period, and verify against the
  authoritative servers instead of one resolver.
- Make the Secrets of bring-your-own keys cheaper to follow: they are read
  every five minutes because Secrets are not cached; a metadata-only watch
  on labelled Secrets would react at once.
- Sign the bounces and notifications Postfix generates itself
  (`internal_mail_filter_classes = bounce, notify`): revisit with a Postfix
  that sets `{postfix_internal_origin}`, so the guard can tell them from
  `sendmail` mail (ADR 0013).
- Test that a re-injected message (`postsuper -r`, queue rescue) is
  guarded correctly (ADR 0013, section 6).
- DNS TXT proof of control on `MailDomainGrant` (`verification.dnsTXT`), for
  self-service domain onboarding (see ADR 0003).
- Additive `MailDomainGrant` fields: `allowedClusterIssuers`,
  `allowedDNSModes`. `allowedClusterIssuers` replaces the all-or-nothing
  `--allow-cluster-issuers` operator flag (ADR 0003).
- Make the revocation grace period (15 minutes) configurable.
- Capsule integration documentation (namespace prefix enforcement for grant
  binding).
- Reconsider a ValidatingAdmissionPolicy for early rejection (dropped in
  ADR 0003).
- DANE/DNSSEC automation.
- Tighten the DMARC `rua`/`ruf` (and `tlsRpt.rua`) address pattern to
  printable ASCII without `#` (the local part of a `mailto:` URI).
- When PROXY protocol is added to the inbound Service, a client must not be
  able to claim `127.0.0.1` or `::1`: that address is in `sign_networks` and
  counts as a signed source for the guard (ADR 0013).
- Keep Ed25519 keys published for the overlap when `ed25519` is switched off
  on a `MailDomain` that has no rotation annotation (today they are deleted
  at once).

## Phase 7: IP management

- rDNS backends other than `manual` (externalDNS, provider APIs).

## Phase 9: hardening, backup and observability

- Metric and alert for the deferrals of the DKIM guard (`451 4.7.1`,
  `cnms_dkim_guard`) and for a `MailServer` whose `DKIMKeysAvailable` stays
  `False` (ADR 0013).
- Sync the k8up annotations onto the PVCs of an existing StatefulSet (the
  claim templates are immutable, so toggling `spec.backup.enabled` or
  upgrading does not reach volumes that already exist) and remove them when
  `spec.backup.enabled` is switched off.
- Operator-managed k8up `Schedule` resources and repository credentials
  (user-created, documented examples for now).
- Postfix scale-down: move a removed replica's queue to the remaining
  replicas (or retain the PVC until empty), then lift the CEL rule that
  rejects decreasing `postfix.replicas` (ADR 0010).
- Operator-driven PVC resize for the Postfix queue and the mail volume
  (expand the PVCs, then allow `queueStorage`/`storage` edits); today the
  sizes are immutable and resized manually (ADR 0010).
- Show the validator output in the `ConfigRejected` message (needs `pods/log`
  access for the operator) and retry a failed validation Job after a backoff
  instead of waiting for its TTL (ADR 0009).
- Roll out a validated component while another is still validating (ADR 0009
  applies all components together).
- Restart the `RolloutStalled` clock on any template change (for example an
  engine image bump by an operator upgrade); today only a spec change
  restarts it (ADR 0009).
- `MailServer` field to opt out of the Rspamd egress policy (public TCP 443
  and UDP 11335, ADR 0006) for clusters that do not want the fuzzy and map
  traffic; the policy is always rendered today.
- Optional Rspamd message history (`MailServer` field with a retention period
  and subject privacy); disabled today (ADR 0005).
- `MailServer` field for the resource requests and limits of each component
  (`spec.<component>.resources`); the defaults are constants in
  `internal/manifests/resources.go` today.
- Valkey authentication (`requirepass` from a Secret) and a `maxmemory` below
  the container limit: Valkey runs without a password and without a memory
  cap today, protected only by the NetworkPolicies (ADR 0006), and is
  OOM-killed instead of refusing writes when it reaches its memory limit.

## Phase 10: docs and release

- Pin the CNMS-built images (`cnms-postfix`, `cnms-e2e-client`), which the
  `images` workflow publishes to `ghcr.io/rocketpadplatforms`, by digest in
  `internal/images/images.go` (tag only today, ADR 0004).
- List the licenses of the npm tooling of the docs site (Docusaurus and its
  dependencies, not shipped) in the third-party notices; only the Go modules
  and the data-plane images are listed today.
- Ship license texts with the images: run `go-licenses save` in the
  Dockerfile builder stage and copy the licenses into the image (or attach
  them to the release artifacts), so the MIT/BSD/Apache notice obligations
  are met for the operator binary; the distroless image carries none today.

## No phase yet

- Tracing.
- Webmail/UI, billing, multi-cluster federation.
- IMAP horizontal scaling.
