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
| Postfix | 3.11.7 | IPL-1.0 / EPL-2.0 (dual) | No official upstream image: built by CNMS (multi-arch, non-root, read-only root FS where possible). Build strategy decided in the Phase 2 ADR. |
| Rspamd | 4.2.3 | Apache-2.0 | Start from the official `rspamd/rspamd` image; non-root and read-only suitability verified in Phase 2. |
| Dovecot | 2.4.5 (incl. Pigeonhole) | Core LGPL-2.1, MIT parts | Prefer the official Dovecot image; verified in Phase 4. |
| Valkey | 9.1.2 | BSD-3-Clause | Official `valkey/valkey` image. |
| Go (toolchain) | 1.27.x (go.mod minimum 1.26.0) | BSD-3-Clause | `go-version-file: go.mod` in CI. |
| Kubebuilder | 4.16.0 | Apache-2.0 | Project scaffold tooling. |
| controller-runtime | v0.25.2 | Apache-2.0 | Pinned in `go.mod`; k8s API v0.37.0. |

Policy:

- **Runtime dependencies** (Go modules in the operator binary, images we build
  or reference by default): permissive licenses (Apache-2.0, MIT, BSD, ISC)
  or weak copyleft used unmodified as separate programs (LGPL, MPL-2.0,
  EPL/IPL). No SSPL, RSAL, BSL, Commons Clause, AGPL, or other
  source-available licenses.
- **Build/dev tools** (linters, doc generators, CI tooling): any OSI-approved
  license, recorded here when added.
- Enforced in CI by `make license-check`
  (`go-licenses check --disallowed_types=restricted,forbidden,unknown`).
- The third-party Go dependency report is generated with
  `make license-report` into `docs/reference/third-party-licenses.md`.
- Container images are pinned by version; engine images are additionally
  pinned by digest when they are first used in manifests (Phase 2+). The
  operator's build base (`golang:*`) is Renovate-managed by tag.
- Renovate: tooling (GitHub Actions, docs tooling) patch/minor updates may
  automerge. Engine and Go module updates always open a PR and must pass the
  golden-file tests and rendered-config validation. Major engine updates need
  a new ADR (config compatibility) and update this table.

## Consequences

- Rspamd's current stable line is 4.2.x. All Rspamd configuration is verified
  against the pinned 4.2 documentation before it is rendered (principle 8);
  assumptions are recorded here.
- Postfix is built by CNMS: image build, base-image choice, and non-root
  layout are Phase 2 work with their own ADR.
- Every Renovate PR that changes a version in this table must keep the table
  current (working agreement: versions are pinned and recorded).
