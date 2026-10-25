# Architecture

CNMS is a Kubernetes operator (a control plane) that renders
configuration for proven mail software (the data plane) and manages
its lifecycle. This page explains how the parts fit together; the
[resource model](resource-model.md) documents the API objects, and
[control plane vs data plane](control-vs-data-plane.md) and
[failure modes](failure-modes.md) cover the operational consequences.

## Control plane

The operator is a standard controller-runtime manager with leader
election; running several replicas is safe. For each `MailServer` it:

- renders the engine configuration (Postfix, Rspamd, Valkey,
  Dovecot) deterministically,
- creates or updates the data-plane workloads, Services,
  NetworkPolicies and validation gate,
- evaluates domain authorization
  ([domain authorization](domain-authorization.md)) and keeps the
  status conditions up to date.

It does not run in the mail path: it never handles, stores or
inspects a message.

## Data plane

Each `MailServer` runs its own isolated set of workloads in its own
namespace — nothing is shared between servers
([reputation isolation](reputation-isolation.md)):

| Workload | Kind | Role |
|---|---|---|
| Postfix | StatefulSet, 2+ replicas | SMTP in (port 25 behind postscreen), submission (587), outbound delivery; one queue PVC per replica |
| Rspamd | Deployment, 2+ replicas | Filtering, DKIM signing, SPF/DMARC checks, rate limits |
| Valkey | one instance per server | Rspamd state (rate limits, reputation, Bayes) |
| Dovecot | StatefulSet, single replica | IMAP, LMTP delivery from Postfix, SASL authentication for submission |

Every workload runs as non-root with a read-only root FS where the
engine image allows it, logs to stdout, and serves its metrics on a
named port with a Service (sources and caveats: ADR 0005).

## Rendering and rollout

Configuration is rendered by engine-neutral Go renderers and stored
in ConfigMaps and Secrets; the workloads mount them. A content hash
of the rendered configuration is set as a pod-template annotation,
so a change triggers a rollout — while the certificate Secret is
kept **out** of the hash, so a certificate renewal triggers a reload
(`postfix reload`, Rspamd reload, `doveadm reload`) instead.

Before a new configuration replaces a working one, a validation gate
(init container) checks it with the engine's own tooling:
`postfix check`, `rspamadm configtest`, `doveconf -n`, and a
brief start-and-ping for Valkey (9.x has no config-check flag). A
bad configuration fails the rollout; it never replaces a working
one.

Engine and test images are pinned by version **and** digest in one
Go package consumed by renderers and the e2e suite, kept current by
Renovate (ADR 0004).

## Where state lives

Anything that cannot be recomputed from the world (bindings,
rotation schedule progress) lives in the spec or in annotations,
never only in the status. Custom resources are expected to be
restored from GitOps, which contains no status at all — after a
restore the operator re-derives everything else and never regenerates
a DKIM key that exists in a restored Secret.

## Observability

Every component exposes Prometheus-format metrics (ADR 0005). The
operator checks via API discovery whether the Prometheus Operator
CRDs are available; if so (and the `MailServer` observability
setting allows, default `auto`) it creates ServiceMonitors and
PodMonitors, otherwise it sets a condition explaining that and
continues normally. Without the CRDs the same metrics are scrapeable
from the per-component Services directly.

## Decisions

The design decisions behind this architecture are recorded in the
ADRs: [0002 (API design)](../decisions/0002-v1alpha1-api-design.md),
[0003 (security defaults)](../decisions/0003-security-defaults-and-domain-ownership.md),
[0004 (images and build strategy)](../decisions/0004-data-plane-images-and-build-strategy.md),
[0005 (metrics sources)](../decisions/0005-component-metrics-sources.md),
[0006 (network and security floor)](../decisions/0006-network-policy-and-security-floor.md).
