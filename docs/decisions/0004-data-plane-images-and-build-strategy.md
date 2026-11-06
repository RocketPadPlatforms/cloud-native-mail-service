# ADR 0004: Data-plane images and build strategy

- **Status:** Accepted, partially amended by ADR 0015
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
   official 3.11.7 source tarball, downloaded from a mirror of the
   official release directory (`postfix-mirror.horus-it.com`), with
   Wietse Venema's own mirror (`ftp.porcupine.org`, plain HTTP) as a
   fallback; downloads are retried. Both serve byte-identical files
   (same SHA512 for tarball and signature), and the content is
   authenticated by the pin and the signature, not by the transport.
   The version and the SHA512 are build arguments, and the build also
   verifies Wietse Venema's GPG signature of the tarball against the
   key in `images/postfix/keys`. The base is `debian:trixie-slim`
   (pinned by digest). Postfix 3.11 has no configure script: the
   Dockerfile runs `make makefiles` with `CCARGS` of
   `-DUSE_TLS -DUSE_SASL_AUTH -DDEF_SERVER_SASL_TYPE="dovecot"
   -DHAS_LMDB -DHAS_SQLITE -DHAS_LDAP -DNO_DB -DNO_NIS -DNO_NISPLUS`
   (SASL for the Dovecot auth backend on the submission listener, with
   the dovecot type built in and no external SASL library; LDAP and
   SQLite for the Phase 3 directory lookups; LMDB as the default map
   store and no Berkeley DB) and installs the client binaries to
   `/usr/sbin`. The entrypoint runs the
   master as root; the master drops privileges to the `postfix` user
   for all daemons, and the queue lives on a per-replica volume. The
   root FS is read-only; the data directory (`/var/lib/postfix`) is an
   `emptyDir` (ADR 0006 lists the capabilities the root master keeps).
   Logging goes through postlog with
   `maillog_file = /dev/stdout`; the master must run with `-s`
   (`postfix start-fg`) to keep stdout open — a plain `postfix start`
   runs `master -w`, which discards the master's stdout to `/dev/null`
   and the daemon logs would be lost. The e2e suite asserts daemon
   logs are visible via `kubectl logs`. The same image is reused for
   the config-validation Job (ADR 0009) and the Phase 9 queue-rescue
   job.
2. **Submission architecture:** Postfix serves port 587
   (`smtpd_sasl_type = dovecot`); Dovecot is the SASL backend,
   consistent with "auth-only Dovecot for submission" and with the
   LMTP flow (Postfix delivers to Dovecot, never the other way around).
   The auth channel crosses the pod boundary as plain TCP:
   `smtpd_sasl_path = inet:<dovecot-service>:12345`. Verified against
   the 3.11.7 source: the dovecot SASL client connects to `inet:`
   paths without TLS. The Dovecot listener must be named `auth` to
   serve the auth-client protocol the client speaks; the names
   `auth-master` and `auth-userdb` serve other protocols. The same
   container serves the LMTP listener (Service port 24) used by
   `virtual_transport`, only when mailboxes are enabled: in auth-only
   mode Dovecot runs without any protocol and has no LMTP listener.
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
   Metrics: a loopback sidecar copies the controller worker's
   `GET /metrics` and serves it (the sidecar runs the Postfix image
   for `busybox`); see ADR-0005. The message history is disabled.
5. **Valkey image:** official `valkey/valkey:9.1.2`. The rendered
   config is mounted at `/etc/valkey/valkey.conf`; the container
   args are just this path (the entrypoint hands it to
   `valkey-server`). Verified against the image: the entrypoint
   drops root to the valkey user (uid/gid 999) and chowns `/data`
   (WORKDIR) to that user, so both the `emptyDir` and the PVC get
   correct ownership. Persistence: RWO PVC when enabled in `spec`,
   `emptyDir` otherwise. 9.1.2 has no config-check flag, so the
   validation gate briefly starts the server and pings it.
6. **Pin handling:** every engine/test image reference is a Go
   constant (version tag + digest) in one package, consumed by the
   renderers and the e2e suite; a Renovate custom manager updates them.
   Base images of CNMS-built Dockerfiles are pinned by digest
   (Renovate dockerfile manager). The CNMS-built images are fully
   qualified under the project's registry namespace
   (`ghcr.io/rocketpadplatforms/cnms-postfix`, `cnms-e2e-client`): a
   short name such as `cnms/postfix` would resolve to a Docker Hub
   namespace CNMS does not control. Their tags follow `POSTFIX_VERSION`
   (a Renovate custom manager keeps the Dockerfile, the Makefile and
   the constants in step; the checksum of the tarball is updated by
   hand in the same PR). The constants carry the version tag only:
   the e2e suite rebuilds both images locally, and the digest pin
   follows once published images exist (backlog). (Amended by ADR 0015:
   from Phase 3a the tags are `<POSTFIX_VERSION>-<revision>`, for the
   Postfix and the e2e client image, so that a change of an image's
   content under the same Postfix version gets a new tag.)
7. **E2E-only images:** a swaks client image
   (`debian:trixie-slim` + `swaks` + `openssl` + `curl`, apt swaks
   `20201014.0` upstream), `axllent/mailpit:v1.32.0` (MIT; the
   sink runs with `--smtp [::]:25`, API/UI on 8025) and
   `curlimages/curl`, which scrapes the operator metrics. Test-only,
   pinned by version and digest in the images package, not part of
   the product.
   The sink must listen on port 25: the kind cluster's NetworkPolicy
   enforcement (kindnet, nftables `queue` to a userspace evaluator)
   evaluates Service traffic on the post-DNAT tuple, so the Postfix
   pod's port-25 egress allow (`mail-egress-smtp`) must match the
   delivery path's target port; a 25→1025 Service would leave the
   flow at dport 1025 and be dropped.
8. **Multi-arch:** the `images` workflow builds the operator, Postfix
   and e2e client images for linux/amd64 and linux/arm64. Pull requests
   only build; pushes to main and version tags publish them to
   `ghcr.io/rocketpadplatforms` (`cnms-operator`, `cnms-postfix`,
   `cnms-e2e-client`) with an SBOM and a provenance attestation. The
   Postfix and e2e client images are also tagged with the Postfix
   version. The e2e suite builds the images locally for the host
   architecture (amd64 in CI). The Makefile `docker-buildx` target
   builds the operator for further platforms. The Postfix image is
   compiled under emulation for arm64, so that build is slow.

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
