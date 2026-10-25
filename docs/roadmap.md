# Roadmap

The project is built in phases (see the phase plan in `AGENTS.md`, section 5).
At the end of every phase: tests green, docs updated, summary with assumptions
and open risks, review before the next phase starts.

Status as of 2026-10-25:

| Phase | Scope | Status |
|---|---|---|
| 0 | Scaffold: repo, kubebuilder init, CI, lint, license check, docs site, ADRs | In progress |
| 1 | API (core subset): `MailServer`, `MailDomain`, `MailDirectory` CRDs with defaults, CEL validation, generated CRDs and API reference, samples | Planned |
| 2 | `MailServer` core: Postfix + Rspamd + Valkey rendering, workloads, config validation, golden-file tests, metrics, swaks e2e | Planned |
| 3 | `MailDirectory`: LDAP and SQL maps for Postfix and Dovecot auth | Planned |
| 4 | Mailboxes (Dovecot): IMAP, LMTP delivery, mail storage, quota, backup hooks | Planned |
| 5 | `MailDomain`: DKIM keys and rotation, SPF/DMARC/MTA-STS/TLS-RPT records, DNS verification | Planned |
| 6 | Relay mode: `MailRelay`, per-domain routing, fallback relay | Planned |
| 7 | IP management: `MailIPPoolClass`/`MailIPPool`/`MailIPPoolClaim`, binding, quarantine, egress policies, manual rDNS | Planned |
| 8 | `InboundRoute`: webhook and LMTP delivery with retry/dead-letter | Planned |
| 9 | Hardening: `MailPolicy`, full metrics, alerts, backup/restore and chaos e2e | Planned |
| 10 | Docs and release: complete user docs, Helm chart, release automation | Planned |
