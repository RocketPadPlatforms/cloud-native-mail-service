# Install and operate

:::note
Phase 2 (v1alpha1). This page covers the data-plane layout of a
`MailServer` and, in detail, the Dovecot component. The operator
deployment manifests live in `config/` (Kustomize); the Helm chart
arrives with the release (Phase 10).
:::

:::danger
**DKIM signing is not implemented yet** (Phase 5): outbound mail leaves
unsigned. Do not send production mail from a CNMS server before then.
:::

## Install

Prerequisites in the cluster: cert-manager (certificate issuing for
`spec.tls`), and the CNMS CRDs. Install:

```console
kubectl apply -k config/default
```

A `MailServer` then runs one data plane per server (reputation
isolation, see [resource model](../concepts/resource-model.md)):

| Component | Role | In-cluster ports |
|-----------|------|------------------|
| Postfix   | SMTP in/out, relay, submission | 25 (postscreen), 587 (submission), 9150 (metrics) |
| Rspamd    | filtering, DKIM, rate limits | 11332 (milter), 9151 (metrics) |
| Dovecot   | IMAP, LMTP delivery, SASL auth | 12345 (auth protocol), 24 (LMTP) and 143/993 (IMAP), the last two with mailboxes only |
| Valkey    | Rspamd state (one set per server) | 6379, 9121 (metrics) |

Ports 24 and 12345 (Postfix→Dovecot) and 6379 (Rspamd→Valkey) are
internal: each is reachable only from the pods of the same
`MailServer` (NetworkPolicies,
[ADR 0006](../decisions/0006-network-policy-and-security-floor.md)).
The Rspamd controller API (11334) answers loopback clients only; the
metrics port 9151 is a sidecar copy of its `/metrics`
([ADR 0005](../decisions/0005-component-metrics-sources.md)). The
Rspamd message history is disabled, so the WebUI history is empty
(the controller answers 500); it stores personal data in Valkey
without an expiry.

## Operator flags

| Flag | Default | Effect |
|------|---------|--------|
| `--domain-authorization` | `allowlist` | `allowlist` requires a `MailDomainGrant` covering every domain of a `MailServer` and naming its namespace ([domain authorization](../concepts/domain-authorization.md)); `unrestricted` skips the grant requirement for single-tenant clusters (the operator logs a warning at startup). Uniqueness is enforced in both modes. |
| `--allow-cluster-issuers` | `false` | Lets a `MailServer` reference a cert-manager `ClusterIssuer` in `spec.tls.issuerRef`. Without the flag such a server is fail-closed (`Ready=False, reason=ClusterIssuerNotAllowed`) and no `Certificate` is created; a namespaced `Issuer` is always allowed. A shared `ClusterIssuer` lets every tenant obtain certificates from it, so enable the flag only on clusters where that is intended. Add it to the manager `args` in `config/manager/manager.yaml` (Kustomize); the Helm chart will expose it as a value (Phase 10). |
| `--enable-insecure-test-features` | `false` | Accepts `spec.submission.insecureStaticUsers` (Phase 2 test users from a Secret). For tests only: the operator logs a warning at startup when it is set. Without the flag a `MailServer` that sets the field is fail-closed (`Ready=False, reason=InsecureStaticUsersDisabled`). |

## Status conditions

`kubectl get mailserver` shows a `Ready` rollup; the reasons explain
the first failing sub-condition:

| Condition | True when | Notable False reasons |
|-----------|-----------|----------------------|
| `DomainsAuthorized` | every `spec.domains` entry passes the grant check | `NotGranted`, `DuplicateDomain`, `DomainNotFound`, `PartiallyAuthorized` (only the authorized domains are rendered; the message names the blocked ones, which leave the data plane when the new configuration is rolled out; `Ready` stays `False`) |
| `CertificateReady` | the TLS Secret (`<name>-tls` or `spec.tls.secretName`) carries `tls.crt` and `tls.key` | `SecretNotFound`, `SecretIncomplete` |
| `ConfigValidated` | the rendered configuration of every component passed its validation Job (or already runs); `Unknown` while the Jobs run | `ConfigRejected` (message names the failed Job; delete the Job to retry), `ValidatingConfig` (Unknown) |
| `WorkloadsReady` | every data-plane workload finished its rollout (latest spec observed, all replicas on the desired template and ready) | `Progressing` while converging, `RolloutStalled` after a Deployment exceeds its progress deadline or the workloads stay not ready for 10 minutes (the message lists which) |
| `Observability` | the Prometheus Operator integration matches `spec.observability.prometheusOperator` (`auto` is the default) | `CRDsRequired` (setting `enabled` without the ServiceMonitor CRDs) |
| `ClientIPPreserved` | Postfix sees the real client address (postscreen and the per-client rate limits depend on it); informational, never part of `Ready` | `ClusterTrafficPolicy` (`inbound.externalTrafficPolicy: Cluster` on a LoadBalancer Service source-NATs clients, so the rate limits count all senders together; use `Local`) |
| `DedicatedSendingAddress` | outbound mail leaves from an address of its own (an IP pool claim) or through a relay; informational, never part of `Ready` | `NodeSourceAddress` (`outbound.direct` without `ipPoolClaimRef` sends from the node address, which other workloads share, so the sender reputation is shared; see ADR 0002) |
| `NodeDrainable` | the Postfix disruption budget allows evicting one pod at a time; informational, never part of `Ready` | `BudgetBlocksDrain` (`postfixMinAvailable` keeps every replica available, for example the default `1` with `postfix.replicas: 1`, so a node drain waits; add replicas or lower the budget) |
| `Ready` | all of the above (except `ClientIPPreserved`, `DedicatedSendingAddress` and `NodeDrainable`) | propagated reasons, e.g. `DirectoryNotFound`, `NoUserSource` (no `directoryRef` and no other user source), `StaticUsersInvalid`, `OwnershipConflict`, `RelayNotAvailable` (Phase 6), `IpPoolClaimNotAvailable` (Phase 7) |

