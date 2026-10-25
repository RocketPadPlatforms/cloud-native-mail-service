# ADR 0004: Data-plane images and build strategy

- **Status:** Accepted
- **Date:** 2026-10-25

## Context

ADR-0001 pins the engine versions (Postfix 3.11.7, Rspamd 4.2.3,
Dovecot 2.4.5, Valkey 9.1.2) and defers the image strategy to Phase 2.
The data plane runs four workloads per `MailServer`; each needs a
container image with a known user, writable paths, and a log path
(`stdout`). Facts verified against upstream on 2026-10-25:

- Postfix 3.11.7: no official upstream image. Debian trixie (13)
  currently ships Postfix 3.10.13 (`3.10.13-0+deb13u1` in the trixie
  pool); `3.11.7-1` is still in unstable. Using the distro package
  would silently downgrade below the ADR-0001 pin.
- Dovecot: official CE images exist at `dovecot/dovecot` (published by
  the Dovecot project, hardened rootless image, aarch64 + amd64). The
  `2.4.5` tag is published; config is mounted at `/etc/dovecot`, mail
  at `/srv/mail`.
- Rspamd: official image `rspamd/rspamd` with a `4.2.3` tag.
- Valkey: official image `valkey/valkey` with a `9.1.2` tag.

## Decision

1. **Postfix image, built by CNMS** (`images/postfix/Dockerfile`):
   official 3.11.7 source tarball from ftp.postfix.org, SHA512 pinned
   as a build argument, built on `debian:trixie-slim` (base pinned by
   digest). Configure flags: `--with-tls --with-sasl --with-lmdb
   --with-ldap --with-sqlite` (SASL for the Dovecot auth backend on the
   submission listener; LDAP and SQLite for the Phase 3 directory
   lookups; LMDB as the default map store). The image runs as the
   non-root `postfix` user, keeps the root FS read-only with the spool
   on an `emptyDir`, and logs via postlog with
   `maillog_file = /dev/stdout` (the master process inherits the pod's
   stdout; e2e asserts logs are visible via `kubectl logs`). The same
   image is reused for the config-validation init container and the
   Phase 9 queue-rescue job.
2. **Submission architecture:** Postfix serves port 587
   (`smtpd_sasl_type = dovecot`); Dovecot is the SASL backend,
   consistent with "auth-only Dovecot for submission" and with the
   LMTP flow (Postfix delivers to Dovecot, never the other way around).
   The auth channel crosses the pod boundary as plain TCP:
   `smtpd_sasl_path = inet:<dovecot-service>:4190`. Verified against
   the 3.11.7 source: the dovecot SASL client connects to `inet:`
   paths without TLS. The Dovecot listener must be named `auth` to
   serve the auth-client protocol the client speaks; the names
   `auth-master` and `auth-userdb` serve other protocols. The same
   container serves the LMTP listener (port 24) used by
   `virtual_transport`.
3. **Dovecot image:** official `dovecot/dovecot:2.4.5` (rootless,
   hardened). Rendered config is mounted at `/etc/dovecot`; mail
   storage (when enabled) is an RWO volume mounted at `/srv/mail`,
   writable by the vmail account (uid/gid 1000; the manifest
   enforces this, e.g. via fsGroup);
   runtime directories (`/var/run/dovecot`, auth sockets) live on
   `emptyDir` as documented exceptions to the read-only root FS.
   Rendering targets Dovecot 2.4 syntax only (2.3 incompatibilities
   per ADR-0001).
4. **Rspamd image:** official `rspamd/rspamd:4.2.3`. Rendered config
   is mounted at `/etc/rspamd`; module state (learned data) is kept in
   the per-`MailServer` Valkey plus on-disk files on `emptyDir`
   (documented exception; restarts reset on-disk module state only,
   never shared state). Writable paths and the run user are verified
   against the running image and recorded in the renderer commit.
   Metrics: the controller worker serves `GET /metrics` (OpenMetrics);
   see ADR-0005.
5. **Valkey image:** official `valkey/valkey:9.1.2`. Persistence: RWO
   PVC when enabled in `spec`, `emptyDir` otherwise.
6. **Pin handling:** every engine/test image reference is a Go
   constant (version tag + digest) in one package, consumed by the
   renderers and the e2e suite; a Renovate regex manager updates them.
   Base images of CNMS-built Dockerfiles are pinned by digest
   (Renovate dockerfile manager).
7. **E2E-only images:** a swaks client image
   (`debian:trixie-slim` + `swaks` + `openssl` + `curl`, apt swaks
   `20201014.0` upstream) and `axllent/mailpit:v1.32.0` (MIT; SMTP on
   1025, API/UI on 8025). Test-only, version-pinned in the e2e suite,
   not part of the product.
8. **Multi-arch:** CNMS-built images are built for the local/CI
   architecture; e2e runs amd64. A multi-arch publish pipeline is a
   Phase 10 concern (Makefile `docker-buildx` support already exists).

## Consequences

- Postfix provenance is upstream tarball + SHA512; a Renovate bump of
  the Postfix version must update the pinned hash in the same PR.
- The Rspamd 4.x line introduced a new scan protocol and configuration
  system in 4.0. All Rspamd rendering is verified against the 4.2
  documentation before the Rspamd renderer commit lands (principle 8);
  assumptions are recorded there.
- Valkey 9.x compatibility: the Valkey 9 behavior changes (auth order,
  command validation) are exercised by the e2e Rspamd↔Valkey path; if
  the redis module misbehaves, the Valkey pin may be moved to 8.1.x in
  a follow-up ADR.
- `maillog_file = /dev/stdout`, the Dovecot rootless layout, and the
  Rspamd writable paths are each covered by an e2e assertion; a
  failure there means the image layout changed upstream, not a bug in
  the operator.
- The submission auth channel (TCP vs. shared volume) is an internal
  wiring detail; the public API does not expose it.
