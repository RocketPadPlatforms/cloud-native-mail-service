# ADR 0001: Engine versions, licenses, and image strategy

- **Status:** Accepted
- **Date:** 2026-10-25

## Context

The project plan (section 0 and 9) requires the latest stable releases of the
mail engines, license-clean dependencies, version and digest pinning, and
Renovate-managed updates. Versions and licenses below were verified against
upstream release pages and registries on 2026-10-25.

## Decision

Pinned engine and toolchain versions (latest stable at verification time):

| Component | Version | License | Source / image strategy |
|---|---|---|---|
| Postfix | 3.11.7 | IPL-1.0 / EPL-2.0 (dual) | No official upstream image: built by CNMS from the upstream source tarball (amd64 and arm64 in CI; the master runs as root and drops to the `postfix` user, read-only root FS; see ADR 0004). |
| Rspamd | 4.2.3 | Apache-2.0 | Start from the official `rspamd/rspamd` image; non-root and read-only suitability verified in Phase 2. |
| Dovecot | 2.4.5 (incl. Pigeonhole) | Core LGPL-2.1, MIT parts | Prefer the official Dovecot image; verified in Phase 4. |
| Valkey | 9.1.2 | BSD-3-Clause | Official `valkey/valkey` image. |
| Go (toolchain) | 1.27.x (go.mod 1.27.3) | BSD-3-Clause | `go-version-file: go.mod` in CI; the builder image (Dockerfile) and the devcontainer use `golang:1.27` pinned by digest. Renovate updates the go directive and both images together (group `go`). |
| Kubebuilder | 4.16.0 | Apache-2.0 | Project scaffold tooling. |
| controller-runtime | v0.25.2 | Apache-2.0 | Pinned in `go.mod`; k8s API v0.37.0. |

Policy:

- **Runtime dependencies** (Go modules in the operator binary, images we build
  or reference by default): permissive licenses (Apache-2.0, MIT, BSD, ISC)
  or weak copyleft used unmodified as separate programs (LGPL, MPL-2.0,
  EPL/IPL). Strong copyleft (GPL) is allowed only for unmodified,
  separate programs inside images (BusyBox, Debian base packages),
  never linked into the operator binary. No SSPL, RSAL, BSL, Commons
  Clause, AGPL, or other source-available licenses.
- **Build/dev tools** (linters, doc generators, CI tooling): any OSI-approved
  license, recorded here when added.
- **Test-only Go modules**, including those of the e2e suite, are checked
  against the same allow list as runtime modules (the license check runs
  with the `e2e` build tag). Recorded: `github.com/emersion/go-msgauth`
  v0.7.0 (MIT, verifies the DKIM signatures in the e2e suite) and
  `golang.org/x/crypto` v0.54.0 (BSD-3-Clause, its dependency).
- Enforced in CI by `make license-check`
  (`go-licenses check --allowed_licenses=...`): only Apache-2.0,
  BSD-2-Clause, BSD-3-Clause, ISC and MIT pass for Go modules, so every
  other license fails the build until it has been reviewed and the
  `ALLOWED_LICENSES` list in the Makefile is extended.
- The third-party Go dependency report is generated with
  `make license-report` into `docs/reference/third-party-licenses.md`.
- Container images are pinned by version and digest (the CNMS-built
  images by version tag only, see ADR 0004); Renovate keeps both current,
  including the operator's build base (`golang:*`).
- Renovate: only patch updates of tooling (GitHub Actions, docs tooling) may
  automerge. Engine and Go module updates always open a PR and must pass the
  golden-file tests and rendered-config validation. Major engine updates are
  labelled `needs-adr`: they need a new ADR (config compatibility) and update
  this table.

## Consequences

- Rspamd's current stable line is 4.2.x. All Rspamd configuration is verified
  against the pinned 4.2 documentation before it is rendered (principle 8);
  assumptions are recorded here.
- Postfix is built by CNMS: image build, base-image choice and runtime
  layout are decided in ADR 0004.
- Every Renovate PR that changes a version in this table must keep the table
  current (working agreement: versions are pinned and recorded).
