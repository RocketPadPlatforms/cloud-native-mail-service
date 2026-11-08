# Testing

CNMS tests run in four layers, each with a different level of
realism:

| Layer | Tool | Covers |
|-------|------|--------|
| Unit | `go test` | Renderers (golden files), parsers, the domain-authorization evaluator |
| Controller | envtest (`make test`) | Reconciliation: gates, fail-closed blocking, conditions, data-plane apply, optional-integration discovery; every file below `config/samples` (including `full-setup/`) is applied to the API server with strict field validation, and the docs embed those files, so examples cannot drift from the CRDs |
| Guard matrix | Docker + `rspamc` (`make test-rspamd-guard`) | The never-unsigned guard and DKIM signing in the pinned Rspamd image, with the configuration the renderer produces |
| E2E | kind + swaks + Mailpit + CoreDNS (`make test-e2e`) | The full data plane in a cluster: mail actually flows through Postfix, Rspamd and Dovecot |

The renderer golden files (`internal/render/*/testdata`, regenerated with
`go test -update`) cover, besides the engine configuration:

- Postfix: the five table files of the accounts (`accounts.vmailbox`,
  `valias`, `sender_login`, `recipient_defer`, `null_sender`) for accounts
  with aliases in two domains, a disabled account, an account whose primary
  domain is not authorized and a conflicted address; `accounts.maincf` and
  `accounts.mastercf` (the strict sender binding on the submission listener,
  `soft_bounce` on the `lmtp` service); the directory tables, including the
  sender table of an LDAP and a PostgreSQL directory;
- Dovecot: `accounts.passdb` and `accounts.userdb` (the login files and the
  delivery files differ for a disabled and a suspended account) and the
  directory blocks.

Unit tests of `internal/addresses` cover the pure decision about address
ownership (ranks and conflicts, the per-address reasons, suspended and
disabled accounts, the limits of 1,000 accounts and 5,000 addresses, the
byte guard at 1,000,000 bytes, and what a blocked revision still applies).
They run every scenario with the input in several orders and require the same
answer, check the invariants on random servers, and check that
`render.PlanAccounts` agrees with the verdict.

The `MailUser` controller has envtest specs (`mailuser_controller_test.go`)
for the credential Secret (generated once and kept, a provided password hashed
and not rehashed after a write that changes no password, a lost race on
create or update, an owner reference, a 64-character name), the addresses and
conditions (aliases that are not served, a suspended account, conflicts, a
disabled account, the other user sources) and one spec through the watches of
a running manager. Unit tests cover the Argon2id hash with a known answer that
the pinned Dovecot accepts, the verification of a hash that Dovecot made, the
bounds of a stored hash, and the map functions of the watches. The specs
assert that no password or hash reaches a condition, an Event or the log.

The `MailServiceAccount` controller has envtest specs
(`mailserviceaccount_controller_test.go`) for the credential Secret (its keys
and the login, the CA chain, a hash that is verified before the password is
kept and replaced with the password when it does not match, a deleted Secret
made again, a Secret of the name that the account does not own), the egress
NetworkPolicy (its shape, put back when changed, removed with the credential
when the namespace is revoked, a policy of the tenant never taken over), the
consent and `senderDomains`, a sender in a domain that is not served or not
authorized, the sender conflicts (two accounts, a `MailUser`, and a namespace
that is not consented to that must not take the sender away), and the
directory check with a fake lookup (asked once per sender, a new account or
sender withheld while the directory is down, an admitted one kept, a hit; an
account that moves to another server, and a server that gains a directory, are
asked again), the read of the accounts of a server through the index (a test
client answers the field selector the way the cache does), the verification of
the hash only for a Secret whose resourceVersion changed (counted through a
hook), a pair that the tenant wrote and that is kept for its own login, and a
server of another namespace that does not exist, which looks like one that does
not consent.
Unit tests cover the login with a known answer, the consent and the senders
that take part in the decision, the shared address input with senders, the
indexes and the map functions of the watches (through the indexes, not the
whole cluster), and the filter and the query of the sender lookup of a
directory (the address is escaped or a parameter). The lookup itself runs
against closed ports only: a successful lookup against an LDAP or PostgreSQL
server is not covered, and the manager watches of the accounts are covered by
the map functions, not by a running manager.