The operator only changes objects it controls. If an object it needs
(for example a Service named `<server>-postfix`) already exists and has
no controller reference to the `MailServer`, nothing is overwritten and
the server reports `Ready=False, reason=OwnershipConflict` with a Warning
Event naming the object; rename or delete the foreign object. Likewise,
only ServiceMonitors that carry the CNMS labels and a controller
reference to the server are ever deleted.

A not-ready `MailServer` is re-checked every 30 seconds (certificates
get issued, workloads come up, CRDs get installed), a ready one every
10 minutes as a safety net behind the watches. Every change to a
`MailDomainGrant` re-evaluates all `MailServer`s, and a change to a
`MailDomain` re-evaluates the servers of every namespace that claims the
same domain, because uniqueness is checked cluster wide. A
`MailDomainGrant` that points at a missing namespace shows
`NamespaceExists=False, reason=NamespaceNotFound` (informational; it
never feeds back into authorization) and is re-checked when the
namespace appears or disappears.

Secrets are not watched, and the operator caches no Secret at all: the
TLS Secret and the test users Secret are read by name on every
reconcile, so no informer holds the Secrets of the cluster in memory
and the operator needs no `watch` on them. A TLS Secret that appears
late is picked up by the 30-second re-check of the not-ready server. A
certificate renewal needs no reconcile: the reload sidecars see the
renewed files. A change of the test users Secret takes effect at the
next re-check (at most 10 minutes), or sooner when the `MailServer`
changes. The operator's own ConfigMaps and Jobs are cached by label
only; ConfigMaps and Jobs of tenants never enter the cache.

## Dovecot

Dovecot runs as a single-replica StatefulSet on the official
`dovecot/dovecot:2.4.5` image ([ADR 0004](../decisions/0004-data-plane-images-and-build-strategy.md)).
The operator renders a **self-contained** `dovecot.conf` into
`/etc/dovecot`; it shadows the image's shipped configuration tree, so
the listener set is exactly the rendered one — nothing extra is
started.

### Modes

- **Mailboxes enabled** (`spec.mailboxes.enabled: true`): IMAP
  (143/993), LMTP delivery from Postfix, and SASL auth. Mail is stored
  on the RWO volume below.
- **Mailboxes disabled, submission enabled**: Dovecot runs in
  **auth-only mode** (no IMAP, no LMTP listener, no mail volume). It
  serves only the SASL backend for Postfix submission; Postfix does not
  LMTP-deliver to it, and the Service, the NetworkPolicies and the
  probes know the auth port only.

### TLS and listeners

- Global `ssl = required` with `ssl_min_protocol = TLSv1.2`
  (floor of ADR 0006).
- **993 (imaps): implicit TLS.**
- **143 (imap):** plaintext sessions are possible, but `AUTH` is only
  offered after `STARTTLS` — credentials never travel unencrypted.
- **24 (LMTP) and 12345 (auth protocol):** plain TCP by design; both are
  reachable only from the Postfix pods of the same `MailServer` via the
  NetworkPolicies.
- **Listen ports:** the Service ports 24, 143 and 993 map to the
  container ports 31024, 31143 and 31993. The image runs as the
  non-root `vmail` user, which can only bind a port below 1024 where
  the node lowers `net.ipv4.ip_unprivileged_port_start`; an added
  `NET_BIND_SERVICE` capability does not help a non-root process
  (Kubernetes cannot grant ambient capabilities; verified with
  `dovecot/dovecot:2.4.5`). NetworkPolicies and probes use the
  container ports.
