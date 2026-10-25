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
| [0002](0002-v1alpha1-api-design.md) | v1alpha1 API design for `MailServer`, `MailDomain`, `MailDirectory` | Accepted (partially superseded by 0003) |
| [0003](0003-security-defaults-and-domain-ownership.md) | Security defaults and domain ownership (`MailDomainGrant`) | Accepted |
| [0004](0004-data-plane-images-and-build-strategy.md) | Data-plane images and build strategy | Accepted |
| [0005](0005-component-metrics-sources.md) | Component metrics sources | Accepted |
| [0006](0006-network-policy-and-security-floor.md) | NetworkPolicy floor and security floor | Accepted |
| [0007](0007-deployment-annotation-merge.md) | Merging operator-owned annotations and skipping no-op writes | Accepted |
| [0008](0008-user-sources-and-service-accounts.md) | Operator-managed users and service accounts | Accepted |