Unit tests check the decision behind them (`render.PlanAccounts`: logins,
mailboxes, aliases, owners of senders, deferred addresses, refusals) and that
no unsafe value (a blank, a newline, a colon, a `+`, `..`) reaches a table
file.

The envtest suite installs the optional integration CRDs only on request:
`PROMETHEUS_CRDS=1` adds a minimal `ServiceMonitor` CRD
(`hack/crds/prometheus-operator`) and `DNSENDPOINT_CRDS=1` a minimal
`DNSEndpoint` CRD (`hack/crds/external-dns`). CI runs the suite without
both and with both, so every integration is exercised present and
absent. The `MailDomain` specs use a fake clock and a fake DNS resolver;
they never touch real DNS. The `MailDirectory` specs use a fake prober
behind the `directory.Prober` interface and never connect to a directory;
the real LDAP and PostgreSQL probes are covered by unit tests of their
filter, configuration and error classification; a real directory answers
only in the e2e fixtures of
[ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md).

## The guard matrix

`hack/test-rspamd-guard.sh` runs the case table of
[ADR 0013](../decisions/0013-never-unsigned-guard.md) against the real
engine. It needs `docker`, `go` and `jq`:

1. `hack/render-rspamd-fixture` renders the Rspamd configuration of a
   test `MailServer` with the production renderer (so the script never
   holds a copy of the golden files) and generates throwaway RSA-2048 and
   Ed25519 keys for six served domains: RSA and Ed25519, RSA only, one of
   two keys missing, a key missing, a key the Rspamd user cannot read,
   and no key. It renders three servers: one with the accounts `alice`
   (an address in every domain and `sales@dual.example`) and `bob`, one
   with a directory (check 2 off) and one without a single account.
2. The pinned Rspamd image (read from `internal/images/images.go`) starts
   the way the manifests start it: read-only root file system, the
   Rspamd user, no capabilities, and no network, so no DNS lookup or map
   download makes a run slow or different.
3. Messages are scanned through the normal worker with `rspamc`, with
   `--ip`, `--user` and `--from` set like Postfix's milter does. Each
   case compares the action, the message, the `DKIM_SIGNED` options and
   the algorithms of the `DKIM-Signature` headers.

