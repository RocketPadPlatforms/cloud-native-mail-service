# Data-plane images

The [Go dependency report](third-party-licenses.md) covers the operator
binary only. The data-plane software runs as separate programs in
container images and is not linked into the operator. Licences were
checked against the upstream licence files of the pinned versions
(`internal/images/images.go`); this page is maintained by hand and
re-checked when an image major version changes (project plan, section 9).

| Component | Version | Licence | Image |
|---|---|---|---|
| Postfix | 3.11.7 | EPL-2.0 or IPL-1.0 (dual, recipient's choice) | Built by CNMS from the upstream source tarball (`images/postfix`) |
| Rspamd | 4.2.3 | Apache-2.0 | Upstream `rspamd/rspamd` |
| Dovecot | 2.4.5 | MIT (`src/lib`, `src/auth`, `src/lib-sql`), LGPL-2.1 (the rest) | Upstream `dovecot/dovecot` |
| Valkey | 9.1.2 | BSD-3-Clause (Valkey contributors, plus BSD-3-Clause code from Redis) | Upstream `valkey/valkey` |
| redis_exporter | v1.96.0 | MIT | Upstream `oliver006/redis_exporter` |
| Mailpit (e2e sink only) | v1.32.0 | MIT | Upstream `axllent/mailpit` |
| BusyBox | 1.37.0 (Debian 1:1.37.0-6) | GPL-2.0-only | Installed in the CNMS Postfix image (queue-metrics and Rspamd metrics sidecars) |
| Debian base packages | trixie | Mixed, including GPL and LGPL | Base of the CNMS Postfix image and of the upstream Rspamd and Valkey images |

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
