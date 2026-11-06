# Testing

CNMS tests run in four layers, each with a different level of
realism:

| Layer | Tool | Covers |
|-------|------|--------|
| Unit | `go test` | Renderers (golden files), parsers, the domain-authorization evaluator |
| Controller | envtest (`make test`) | Reconciliation: gates, fail-closed blocking, conditions, data-plane apply, optional-integration discovery; every file below `config/samples` (including `full-setup/`) is applied to the API server with strict field validation, and the docs embed those files, so examples cannot drift from the CRDs |
| Guard matrix | Docker + `rspamc` (`make test-rspamd-guard`) | The never-unsigned guard and DKIM signing in the pinned Rspamd image, with the configuration the renderer produces |
| E2E | kind + swaks + Mailpit + CoreDNS (`make test-e2e`) | The full data plane in a cluster: mail actually flows through Postfix, Rspamd and Dovecot |

The envtest suite installs the optional integration CRDs only on request:
`PROMETHEUS_CRDS=1` adds a minimal `ServiceMonitor` CRD
(`hack/crds/prometheus-operator`) and `DNSENDPOINT_CRDS=1` a minimal
`DNSEndpoint` CRD (`hack/crds/external-dns`). CI runs the suite without
both and with both, so every integration is exercised present and
absent. The `MailDomain` specs use a fake clock and a fake DNS resolver;
they never touch real DNS.

## The guard matrix

`hack/test-rspamd-guard.sh` runs the case table of
[ADR 0013](../decisions/0013-never-unsigned-guard.md) against the real
engine. It needs `docker`, `go` and `jq`:

1. `hack/render-rspamd-fixture` renders the Rspamd configuration of a
   test `MailServer` with the production renderer (so the script never
   holds a copy of the golden files) and generates throwaway RSA-2048 and
   Ed25519 keys for six served domains: RSA and Ed25519, RSA only, one of
   two keys missing, a key missing, a key the Rspamd user cannot read,
   and no key.
2. The pinned Rspamd image (read from `internal/images/images.go`) starts
   the way the manifests start it: read-only root file system, the
   Rspamd user, no capabilities, and no network, so no DNS lookup or map
   download makes a run slow or different.
3. Messages are scanned through the normal worker with `rspamc`, with
   `--ip`, `--user` and `--from` set like Postfix's milter does. Each
   case compares the action, the message, the `DKIM_SIGNED` options and
   the algorithms of the `DKIM-Signature` headers.

The matrix covers: authenticated mail signed with every active key; a
missing, unreadable or absent key, and a served domain without keys,
answered with a soft reject (`DKIM signing unavailable, try again
later`); unauthenticated mail from Internet and pod addresses, with an
unserved or no `From`, left untouched and never deferred; authenticated mail
whose `From` header is missing, has no domain or an empty domain, or is
in an unserved domain or a subdomain of a served one, rejected
(`From must be an address in a served domain`, [ADR 0015](../decisions/0015-user-sources-directory-users-and-aliases.md));
loopback mail (Postfix's own non-SMTP mail) signed, deferred without a
key, and exempt when the envelope sender is empty; a message that is
rejected anyway stays a reject; a message with several `From` addresses,
one of them in a served domain (in one header or in two), is rejected
(`multiple From addresses cannot be DKIM signed`), also from loopback,
and left alone when there is no login, and from loopback when no address
is served or there is no `From` header; an error inside the guard defers
authenticated mail and loopback mail with a sender, leaves everything
else alone and is logged; an error in the source detection defers
authenticated mail only. `rspamadm configtest` is run for the good
configuration (exit 0, also with missing keys), a guard with a syntax
error and a guard that fails at load time (exit 1 each).

A run takes under ten seconds once the image is present, can be repeated
at will, and removes its containers and temporary files on exit.

## The e2e suite

The e2e suite (`test/e2e`) creates an isolated kind cluster and
deploys the operator (with
`--enable-insecure-test-features`), a cert-manager
self-signed `Issuer`, and one `MailServer` with the hostname
`mail.e2e.example.org` (its own `MailDomainGrant`, apart from the one
of the domain), a `MailDomain`, a `MailDirectory` (referenced only for existence in
Phase 2; users come from the static test Secret), and the Phase 2
static test users. The Mailpit sink (ADR 0004, decision 7) runs in
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
  is Dovecot with the static test users);
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
- a broken spec (missing `MailDirectory`) makes the `MailServer`
  `Ready=False` with `DirectoryNotFound` **without rolling the data
  plane** (the rendered-config hash of the pod template is unchanged
  and all workloads stay ready);
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