The matrix covers: authenticated mail signed with every active key; a
missing, unreadable or absent key, and a served domain without keys, answered
with a soft reject (`DKIM signing unavailable, try again later`);
unauthenticated mail from Internet and pod addresses, with an unserved or no
`From`, left untouched and never deferred; authenticated mail whose `From`
header is missing, has no domain or an empty domain, or is in an unserved
domain or a subdomain of a served one, rejected (`From must be an address in
a served domain`, [ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md));
loopback mail (Postfix's own non-SMTP mail) signed, deferred without a key,
and exempt when the envelope sender is empty; a message that is rejected
anyway stays a reject; check 2 of [ADR
0015](../decisions/0015-user-sources-directory-users-and-aliases.md) (the
header `From` of authenticated mail must be owned by the login): an owned
address, an owned alias, a case variant and a display name with a comma pass
and are signed, the login in other case passes, an address of another
account, an address nobody owns, a login that owns another address and a
login that is no account are rejected (`From address is not owned by the
login`), unauthenticated and loopback mail is not bound, a server with a
directory passes every served address, a server without accounts refuses
every one, and a table that is missing, unreadable or malformed defers the
mail of logins and is logged; a message with several `From` addresses, one of
them in a served domain (in one header or in two), is rejected (`multiple
From addresses cannot be DKIM signed`), also from loopback, and left alone
when there is no login, and from loopback when no address is served or there
is no `From` header; an error inside the guard defers authenticated mail and
loopback mail with a sender, leaves everything else alone and is logged; an
error in the source detection defers authenticated mail only. `rspamadm
configtest` is run for the good configuration (exit 0, also with missing
keys), a guard with a syntax error and a guard that fails at load time (exit
1 each).

A run takes under a minute once the image is present, can be repeated
at will, and removes its containers and temporary files on exit.

## The e2e suite

The e2e suite (`test/e2e`) creates an isolated kind cluster and
deploys the operator (with
`--enable-insecure-test-features`), a cert-manager
self-signed `Issuer`, and one `MailServer` with the hostname
`mail.e2e.example.org` (its own `MailDomainGrant`, apart from the one
of the domain), a `MailDomain`, and the two directory fixtures below; the
server's only user source is the PostgreSQL directory. The Mailpit sink (ADR 0004, decision 7) runs in
the same namespace as a plain Pod with a Service; its cluster FQDN
(`mailpit.<ns>.svc.cluster.local`) is used as a *recipient domain*
whose MX-then-A lookup resolves to the sink inside the cluster, so
Postfix's own SMTP client delivers outbound mail to it. Mail is sent
with `swaks` from the e2e client Pod.

Assertions (Phase 2):

- the `MailServer` reaches `Ready=True` with all four workloads
  ready (the per-component config validation Jobs have passed:
  `postfix check`, `rspamadm configtest`, `doveconf -n`, Valkey ping;
  ADR 0009);
- the Postfix queue metrics and the Rspamd metrics sidecar (port 9151,
  `cnms_rspamd_metrics_up 1`) are served as `text/plain` (ADR 0005);
- the Rspamd controller port 11334 (`/metrics`, `/learnspam`,
  `/fuzzyadd`, `/savemap`, `/history`) is not served to other pods;
- Postfix logs to the pod stdout (`maillog_file=/dev/stdout`);
- unauthenticated mail for the server's own domain is accepted on
  port 25 (postscreen) and delivered to the Dovecot maildir via
  LMTP;
- authenticated submission over STARTTLS is accepted (SASL backend
  is Dovecot with the directory user `alice`);
- the hostname is authorized (`HostnameAuthorized=True`), the certificate
  on port 587 has it as common name and only SAN (no apex, no domain of the
  DKIM specs, no Service name), the greeting is `220 mail.e2e.example.org
  ESMTP` and the `Received` header of a delivered message names it; the
  inbound Service is `ClusterIP`, so `AddressRecords` says
  `NoExternalAddress`;
- authenticated mail for the sink domain is delivered to Mailpit
  (verified through the Mailpit API);
- unauthenticated mail for a foreign domain is rejected with
  `554 5.7.1` (`reject_unauth_destination` in both
  `smtpd_relay_restrictions` and `smtpd_recipient_restrictions` —
  no open relay; the default relay restrictions would only tempfail);
- AUTH without TLS is rejected (`smtpd_tls_auth_only`);
- a broken spec (a `directoryRef` to a `MailDirectory` that does not
  exist, on a server that runs on a real one) makes the `MailServer`
  `Ready=False` with `DirectoryNotFound` **without rolling the data
  plane** (the rendered-config hash of the pod template is unchanged,
  all workloads stay ready, and the running pods still accept the
  directory's recipients and logins);
- a changed configuration waits for its validation Job before the
  pod template changes; a failing Job makes the `MailServer`
  `Ready=False` with `ConfigRejected`, the running pods and template
  stay unchanged, and restoring the spec needs no new validation;
- a renewed TLS certificate is served by Postfix without a pod
  restart (the cert-reload sidecar reloads in place);
- the Prometheus Operator integration creates the three
  `ServiceMonitor`s with the CRDs (`PROMETHEUS_CRDS=1`) and reports
  `CRDsUnavailable` without them.

The suite never sends real mail and never touches real DNS or
provider APIs (testing rule); the `example.*` domain and
documentation address ranges are used throughout.

### Directories and user sources (Phase 3)

`test/e2e/directoryfixtures.go` runs two directories in the data plane
namespace, with the same accounts: **OpenLDAP** (`osixia/openldap`, MIT
tooling around OpenLDAP, OLDAP-2.8) and **PostgreSQL** (`postgres`, PostgreSQL
License). Both images are pinned by tag and digest in `internal/images` (ADR
0015, section 9), and Renovate follows them there. The OpenLDAP tag is a
pre-release (`2.6.15-rc`): the 2.6 line has no other, so a stable tag has to
be adopted by hand. Each fixture is a Deployment with a Service (an outage
is the Service forwarding to a port where nothing listens), a server certificate from a cert-manager CA of
its own (the `MailDirectory` verifies against it: StartTLS and `verify-full`,
the defaults) and a seed: an LDIF mounted over the image's `custom` bootstrap
directory (the LDIF files load at the first start), and an initdb script for
the two relations. The accounts: `alice` (primary address
`alice@e2e.example.org`, alias `sales@e2e.example.org`), `bob`, and in LDAP
`carol`, whom `mailEnabled=FALSE` and the `activeFilter` switch off. The
lookup role has read access only (a read-only account in LDAP, `SELECT` on
the two relations in PostgreSQL). A `NetworkPolicy` of the fixture allows the
pods of the `MailServer` to reach the directories, which the egress policy of
the data plane does not (see the [how-to](../how-to/directories.md)).

The specs run against the PostgreSQL directory and then against the LDAP
directory, on the **same** `MailServer`: the LDAP run switches `directoryRef`
and the server rolls out a new, validated revision, which is cheaper than a
second server (about seven more pods and their start-up) and covers the
change of directory. They check, for each directory:

- the `MailDirectory` is `Ready` and `Reachable` (the first probe against a
  real OpenLDAP and PostgreSQL);
- a valid recipient, the same in upper case, an alias and a second account get
  `250` on port 25; an unknown address, and in LDAP the disabled account, get
  `550 5.1.1`; so does an alias of an account the directory does not serve
  (PostgreSQL: an alias with no account and one to a login the guard
  refuses; LDAP: the alias of the disabled account), where mail would
  otherwise be accepted and bounce later;
- a message for the alias is delivered **once**, into the mailbox of the
  canonical address (IMAP search for its subject finds one message for
  `alice` and none for `bob`);
- SASL on port 587 accepts the login name, the primary address and the
  address in upper case, and refuses the alias, a wrong password, an unknown
  login and (LDAP) the disabled account; IMAP accepts the first two forms
  and reads the delivered message, and refuses the alias;
- with the directory down (its Service forwards to a closed port and its pod is
  replaced, so connections are refused; packets that are dropped make a lookup
  much slower, see the backlog), recipients, known
  or not, get `451 4.3.0` (never a `5xx`), a login does not succeed (`454 4.7.0` with
  LDAP, `535 5.7.8` with PostgreSQL), and the `MailServer` stays `Ready`; with the directory back, the answers return (the PostgreSQL
  client of Postfix waits about a minute after a failed connection).

The container also covers the rule that a server has one user source
(`UserSourceConflict`, ADR 0015, section 3): a server that gets
`insecureStaticUsers` next to its `directoryRef` is `Ready=False` with that
reason and an Event, keeps its pod template and keeps answering from the
directory; removing the `directoryRef` then rolls out the static users,
which keeps the Phase 2 static path covered (the static user logs in and
receives, the directory's accounts do not); the next patch moves the server
to the LDAP directory, the specs of the `MailUser`s follow (below), and the last
patch returns it to PostgreSQL, where the `MailDomain` specs expect it.

The `MailUser` specs (ADR 0015, 3b) first show that `MailUser`s on a server with
a directory are refused (`UserSourceConflict`, the running revision stays), then
remove the directory and check, with two domains: delivery to all addresses of
one account into the one mailbox (read over IMAP by name and by address, in no
other mailbox); login by name and by primary address, not by alias; sending as
each owned address (a message reaches the Mailpit sink), and the refusals
(`553 5.7.1` for another account's address, an unowned address and a foreign
domain, `554 5.7.1` for the null sender); a disabled account that cannot log in
over SASL or IMAP and still receives (it reads the message once re-enabled); an
account added, changed and removed with no restart of the Dovecot pod (its UID
and restart count are compared) and the latency of each change; and the
revocation of the alias domain, which ends its addresses while the account keeps
its primary address; and the binding of the header `From` to the login (an
owned address passes and is signed, also for a login by the primary address; the
address of another account and one that nobody owns are refused with `554 5.7.1`). The envtest specs (`mailserver_users_test.go`) cover the
rendering: the Postfix maps, the live Secret (same name, new `resourceVersion`,
unchanged pod-template hash for a changed account), disabled, suspended and
conflicting accounts, a missing or foreign credential hash, the second user
source, the last account removed, a revoked domain, the cleanup of the earlier
scheme, and the wake-up of the server by a new `status.credentialRevision`
without waiting for the safety requeue. The limits are covered with the caps
lowered through `MailServerReconciler.Limits` (a zero field is the value of the
ADR): a revision over the cap of accounts, over the cap of addresses and over
the byte guard is held back, the revocation-only write of the live files, the
restart safety (every step after the first block runs in a fresh reconciler),
the recovery, and the sizes that the API server accepts and refuses
(`mailserver_limits_test.go`). The e2e suite takes about 22 minutes (64 specs;
the MailUser block about five of them and the account specs about three), against
the 60-minute timeout of `make test-e2e` (`go test -timeout=60m`) and the
60-minute `timeout-minutes` of the CI job, which is slower than a workstation.

CI runs the whole suite in both legs (Prometheus Operator CRDs absent and
present) on every pull request and every push to `main`, and is authoritative.
A local host with a low per-user kernel key limit (`kernel.keys.maxkeys`,
default 200) cannot start all pods of the kind cluster; the symptom is pods
stuck with a "disk quota exceeded" session key error. Raise the limit as root
on the host, or rely on CI.

The `MailServiceAccount` specs of the e2e suite (ADR 0015, 3c) run in two places,
in a second namespace `cnms-e2e-app`. On the PostgreSQL directory they check what
the directory decides, with the real lookup of the operator against the database:
a free sender is admitted for this server and directory and gets its credential
(the keys and the values of the Secret, the NetworkPolicy as an object), and the
address of a directory account is `SenderConflict` with nothing issued. The
admitted account then logs in through the Dovecot of the directory server (the
passwd-file passdb is asked before the directory's), sends as its sender and no
other address (`unionmap` of the sender tables in a running `smtpd`), and the
directory's own accounts keep their addresses and have not the sender of the
account; the accounts and the consent are removed again at once. On the server that the
`MailUser` specs leave (no directory, no user), which is then a send-only server,
they show that a namespace the server does not consent to gets
`NamespaceNotAllowed` and nothing; that the consented one gets the credential and
the policy; that the login works after the projection of the live Dovecot Secret
and a wrong password does not; that mail is sent only as `spec.sender` (it
reaches the Mailpit sink, signed, while another address of the domain and a
foreign domain are `553 5.7.1` and the null sender `554 5.7.1`), that the header
`From` must be the sender (`554 5.7.1`), that mail for the sender is `550 5.1.1`;
and that withdrawing the consent deletes the credential Secret and the
NetworkPolicy, and the login is refused after the projection. The application
is played by pods in `cnms-e2e-app` from the e2e client image that mount
`notifications-credential` at `/creds`: the login, the sending and the TLS
checks run in the pod, not in the namespace of the server. `openssl s_client`
and `swaks --tls-verify` verify the certificate of the server against `ca.crt`
and the name in `tlsServerName` (the name of the Service in `server` fails, and
without `ca.crt` the certificate is not trusted). The NetworkPolicy is tested on
kind, whose CNI (kindnet) enforces it (ADR 0004, item 7): a pod without the
label reaches the Mailpit sink and port 25 of the server, the pod with the label
reaches neither and still sends on port 587.
The envtest specs (`mailserver_serviceaccounts_test.go`) cover the server side:
the send-only server, the login taken from the account and the hash only from a
Secret the account controls and in the operator's form, the sender decided at
render time (a `MailUser` that takes the address after the admission wins, and
the account loses it without a new status), the consent withdrawn between two
passes, a server with `MailUser`s, the limit counted on senders, and a server
with a directory (the union of the sender tables, the passwd-file asked before
the directory, check 2 off); unit tests cover the accepted accounts and the
accounts of the render, and golden files the Postfix, Dovecot and Rspamd
render.

The DKIM specs below submit with the envelope sender `alice@e2e.example.org`,
the address `alice` owns in the directory fixtures (the envelope sender
binding of [ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md),
section 4.4, refuses every other address), and set the **header `From`** to
`alice@<domain>` for the domain under test (`swaks --h-From`). Rspamd signs by
the domain of the header `From` and the guard needs a served domain there, so
the specs still prove signing by that domain and the `451 4.7.1` deferral of
a domain without a key. The header `From` of a directory user is not bound to
its addresses.

The conflict, its watch and its index are also covered by envtest
(`mailserver_directory_test.go`, `watches_test.go`).

### MailDomain, DKIM and the guard (Phase 5)

The last container of the suite (`test/e2e/dkim_test.go`) reuses the
operator, the `MailServer` and the Mailpit sink of the fixture. Its setup
starts a **test DNS server**, a CoreDNS pod (Apache-2.0, pinned in
`internal/images`) that is authoritative for `e2e.example.org` only, with
no forwarding, and restarts the operator with `--dns-resolver` pointing at
it, so the records the operator verifies never leave the cluster. It then
creates two `MailDomain`s (each with its own `MailDomainGrant`): one with
generated keys and `ed25519: true`, one with a bring-your-own RSA key whose
Secret the test creates. The test copies `status.records` of both into the
zone ConfigMap, restarts the DNS pod to load it, and only then moves the
domains to the `MailServer` (they start with a `serverRef` to a server
that does not exist), so the data plane rolls out once. Once the server
serves the domains their status lists an `MX` record at the hostname; the
test publishes it with the other records (the zone serves TXT and MX
records, and an address for the hostname). The specs check:

- the DKIM and MX records the controller requires are found by the lookups
  (`RecordsVerified=True`, `Ready=True`), the names and selectors match
  `status.dkim`, and the MX record is `10 mail.e2e.example.org`;
- an authenticated message from the generated-key domain reaches the
  sink with two `DKIM-Signature` headers (`rsa-sha256` and
  `ed25519-sha256`, `d=` the domain, `s=` the active selectors), which also
  proves the projected key files are readable by the Rspamd user on a real
  cluster (ADR 0011, section 5). The suite verifies every signature of
  every fetched message cryptographically, as a receiver would, against the
  TXT records in `status.records`; the lookup answers only from those
  records and never queries real DNS;
- the bring-your-own key signs with its selector;
- deleting the `cnms.rpp.gmbh/rotation` annotation, as a restore from
  GitOps does, makes the controller derive the same annotation again: the
  key Secrets keep their names and UIDs (nothing is created, replaced or
  deleted), the active selectors are unchanged and the domain signs with the
  same keys afterwards;
- a corrupted bring-your-own key sets `DKIMKeysValid=False`, authenticated
  submission is answered with `451 4.7.1` once the kubelet has synced the
  Secret, nothing unsigned reaches the sink, and restoring the key lets the
  retry through, signed;
- with Rspamd stopped (and the operator stopped, so it cannot scale Rspamd
  back) submission is answered with a 4xx and nothing is accepted; signed
  mail flows while only the operator is down; after Rspamd returns the
  retry is signed.

The corrupted-key spec depends on the kubelet propagating a Secret change
to the Rspamd pod, which takes from seconds to a minute or two, and on the
rolling update that follows the status change; the specs poll for up to
eight minutes. In a local kind run the whole part took about 3.5 minutes.
Unauthenticated mail to
an own-domain mailbox is covered by the inbound spec above and, for the
missing signature, by the guard matrix; the e2e does not repeat it.

The signature check uses `github.com/emersion/go-msgauth` v0.7.0 (MIT),
for the e2e suite only, with `golang.org/x/crypto` (BSD-3-Clause). The
license check and report run with the `e2e` build tag, so both are
covered.

**Not covered end to end:** the rotation timeline. The interval is at
least 24 hours, so a real-time e2e is impossible. Rotation (a pending key
that waits for its verified record, the 15-minute hold, the overlap with
both selectors published, the retirement and the deletion of the Secret,
the `AwaitingDNS` stall) is covered by the state-machine tests of
`internal/dkim` and the `MailDomain` envtest specs, which use a fake clock
and a fake DNS resolver.

**Also not covered end to end:** the `A` and `AAAA` records of the
hostname. The fixture server is of type `ClusterIP` (kind has no load
balancer), which has no address to list. The envtest specs of the
`MailServer` records set the load-balancer status of the inbound Service
and verify the records through a fake resolver; `internal/dnsrecords` tests
the address and MX lookups against an in-process DNS server.

## Running the tests

```console
make test        # unit + envtest (PROMETHEUS_CRDS=1 also runs the
                 # optional Prometheus Operator integration leg)
make test-rspamd-guard  # guard and signing matrix (needs docker, go, jq)
make test-e2e    # e2e on kind (needs kind, docker and kubectl);
                 # PROMETHEUS_CRDS=1 installs the ServiceMonitor CRD
                 # first and expects the operator to create monitors
```

CI runs `make test` and `make test-e2e` on kind with and without the
ServiceMonitor CRDs, the guard matrix, the linter, the license check, and
a check that the generated files are up to date.