- The certificate comes from the `spec.tls` cert-manager Issuer
  (Secret default `<name>-tls`) and is mounted at `/etc/dovecot/ssl`.
  Certificate renewals trigger a **reload**, not a pod restart.
- **Before the certificate exists:** Dovecot 2.4.5 does not start
  without its certificate files (fatal at startup, whatever `ssl` is),
  and it cannot serve without TLS. The pod therefore waits in the
  `wait-tls` init container (`Init:0/1`, see `kubectl logs -c
  wait-tls`) until cert-manager has issued the Secret, instead of
  crash-looping. Postfix behaves differently: it starts, its smtpd
  logs `disabling TLS support`, port 25 accepts plain-text mail, and
  submission offers no `AUTH` (`530 5.7.0 Must issue a STARTTLS command
  first`) until the cert-reload sidecar reloads the certificate once
  the Secret appears (within about 30 seconds). Both behaviours were
  checked with the pinned images.

### Volumes and ownership

| Mount | Source | Notes |
|-------|--------|-------|
| `/etc/dovecot` | rendered immutable ConfigMap | self-contained `dovecot.conf` |
| `/etc/dovecot-users` | rendered immutable Secret | the `users` passwd-file (password hashes) |
| `/srv/mail` | RWO PVC (`spec.mailboxes.storage`) | mail storage; must be writable by vmail (uid/gid 1000), enforced by the manifest (fsGroup) |
| `/var/run/dovecot` | `emptyDir` | runtime sockets/state; lost on pod restart (rebuildable) |

Mail is stored as **maildir** under `/srv/mail/<username>/mail`.
Maildir is crash-consistent and safe for file-level backup (k8up);
indexes are rebuildable. The mailbox-format decision is formalized in
the Phase 4 ADR.

### Test users (Phase 2)

Users come from `spec.submission.insecureStaticUsers` (accepted only
with the operator flag `--enable-insecure-test-features`); Phase 3
replaces them with the `MailDirectory`. The referenced Secret carries
the key `users.yaml`:

```yaml
# key: users.yaml
users:
  - username: testuser
    passwordHash: '{SHA512-CRYPT}...' # crypt(3) hash; never plaintext
    mailbox: testuser@example.com     # full address
```

Usernames and mailboxes are lower-cased (Dovecot lower-cases logins) and
must be unique when compared case-insensitively; a duplicate is rejected
and the server stays on its previous configuration. A username consists
of `[a-z0-9._-]` after lower-casing (it is a path component of the home
directory): it must not start with a dot or contain `..`, `/` or a colon.
No field may contain a colon (the passwd-file separator) or a control
character. Plaintext password schemes (`{PLAIN}`, `{CLEAR}`,
`{CLEARTEXT}`) and the unsalted `{PLAIN-*}` schemes (`{PLAIN-MD5}`,
`{PLAIN-TRUNC}`, ...) are rejected, whatever their case; use a salted
scheme such as `{SHA512-CRYPT}` or `{ARGON2ID}`.

The mailbox addresses also form Postfix's recipient table
(`virtual_mailbox_maps`): with mailboxes enabled, port 25 rejects an
unknown recipient of the server's domains at SMTP time (`550 5.1.1`)
instead of accepting it and bouncing later, and a server without
users rejects every recipient. Phase 3 replaces the table with the
directory lookup.

Each user is usable with **both** the full address
(`testuser@example.com`) and the bare username (`testuser`) for IMAP
login; delivery (LMTP) is keyed by the full address (Dovecot 2.4 looks
users up by the full recipient address, with no local-part fallback).

### Validation and reload

- Before rollout, the rendered configuration is validated with
  `doveconf -n` in a validation Job (unknown settings are fatal, so
  the gate is strict; [ADR 0009](../decisions/0009-config-validation-gate.md));
  a bad config never replaces a working one.
- Running configuration changes and certificate renewals apply via
  `doveadm reload` (verified to work without `doveadm_password`), not
  by restarting the pod.

### Verifying a running server

```console
# IMAP over implicit TLS (login with the full address or the username)
kubectl port-forward svc/mail-dovecot -n <ns> 993:993

# SASL backend sanity check (inside the pod)
kubectl exec -n <ns> mail-dovecot-0 -- /dovecot/bin/doveadm auth test testuser '<password>'
```

## Failure behavior

- **Dovecot down (mailboxes enabled):** Postfix receives a temporary
  failure (4xx) from the LMTP hop and keeps the message in its queue —
  mail is not lost and not bounced (principle 5).
- **Dovecot down (submission):** `AUTH` on 587 is unavailable until the
  SASL backend responds; accepted mail already in the queue is not
  affected.
- **Auth-only mode:** there is no IMAP to fail; only submission auth is
  impacted.
