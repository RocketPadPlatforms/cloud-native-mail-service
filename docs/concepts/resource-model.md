# Resource model

CNMS v1alpha1 (API group `cnms.rpp.gmbh`) currently defines three resources.
They are all namespace-scoped: a tenant's mail system lives in one namespace,
and every reference between CNMS resources stays inside that namespace
(name-only references, no cross-namespace RBAC).

The full API reference is [generated from the Go types](../reference/api.md)
(`make api-ref`). Design decisions are recorded in
[ADR 0002](../decisions/0002-v1alpha1-api-design.md).

## The three resources

### `MailServer`

A tenant-facing mail system: inbound (port 25) and submission (port 587)
services, outbound delivery, and mailboxes. One `MailServer` is fully isolated
from any other: own workloads and queues, own sending addresses, own DKIM
keys, own Rspamd and Valkey instances. Nothing is shared between
`MailServer`s, so their sender reputation stays independent.

Key spec fields:

- `directoryRef` — the `MailDirectory` used for recipient validation and
  authentication.
- `outbound` — exactly one of `direct` (own sending addresses, optionally via
  a `MailIPPoolClaim`) or `relay` (a smart host; the `MailRelay` resource
  arrives with the relay phase).
- `domains` — the `MailDomain`s this server handles: they are signed with
  DKIM and accepted for submission.
- `tls` — the cert-manager issuer the server certificate comes from.
- `submission`, `mailboxes` — enable submission (on by default) and Dovecot
  mail storage (off by default). With submission on and mailboxes off,
  Dovecot runs in auth-only mode.
- `postfix`, `rspamd`, `valkey` — workload tuning (replicas, queue storage,
  Valkey persistence) with defaults that match the HA requirements.
- `availability`, `observability`, `backup` — PDB/topology spread,
  Prometheus Operator integration (`auto`/`enabled`/`disabled`), and k8up
  backup annotations.

### `MailDomain`

Owns the public DNS records of one domain: DKIM (selector + key rotation with
overlap), SPF, DMARC (DMARC1 / RFC 9989-9991 semantics for the records CNMS
publishes), MTA-STS, and TLS-RPT.

- `dnsMode` is `manual` (default) or `externalDNS`. In manual mode the status
  lists every required record and whether a lookup verified it; the admin
  creates the records with the hoster. In `externalDNS` mode the operator
  creates `DNSEndpoint` resources.
- DKIM rotation policy (`interval`, `overlap`) is spec; the rotation schedule
  progress is recorded in the `cnms.rpp.gmbh/rotation` annotation so it
  survives a restore without status.
- `status.records` is the contract for DNS verification: every required
  record with name, type, expected value, and `verified` state.

### `MailDirectory`

Points at the external user directory (accounts are managed outside CNMS):
`ldap` or `sql` (PostgreSQL), credentials via Secret references. It backs
Postfix recipient validation (unknown recipients tempfail when the directory
is down) and Dovecot authentication (submission and IMAP). `status` carries a
`Ready` condition for reachability.

## How they fit together

```
MailServer ──references──> MailDirectory   (auth + recipient validation)
MailServer ──references──> MailDomain      (DKIM signing, submission domains)
MailDomain ─── publishes ──> DNS records   (DKIM/SPF/DMARC/MTA-STS/TLS-RPT)
```

`MailServer` references its domains; `MailDomain` is independent and can in
principle be used by several `MailServer`s. The controller (Phase 2 onward)
validates those references at reconcile time; the API server enforces only the
per-object rules (schema, defaults, CEL).

## What is not here yet

Resources for the later phases are added in the phase that implements them,
so their fields are designed with implementation feedback:

- `MailRelay` (phase 6), `MailIPPoolClass` / `MailIPPool` /
  `MailIPPoolClaim` (phase 7), `InboundRoute` (phase 8), `MailPolicy` (phase
  9).

State that cannot be recomputed from the world (bindings, rotation schedule
progress) lives in spec or annotations, never only in status: custom resources
are restored from GitOps, which contains no status at all.
