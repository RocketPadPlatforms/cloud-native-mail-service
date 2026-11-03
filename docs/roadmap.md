# Roadmap

The project is built in phases (see the phase plan in `AGENTS.md`, section 5).
At the end of every phase: tests green, docs updated, summary with assumptions
and open risks, review before the next phase starts.

Status as of 2026-11-03:

| Phase | Scope | Status |
|---|---|---|
| 0 | Scaffold: repo, kubebuilder init, CI, lint, license check, docs site, ADRs | Done |
| 1 | API (core subset): `MailServer`, `MailDomain`, `MailDirectory` CRDs with defaults, CEL validation, generated CRDs and API reference, samples | Done |
| 2 | `MailServer` core: Postfix + Rspamd + Valkey rendering, workloads, config validation, golden-file tests, metrics, swaks e2e; plus domain-authorization gating of `spec.domains` (`MailDomainGrant`, `--domain-authorization` flag), default-deny NetworkPolicy floor, security floor (AUTH only over TLS, TLS >= 1.2, rate limits, `reject_unauth_destination`) with an open-relay e2e test | Done |
| 3 | User sources and service accounts: `MailDirectory` (LDAP and SQL maps for Postfix and Dovecot auth), operator-managed `MailUser` accounts for environments without a directory service, `MailServiceAccount` application credentials (cross-namespace reference, operator-provided SMTP credential Secret, sender binding, including sender login maps on submission) — ADR 0008 | Planned |
| 4 | Mailboxes (Dovecot): IMAP, LMTP delivery, mail storage, quota, backup hooks | Planned |
| 5 | `MailDomain`: DKIM keys and rotation, SPF/DMARC/MTA-STS/TLS-RPT records, DNS verification; plus domain uniqueness and derived-name conflicts, public-suffix rejection, revocation semantics (grace period, kept DKIM keys), apex TXT conflict detection, dedicated external-dns wiring; the never-unsigned Rspamd guard ([ADR 0013](decisions/0013-never-unsigned-guard.md)) | In progress |
| 6 | Relay mode: `MailRelay`, per-domain routing, fallback relay | Planned |
| 7 | IP management: `MailIPPoolClass`/`MailIPPool`/`MailIPPoolClaim`, binding, quarantine, egress policies, manual rDNS | Planned |
| 8 | `InboundRoute`: webhook and LMTP delivery with retry/dead-letter | Planned |
| 9 | Hardening: `MailPolicy`, full metrics, alerts, backup/restore and chaos e2e; plus the Postfix queue move for scale-down, queue-rescue and PVC resize (ADR 0010) | Planned |
| 10 | Docs and release: complete user docs, Helm chart, release automation | Planned |

## What is implemented today

The table gives the phase a feature belongs to; this list says what the
operator does now, including parts of later phases that landed early and
parts of finished phases that are still open. The truth for any item is
the code and the [status conditions](reference/status-conditions.md).

- **Reconciled resources:** `MailServer`, `MailDomain` and `MailDomainGrant`.
  `MailDirectory` is API only: the `MailServer` controller reads it, no
  controller writes its status.
- **Data plane (Phase 2):** Postfix, Rspamd, Valkey and Dovecot with
  validation Jobs and immutable, hash-named configuration, per-component
  disruption budgets and topology spread, resource requests and limits,
  default-deny NetworkPolicies, k8up annotations on the volumes, and
  ServiceMonitors when the Prometheus Operator CRDs exist.
- **Users (Phase 3):** only the static test users
  (`--enable-insecure-test-features`) and a `directoryRef` existence
  check. No directory lookups, `MailUser`, `MailServiceAccount` or
  sender binding.
- **Mailboxes (Phase 4):** IMAP, LMTP delivery and the maildir volume
  work with the data plane. Open: quota, Dovecot metrics, the mailbox
  format ADR, and the k8up backup and restore e2e.
- **Domains (Phase 5):** the authorization of `spec.domains` is
  enforced on the `MailServer`. The `MailDomain` controller authorizes
  the domain (grants, duplicates, public suffixes), generates and
  rotates DKIM keys or reads your own, lists and verifies every DNS
  record, reports the MTA-STS policy with the hosts of the `MailServer`s
  that serve the domain, and publishes a `DNSEndpoint` in `externalDNS`
  mode ([how-to](how-to/dkim-keys-and-dns-records.mdx)). The `MailServer`
  hands the active keys to Rspamd, which signs with them (RSA, and Ed25519
  when enabled), and a guard defers the outbound mail of a served domain
  that has no usable key instead of sending it unsigned
  ([concepts](concepts/dkim-signing.md)). Open: the guard's Lua test
  matrix in CI and the end-to-end tests (signature at the sink, a
  corrupted key defers, rotation with both selectors valid, restore);
  derived-name conflicts are not detected.
- **Relay and IP management (Phases 6 and 7):** the API fields exist;
  a server that sets `outbound.relay` or `ipPoolClaimRef` is not ready
  (`RelayNotAvailable`, `IpPoolClaimNotAvailable`). Direct outbound
  sends from the node address.
- **Hardening (Phase 9):** the queue and Rspamd metrics sidecars and the
  Valkey exporter exist; there are no alert rules, dashboards, `MailPolicy`
  or chaos e2e. Postfix scale-down and PVC resize are refused by the API
  (ADR 0010).
- **Docs and release (Phase 10):** no Helm chart and no release workflow
  yet; the operator installs with Kustomize (`config/default`).
