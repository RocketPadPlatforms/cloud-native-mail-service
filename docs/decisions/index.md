# Architecture Decision Records (ADRs)

Decisions in this repository are recorded as ADRs: one file per decision,
numbered in the order they are written, immutable once accepted.

## Process

- Every decision that is not obvious from the code or the project plan gets
  an ADR before or at the point of implementation (engine versions, license
  choices, config strategies, enforcement layers).
- An accepted ADR is never rewritten. If a decision changes, write a new ADR
  that supersedes the old one and update the table below.
- If a requirement conflicts with reality, the conflict and the options are
  recorded in the ADR.

## Template

See [TEMPLATE.md](TEMPLATE.md).

## ADRs

| # | Title | Status |
|---|---|---|
| [0001](0001-engine-versions-and-licenses.md) | Engine versions, licenses, and image strategy | Accepted |
| [0002](0002-v1alpha1-api-design.md) | v1alpha1 API design for `MailServer`, `MailDomain`, `MailDirectory` | Accepted (partially superseded by 0003, amended by 0014) |
| [0003](0003-security-defaults-and-domain-ownership.md) | Security defaults and domain ownership (`MailDomainGrant`) | Accepted |
| [0004](0004-data-plane-images-and-build-strategy.md) | Data-plane images and build strategy | Accepted (partially amended by 0015) |
| [0005](0005-component-metrics-sources.md) | Component metrics sources | Accepted |
| [0006](0006-network-policy-and-security-floor.md) | NetworkPolicy floor and security floor | Accepted |
| [0007](0007-deployment-annotation-merge.md) | Merging operator-owned annotations and skipping no-op writes | Accepted |
| [0008](0008-user-sources-and-service-accounts.md) | Operator-managed users and service accounts | Accepted (partially amended by 0015) |
| [0009](0009-config-validation-gate.md) | Config validation gate with immutable configuration | Accepted |
| [0010](0010-storage-immutability-and-scale-down.md) | Immutable storage sizes and no Postfix scale-down yet | Accepted |
| [0011](0011-dkim-keys-and-rotation.md) | DKIM keys, signing configuration and rotation | Accepted |
| [0012](0012-dns-records-and-dmarc.md) | DNS records, DMARC and DNS verification | Accepted (partially amended by 0014) |
| [0013](0013-never-unsigned-guard.md) | Never-unsigned guard | Accepted (partially amended by 0014) |
| [0014](0014-domain-server-binding-and-hostname.md) | Domain-to-server binding and server hostname | Accepted |
| [0015](0015-user-sources-directory-users-and-aliases.md) | User sources, directory users and addresses | Accepted |

