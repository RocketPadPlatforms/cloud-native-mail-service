# Testing

CNMS tests run in four layers, each with a different level of
realism:

| Layer | Tool | Covers |
|-------|------|--------|
| Unit | `go test` | Renderers (golden files), parsers, the domain-authorization evaluator |
| Controller | envtest (`make test`) | Reconciliation: gates, fail-closed blocking, conditions, data-plane apply, optional-integration discovery; every file below `config/samples` (including `full-setup/`) is applied to the API server with strict field validation, and the docs embed those files, so examples cannot drift from the CRDs |
| Guard matrix | Docker + `rspamc` (`make test-rspamd-guard`) | The never-unsigned guard and DKIM signing in the pinned Rspamd image, with the configuration the renderer produces |
| E2E | kind + swaks + Mailpit (`make test-e2e`) | The full data plane in a cluster: mail actually flows through Postfix, Rspamd and Dovecot |

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
later`); unauthenticated mail from Internet and pod addresses, mail of an
unserved domain and of a subdomain left untouched and never deferred;
loopback mail (Postfix's own non-SMTP mail) signed, deferred without a
key, and exempt when the envelope sender is empty; a message that is
rejected anyway stays a reject; a message with several `From` addresses,
one of them in a served domain (in one header or in two), is rejected
(`multiple From addresses cannot be DKIM signed`), also from loopback,
and left alone when no address is served, when there is no login, and
when there is no `From` header; an error inside the guard defers
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
self-signed `Issuer`, and one `MailServer` with a `MailDomainGrant`,
a `MailDomain`, a `MailDirectory` (referenced only for existence in
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
