# Install and operate

:::note
Phase 2 (v1alpha1). This page covers the data-plane layout of a
`MailServer` and, in detail, the Dovecot component. The operator
deployment manifests live in `config/` (Kustomize); the Helm chart
arrives with the release (Phase 10).
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
| Rspamd    | filtering, DKIM, rate limits | 11332 (milter), 11334 (API/metrics) |
| Dovecot   | IMAP, LMTP delivery, SASL auth | 24 (LMTP), 4190 (auth protocol), 143/993 (IMAP, mailboxes only) |
| Valkey    | Rspamd state (one set per server) | 6379, 9121 (metrics) |

Ports 24 and 4190 (Postfix→Dovecot) and 6379 (Rspamd→Valkey) are
internal: each is reachable only from the pods of the same
`MailServer` (NetworkPolicies,
[ADR 0006](../decisions/0006-network-policy-and-security-floor.md)).

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
  **auth-only mode** (no IMAP, no mail volume). It serves only the SASL
  backend for Postfix submission; Postfix does not LMTP-deliver to it.

### TLS and listeners

- Global `ssl = required` with `ssl_min_protocol = TLSv1.2`
  (floor of ADR 0006).
- **993 (imaps): implicit TLS.**
- **143 (imap):** plaintext sessions are possible, but `AUTH` is only
  offered after `STARTTLS` — credentials never travel unencrypted.
- **24 (LMTP) and 4190 (auth protocol):** plain TCP by design; both are
  reachable only from the Postfix pods of the same `MailServer` via the
  NetworkPolicies.
- The certificate comes from the `spec.tls` cert-manager Issuer
  (Secret default `<name>-tls`) and is mounted at `/etc/dovecot/ssl`.
  Certificate renewals trigger a **reload**, not a pod restart.

### Volumes and ownership

| Mount | Source | Notes |
|-------|--------|-------|
| `/etc/dovecot` | rendered ConfigMap | self-contained `dovecot.conf` + `users` |
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

Each user is usable with **both** the full address
(`testuser@example.com`) and the bare username (`testuser`) for IMAP
login; delivery (LMTP) is keyed by the full address (Dovecot 2.4 looks
users up by the full recipient address, with no local-part fallback).

### Validation and reload

- Before rollout, the rendered configuration is validated with
  `doveconf -n` in an init container (unknown settings are fatal, so
  the gate is strict); a bad config never replaces a working one.
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
