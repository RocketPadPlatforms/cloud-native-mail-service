# Data-plane images

The [Go dependency report](third-party-licenses.md) covers the Go
packages of the operator and its tests. The data-plane software runs as
separate programs in container images and is not linked into the
operator. Licences were
checked against the upstream licence files of the pinned versions. The
versions are in `internal/images/images.go` and are not repeated here,
so version bumps do not change this page. It is maintained by hand and
re-checked when an image major version changes (project plan, section 9).

| Component | Licence | Image |
|---|---|---|
| Postfix | EPL-2.0 or IPL-1.0 (dual, recipient's choice) | Built by CNMS from the upstream source tarball (`images/postfix`) |
| libpq (PostgreSQL client library) | PostgreSQL License ([licence](https://www.postgresql.org/about/licence/)) | Linked dynamically by the Postfix `pgsql` map type in the CNMS Postfix image (Debian `libpq5`) |
| ca-certificates (system root bundle) | MPL-2.0 for the Mozilla root list ([licence](https://www.mozilla.org/MPL/2.0/)), GPL-2+ for the Debian scripts; the licence text is `/usr/share/doc/ca-certificates/copyright` in the image | Debian package in the CNMS Postfix image, so libldap and libpq verify a directory with no `caSecretRef` against public roots (the Dovecot image has it already) |
| Rspamd | Apache-2.0 | Upstream `rspamd/rspamd` |
| Dovecot | MIT (`src/lib`, `src/auth`, `src/lib-sql`), LGPL-2.1 (the rest) | Upstream `dovecot/dovecot` |
| Valkey | BSD-3-Clause (Valkey contributors, plus BSD-3-Clause code from Redis) | Upstream `valkey/valkey` |
| redis_exporter | MIT | Upstream `oliver006/redis_exporter` |
| Mailpit (e2e sink only) | MIT | Upstream `axllent/mailpit` |
| CoreDNS (e2e test DNS server only) | Apache-2.0 | Upstream `coredns/coredns` |
| BusyBox | GPL-2.0-only | Installed in the CNMS Postfix image (queue-metrics and Rspamd metrics sidecars) |
| Debian base packages | Mixed, including GPL and LGPL | Base of the CNMS Postfix image and of the upstream Rspamd, Dovecot and Valkey images |

## Copyleft components

BusyBox and some Debian base packages are GPL. CNMS runs them
unmodified as separate programs inside images and never links them
into the operator binary, which the licence policy allows (ADR 0001,
ADR 0005). The Debian package sources are available from the Debian
archive for the package versions in the image (`apt-get source
busybox`); the licence text of every installed Debian package is in
`/usr/share/doc/<package>/copyright` inside the image.

The operator image contains only Go modules and is built from
`gcr.io/distroless/static`.
