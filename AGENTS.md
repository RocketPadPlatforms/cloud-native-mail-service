# Cloud Native Mail Service (CNMS) — Agent Guide

This file is the persistent agent guide for this repository. Part 1 is
the project specification (decisions, principles, phase plan). Part 2 is
the kubebuilder project-conventions scaffold.

---

# Task: Build CNMS, a Kubernetes operator for cloud-native email

You are building **Cloud Native Mail Service (CNMS)**, a production-grade
Kubernetes operator that runs and manages email servers (sending, optionally via
a relay, receiving, and mailboxes) on Kubernetes.
Work autonomously, but follow the phases, decisions and working agreements below.
Put this file in the repo as `AGENTS.md` (or your tool's equivalent) so it persists.

## 0. Project facts (decided)

| Item | Value |
|---|---|
| Name | Cloud Native Mail Service (CNMS) |
| API group | `cnms.rpp.gmbh` (Kubebuilder: `--domain rpp.gmbh`, group `cnms`) |
| Repository / Go module | `github.com/RocketpadPlatforms/cloud-native-mail-service` |
| License | Apache-2.0 |
| Docs hosting | GitHub Pages at the default URL `https://rocketpadplatforms.github.io/cloud-native-mail-service/` (no custom domain for now) |
| Key-value store | Valkey (not Redis; licensing, see section 9) |
| rDNS | `manual` only for now (default); backend stays behind an interface |
| Dovecot (mailboxes) | In scope for the first release |
| Sending IPs per claim | Single and multiple are both supported |
| Engine versions | Latest stable releases, kept current by Renovate |
| Rspamd + Valkey | One dedicated set per `MailServer` (never shared) |
| Backup tool | k8up (documented default) for volume data; custom resources live in GitOps |

## 1. Core principles (non-negotiable)

1. **Do not write an MTA.** The operator is a control plane that renders
   configuration for proven software and manages its lifecycle:
   **Postfix** (SMTP in/out, relay), **Rspamd** (+ **Valkey**; spam, DKIM
   signing, SPF/DMARC checks, rate limits), and **Dovecot 2.4** (IMAP, LMTP,
   SASL auth).
2. **Users are managed outside the operator.** No Mailbox/User CRDs. Accounts
   come from an external directory (`MailDirectory`: LDAP and SQL first,
   OIDC later). The operator only configures Postfix/Dovecot to query it.
3. **Engine-neutral API, replaceable renderer.** CRDs must not leak Postfix
   syntax. Put config rendering behind a Go interface so another engine could be
   added later.
4. **Control plane != data plane.** If the operator is down, mail keeps flowing.
   Data-plane pods run from rendered ConfigMaps/Secrets and never depend on the
   operator at runtime. The same applies to anything that serves traffic for
   mail (e.g. an MTA-STS policy server): it is data plane, not part of the
   operator pod.
5. **Mail must never be lost because a component is down.** Filter outage or
   lookup failure => temporary failure (4xx) / queueing, never reject, never
   send unsigned. See section 4 ("Never unsigned") for what this requires in
   practice; `milter_default_action=tempfail` alone is not sufficient.
6. **Reputation isolation between mail servers.** Several `MailServer`s on one
   cluster must not affect each other's sender reputation (own Postfix
   instances and queues, own IPs, own DKIM keys, own HELO names). Each
   `MailServer` also gets its own Rspamd Deployment and its own Valkey
   instance; rate-limit, reputation and Bayes state are never shared.
7. **No message content in etcd.** No per-message CRDs.
8. **Verify, don't assume.** Postfix/Rspamd/Dovecot config syntax and versions
   change (Dovecot 2.4 is incompatible with 2.3 settings; no Director or
   replication in 2.4). Read the upstream docs for the exact pinned versions
   before rendering config. If unsure, write down the assumption in
   `docs/decisions/` and flag it in your summary. If you cannot access the docs
   or source for a pinned version, stop and ask; do not fall back on memory.
9. **License-clean dependencies.** Every dependency and tool is license-checked
   before it is added and again when it changes major version (see section 9).
10. **Optional integrations degrade gracefully.** Prometheus Operator CRDs,
    cert-manager, external-dns, Cilium and backup tools are integrations, not
    hard requirements, unless a feature explicitly needs them. Missing
    integrations disable the feature with a clear status condition; they never
    crash the operator.

## 2. Tech stack

- Go, Kubebuilder (latest stable), controller-runtime, `metav1.Condition` for
  status, finalizers, owner references, Events, leader election.
- CRDs `v1alpha1`, status subresource, CEL validation rules where possible
  (admission webhooks only if CEL cannot express the rule).
- Tests: unit (renderers: golden files), `envtest` (controllers), `kind` e2e.
- Lint/CI: golangci-lint, `make test`, `make e2e`, GitHub Actions.
- Packaging: Kustomize + Helm chart for the operator. Multi-arch images.
- Dependency updates: **Renovate** for Go modules, GitHub Actions (pinned by
  digest), container images (pinned by digest), Helm chart dependencies, and
  tool/engine versions in Makefiles and Dockerfiles (custom managers where no
  built-in manager exists).
- Docs: Docusaurus with versioning, deployed to GitHub Pages (section 10).

## 3. Resource model

Cluster-scoped (platform admin):
- `MailIPPoolClass`: how addresses are attached (e.g. Cilium egress gateway),
  `accessMode` (Exclusive|Shared), `reclaimPolicy` (Retain|Quarantine|Release),
  quarantine duration, warm-up schedule.
- `MailIPPool`: addresses (IPv4/IPv6), PTR name per address, rDNS backend
  (`manual` only for now, default).

Namespaced (tenant):
- `MailIPPoolClaim`: tenant requests one or more addresses (PVC-style). Binds
  to exactly one pool; exclusive pools bind once. The binding is recorded in
  **spec** once made (like `PersistentVolumeClaim.spec.volumeName`), so it
  survives a restore without status. Status: phase, bound pool, per-address
  details (address, HELO name derived from the PTR, warm-up stage,
  verification), conditions (`PTRVerified`, `EgressIPVerified`,
  `BlocklistClean`).
- `MailServer`: engines + replicas + storage, inbound service, outbound mode
  (`direct` with `ipPoolClaimRef`, or `relay` with `relayRef`), `directoryRef`,
  submission settings, mailbox settings (Dovecot), TLS (cert-manager
  issuerRef), availability (PDB, spread), observability toggles.
- `MailDomain`: domain, DKIM selectors + rotation (overlapping selectors), SPF
  (generated), DMARC (RFC 9989-9991 semantics, not RFC 7489), MTA-STS, TLS-RPT,
  DNS mode (`externalDNS` via DNSEndpoint | `manual`: report required records
  in status). Status lists every required DNS record and whether lookup verifies.
- `MailRelay`: smarthost, port, TLS, auth Secret, fallback relay.
- `MailDirectory`: `ldap` | `sql` (later `oidc`), credentials via Secret refs.
- `MailPolicy`: rate limits, TLS enforcement per destination, auto-suspend
  thresholds (bounce rate; complaint rate only once a data source exists, see
  open question 2).
- `InboundRoute`: match recipients -> deliver to HTTP webhook or LMTP service,
  with retry and dead-letter behaviour.

**State rule:** anything that cannot be recomputed from the world (bindings,
quarantine start times, rotation schedule progress) lives in spec or in
annotations, not only in status. Status must be re-derivable: custom resources
are restored from GitOps (section 8), which contains no status at all.
Fields the operator writes into spec (e.g. a claim binding) must be documented
so GitOps users can either commit them or configure their GitOps tool to
ignore the difference without the binding being lost.

A full example is in `examples/full-setup.yaml` **if it was provided with this
task**. Treat it as a design reference, not a spec; field names may be improved,
but explain deviations. If the file does not exist, draft it yourself and mark
it clearly as a draft, not as an agreed reference.

## 4. Behaviour requirements

**Rendering and rollout**
- Deterministic rendering; content hash annotation on pod templates to trigger
  rollouts; reload (`postfix reload`, Rspamd reload, `doveadm reload`) where a
  restart isn't needed.
- Certificate renewals (cert-manager Secret changes) trigger a **reload**, not a
  rollout: keep the cert Secret out of the pod-template content hash.
- Validate rendered config before rollout (dry-run Job or init container using
  `postfix check`, `rspamadm configtest`, `doveconf -n`). A bad config must
  never replace a working one.
- Log to stdout (`maillog_file=/dev/stdout` for Postfix; see section 7 for how
  log-derived metrics are obtained).

**Never unsigned (principle 5 in practice)**
- Set `milter_default_action=tempfail` with deliberately chosen milter timeouts.
- Also configure `non_smtpd_milters` (and `internal_mail_filter_classes` if
  locally generated bounces should be signed): mail generated by Postfix itself
  does not pass through `smtpd_milters`.
- `milter_default_action` only covers an unreachable or timing-out milter. If
  Rspamd is up but has no usable DKIM key, it may pass the message through
  unsigned. Add an explicit guard: for authenticated/local senders, a missing
  signature must result in tempfail (Rspamd rule or equivalent), and key
  presence must be checked before rollout. DKIM keys are served from mounted
  Secrets, not from Valkey, so signing does not depend on the key-value store.

**HA / resilience**
- Postfix: StatefulSet, one PVC per replica for the queue, 2+ replicas.
  Rspamd: Deployment, 2+ replicas, one Deployment per `MailServer`.
  Valkey: one instance per `MailServer`, persistence configurable.
- Dovecot: single replica, RWO volume for mail storage (active/standby via
  rescheduling). Document the realistic failover time: after a node failure an
  RWO volume can stay attached for minutes unless the node is tainted/removed
  (non-graceful node shutdown); put this in the runbook. Choose the mailbox
  format in an ADR, considering crash-consistent snapshots (section 8).
- PodDisruptionBudgets and topology spread. Rollouts: Deployments (Rspamd) use
  `maxUnavailable: 0` with surge. The Postfix StatefulSet cannot surge, so it
  uses ordered one-at-a-time updates gated on readiness plus a PDB.
- Graceful shutdown: a generous `terminationGracePeriodSeconds`, but remember
  that deferred mail can stay queued for days; a grace period never "drains" a
  queue.
- **Scale-down is operator-orchestrated:** before a Postfix replica's PVC is
  released, its queue is moved to the remaining replicas (or the replica is
  retained until empty). Orphaned PVCs with live mail are never deleted
  automatically.
- Queue-rescue: a mechanism (Job) to mount an orphaned queue PVC and
  requeue/flush; used for scale-down and for disaster recovery.
- Operator: leader election, safe to run 2+ replicas.
- Valkey outage must degrade gracefully (rate limits and some modules may
  degrade; mail still flows and is signed). Document the behaviour per module.

**Mailboxes and authentication (Dovecot)**
- Dovecot provides IMAP, LMTP delivery from Postfix, and SASL auth for
  submission, all backed by the `MailDirectory`.
- If submission is enabled but mailboxes are disabled for a `MailServer`, run
  Dovecot in **auth-only mode** (no mail storage). Do not invent another SASL
  mechanism.

**Network and IPs**
- Inbound: LoadBalancer Service with `externalTrafficPolicy: Local` (or PROXY
  protocol, configurable). Use postscreen on port 25 (it supports the PROXY
  protocol).
- Outbound (direct mode): per-claim egress. Start with Cilium egress gateway
  policy generation behind an interface; keep the mechanism pluggable.
  Egress gateway HA is a Cilium Enterprise feature; with OSS Cilium the gateway
  node is a single point of failure (it fails closed). Record this in an ADR
  and surface it as a documented limitation.
- **Multiple sending IPs per claim:** the gateway does the SNAT, so Postfix
  cannot choose its source IP and `smtp_bind_address` does not apply. Map IPs
  to **pods** instead: each Postfix replica gets one sending address per
  address family via an egress policy that selects that pod (e.g. by the
  `statefulset.kubernetes.io/pod-name` label), and that pod's `smtp_helo_name`
  is the PTR name of exactly those addresses (per-ordinal rendered config,
  selected at startup). Mapping is `ordinal mod N`. If replicas < addresses,
  some addresses are unused: report this as a status condition (CEL cannot
  check it across objects). For dual-stack, the IPv4 and IPv6 address of a pod
  share one HELO name with matching A/AAAA and PTR records. Warm-up limits
  apply per address. Verify the selector and policy fields against the pinned
  Cilium version before implementing.
- Generate NetworkPolicies so port-25 egress only goes through the assigned
  path (fail closed). Also provide a cluster-wide default-deny for port-25
  egress so other pods cannot send mail from node IPs and damage reputation.
- **rDNS: `manual` only (default).** The operator computes the required PTR
  records (in-addr.arpa/ip6.arpa names and targets) and reports them in the
  pool status; the admin creates them at the hoster. Verify with a real lookup
  and forward-confirmation. A sending IP must have exactly one PTR. Keep the
  rDNS backend behind an interface so `externalDNS` and provider APIs can be
  added later (backlog).
- Blocklist checker (behind an interface): must support a configurable resolver
  and/or provider data-query key, because major blocklists refuse queries from
  public or large shared resolvers and would otherwise give false results.
- Warm-up limits are enforced via rate limits derived from the pool schedule.
  Decide and document the enforcement layer in an ADR: Postfix per-destination
  concurrency/rate delay (delays mail) vs. Rspamd ratelimit (rejects or
  tempfails). Prefer the one that delays rather than rejects.
- On claim deletion with `Quarantine`, keep the pool unavailable until the
  quarantine duration passes and the blocklist check is clean.

**DNS and MTA-STS**
- DMARCbis semantics (RFC 9989-9991) apply to the records the operator
  *publishes*: `v=DMARC1`, no `pct`/`rf`/`ri`, `t=` for testing, optional `np`
  and `psd`. Inbound DMARC *evaluation* is done by Rspamd and follows what the
  pinned Rspamd version implements (it may still use the Public Suffix List
  instead of the DNS tree walk). Record the exact behaviour in an ADR.
- MTA-STS requires an HTTPS endpoint at
  `https://mta-sts.<domain>/.well-known/mta-sts.txt` with a valid public
  certificate. Until open question 1 is decided, implement **report-only**:
  `MailDomain` status contains the exact policy content, its `id`, and the
  required `_mta-sts` TXT record. The policy `id` changes whenever the policy
  content changes.

**Inbound delivery**
- `InboundRoute` retry and dead-letter must not use etcd or a new custom queue.
  Prefer delivery via Postfix (LMTP or pipe to a small forwarder) so Postfix's
  own queue provides retries, with a defined bounce/dead-letter outcome. Record
  the design in an ADR before Phase 8.

**Security**
- Secrets only via Secret refs; never log secret values, DKIM private keys
  live in Secrets generated by the operator (RBAC-minimal). Allow
  bring-your-own DKIM keys via Secret ref (e.g. SOPS/sealed secrets in GitOps
  or an external secret store); this is the recommended production setup
  because it puts the keys under GitOps-style backup (section 8).
- Least-privilege RBAC for the operator; tenants cannot reference other
  tenants' resources (namespace-scoped refs; claims are the only way to use a pool).
- Containers: non-root where possible, read-only root FS where possible,
  documented exceptions.

## 5. Phases

Work one phase at a time. At the end of each phase: run all tests, update docs,
summarize what was done, list assumptions and open risks, check the phase's
definition of done, then **stop and wait for review** (unless told to continue).
Every phase's definition of done includes: docs for the new feature exist,
the docs site builds without broken links, and the license check passes.

- **Phase 0 - Scaffold:** repo layout, Kubebuilder init with the decided
  domain/group/module, CI, lint, Makefile, `docs/decisions/` ADR template,
  `AGENTS.md`, example directory, Apache-2.0 `LICENSE` and `NOTICE`,
  Renovate config, license-check job, Docusaurus skeleton with GitHub Pages
  deployment, `CONTRIBUTING.md`, `SECURITY.md`.
  *Done when:* `make test`, lint, license check and docs build run green in CI
  on an empty operator, and the docs skeleton is live at the default GitHub
  Pages URL.
- **Phase 1 - API (core subset):** define only the CRDs needed for Phases 2-5
  (`MailServer`, `MailDomain`, `MailDirectory`), with defaults, CEL validation,
  generated CRDs, generated API reference docs, and sample CRs that validate in
  envtest. No controllers yet. The remaining CRDs are added in the phase that
  implements them, so fields are designed with implementation feedback.
  *Done when:* every file in `config/samples` validates in envtest and the API
  reference appears in the docs.
- **Phase 2 - MailServer core:** render Postfix + Rspamd (+ Valkey) for
  inbound and submission with a static test user source (including auth-only
  Dovecot for submission); StatefulSet/Deployment/Services/PVCs/PDBs; config
  validation Job; golden-file tests; basic metrics endpoints (section 7);
  e2e on kind with Mailpit as a sink (send with `swaks`).
  *Done when:* the swaks e2e test passes in CI and a deliberately broken config
  does not replace a working one.
- **Phase 3 - MailDirectory:** LDAP and SQL maps for Postfix (recipient
  validation, tempfail on lookup errors) and Dovecot auth; e2e with an LDAP
  test container.
  *Done when:* e2e shows valid/invalid recipients and tempfail when the
  directory is down.
- **Phase 4 - Mailboxes (Dovecot):** IMAP, LMTP delivery from Postfix, mail
  storage PVC, mailbox format ADR, quota basics, Dovecot metrics, backup hooks
  (section 8).
  *Done when:* e2e delivers via SMTP and reads via IMAP; a k8up backup and
  restore of the mail volume on kind (against a local S3-compatible test
  target) recovers the message.
- **Phase 5 - MailDomain:** DKIM key generation + selector rotation, SPF/DMARC/
  MTA-STS (report-only)/TLS-RPT records, DNS via DNSEndpoint or manual status,
  DNS verification through an injectable resolver interface.
  *Done when:* rotation keeps both selectors valid during overlap, and status
  lists every required record with verification state.
- **Phase 6 - Relay mode:** add `MailRelay`; per-domain routing, fallback relay,
  SASL secrets.
- **Phase 7 - IP management:** add `MailIPPoolClass`/`MailIPPool`/
  `MailIPPoolClaim`; class/pool/claim controllers, binding (recorded in spec),
  single and multiple addresses per claim (pod mapping), quarantine, egress
  policy generation, NetworkPolicies, manual rDNS with verification, probes,
  conditions.
- **Phase 8 - InboundRoute:** add `InboundRoute`; webhook and LMTP delivery with
  retry/dead-letter (per the ADR in section 4).
- **Phase 9 - Hardening:** add `MailPolicy` (limits, bounce-rate auto-suspend);
  full metrics set, alert rules and dashboards (section 7); k8up
  backup/restore e2e for all components with data (section 8); chaos e2e (kill a Postfix pod -> queue survives; kill
  Rspamd -> mail queues, nothing unsigned leaves; operator down -> mail flows;
  Prometheus Operator CRDs absent -> operator runs normally).
- **Phase 10 - Docs and release:** complete user docs, upgrade notes, Helm
  chart, release automation (including cutting a docs version per release).

## 6. Testing rules

- Never send real mail or make real DNS/provider API calls in tests. Use
  fakes (resolver, rDNS provider, blocklist checker) behind interfaces;
  use Mailpit and kind for e2e.
- Every renderer has golden-file tests; every controller has envtest tests for
  create/update/delete, finalizer behaviour and failure conditions.
- Alert rules are unit-tested with `promtool test rules`.
- Run envtest and e2e both **with and without** the Prometheus Operator CRDs
  installed.
- Use documentation address ranges in samples (203.0.113.0/24, 198.51.100.0/24,
  2001:db8::/32) and `example.*` domains.

## 7. Observability

**Integration model (works with and without Prometheus Operator)**
- Every component exposes Prometheus-format metrics on a named port with a
  Service. This is the baseline and works with any Prometheus-compatible
  scraper.
- At startup and periodically, the operator checks via API discovery whether
  `monitoring.coreos.com/v1` (ServiceMonitor, PodMonitor, PrometheusRule) is
  available. If it is available **and** enabled (Helm value / `MailServer`
  field, default `auto`), it creates ServiceMonitors/PodMonitors and
  PrometheusRules. If not, it does not watch or create them, sets a condition
  explaining that, and continues normally. CRDs installed later are picked up
  without restarting the operator.
- Without the CRDs: the Helm chart ships the same alert rules as a plain
  Prometheus rules file (and optionally a ConfigMap), plus documented scrape
  configs. One canonical source generates both formats; never maintain two
  copies by hand.
- Grafana dashboards as JSON in the repo, optionally deployed as ConfigMaps
  with the label used by the Grafana dashboard sidecar.
- Every alert has a `runbook_url` that points to a runbook page in the docs.

**Metric sources (verify each against the pinned versions and license-check
any exporter)**
- Operator: controller-runtime metrics plus CNMS metrics (reconcile errors,
  DNS record verification state, DKIM key age, certificate expiry, blocklist
  state, claim/pool phases, quarantine timers).
- Postfix: no native Prometheus endpoint. Queue metrics (length, age, deferred)
  can be read via `postqueue -j`; delivery/deferral/bounce counters need log
  parsing. Evaluate maintained exporters (maintenance status, license, whether
  they can read logs while Postfix logs to stdout). If none fits, write a
  minimal CNMS sidecar exporter. Decide in an ADR.
- Rspamd: native Prometheus metrics endpoint (verify path and worker).
- Dovecot 2.4: native OpenMetrics exporter via the stats service (verify
  config).
- Valkey: a maintained Redis/Valkey-compatible exporter (license check).

**Rules**
- No sender/recipient addresses, message IDs or subjects as metric labels
  (privacy and cardinality). Per-domain labels only where bounded.
- `MailPolicy` auto-suspend must not depend on Prometheus: the operator reads
  the needed counters from the component metrics endpoints itself.
- Logs: stdout everywhere; the operator logs structured JSON; Kubernetes Events
  for important state changes. Tracing is out of scope for now.
- Status conditions and printer columns give a usable `kubectl get` view
  without any monitoring stack.

## 8. Backup and restore

The operator does **not** implement a backup engine. It makes CNMS easy and
safe to back up with standard tools, documents what matters, and proves restore
in e2e.

**Tooling decision**
- **Custom resources:** GitOps is the source of truth and the backup. No
  cluster-resource backup tool is required.
- **Volume data:** **k8up** (restic-based, Apache-2.0) is the documented
  default. Verify its license, current release and features against upstream
  before relying on them, and record the decision in an ADR.
- k8up backs up PVC contents and application output (backup-command
  annotations / pre-backup pods); it does **not** back up Kubernetes objects
  such as Secrets. Design for that (see DKIM keys below).
- k8up is an optional integration (principle 10): the operator must work
  without k8up installed. It only sets annotations and labels; it does not
  require k8up CRDs. Backup `Schedule`s and repository credentials are created
  by the user (documented examples provided), optionally by the operator later
  (backlog).

**What needs backing up**

| Data | Where | Criticality | Strategy |
|---|---|---|---|
| CNMS custom resources (desired state) | etcd | High | GitOps repo |
| DKIM private keys | Secrets | High (loss forces a new selector and DNS change) | Recommended: bring-your-own keys from GitOps (SOPS/sealed secrets) or an external secret store. For operator-generated keys: an optional k8up backup command on a pod that already mounts the keys (Rspamd) streams them into the encrypted restic repository. Restore is a documented manual procedure. If keys are lost, recovery is a selector rotation |
| Mailboxes (Dovecot storage) | RWO PVC | Critical | k8up file-level backup on a schedule. Choose a mailbox format that is safe for file-level backup of a live volume (ADR); indexes are rebuildable. Verify how k8up handles RWO volumes in the pinned version |
| Sieve scripts / per-user Dovecot data | Dovecot volume | Medium | Included with mailboxes |
| Postfix queues | per-replica PVC | Transient | **Excluded** from backup via annotation. Stale queues cause duplicate or late delivery; protect with PVC durability and queue-rescue |
| Valkey (Bayes, fuzzy, rate-limit state) | Valkey volume | Low-medium | Excluded by default; rebuildable except trained Bayes data. Opt-in: backup command that produces a consistent dump instead of copying live files |
| TLS certificates | Secrets | Low | Reissued by cert-manager |
| IP binding / quarantine state | spec / annotations (section 3) | High | Must reach GitOps or survive in the cluster; see section 3 |
| Directory (users) | external | n/a | Owned by the directory operator, not CNMS |
| InboundRoute dead-letters | per ADR | Medium | Placed on a volume that k8up backs up |

**Operator responsibilities**
- Label all CNMS-managed resources consistently.
- Annotate PVCs and pods for k8up: include the Dovecot volume, exclude Postfix
  queues and (by default) Valkey, add backup commands where consistency or
  Secrets require it. All of this behind a `MailServer` toggle.
- After a restore: reconcile without status, never regenerate DKIM keys that
  exist in restored Secrets, never re-bind claims differently from spec, and
  never start delivering a restored Postfix queue without an explicit action.
- Document RPO/RTO guidance, a restore runbook (mailboxes and DKIM keys), and
  a regular restore drill.

## 9. Licensing and dependencies

- CNMS is Apache-2.0. Check the license of every dependency, image and tool
  **before** adding it and when Renovate proposes a major update or when a
  project changes its license (Redis is the example: CNMS uses Valkey).
- Policy:
  - **Shipped runtime** (Go modules in the binary, images we build or
    reference by default): permissive licenses (Apache-2.0, MIT, BSD, ISC) or
    weak copyleft used unmodified as separate programs (e.g. LGPL, MPL-2.0,
    EPL-2.0/IPL for Postfix). No SSPL, RSAL, BSL, Commons Clause, AGPL or other
    source-available licenses.
  - **Build/dev tools** not shipped (linters, doc generators, Renovate as a
    service): any OSI-approved license, recorded.
- Starting point to verify (do not trust this table; check the source):
  Postfix (IPL-1.0 / EPL-2.0), Rspamd (Apache-2.0), Dovecot (core LGPL-2.1 plus
  MIT parts), Valkey (BSD-3-Clause), Docusaurus (MIT), k8up (Apache-2.0)
  and restic (BSD-2-Clause).
- CI: Go module license check (e.g. `go-licenses`), SBOM generation for
  images, and an allow/deny list that fails the build on violations.
- Keep `NOTICE` and third-party notices current; publish them in the docs.
- **Versions:** use the latest stable releases of Postfix, Rspamd, Dovecot and
  Valkey. Prefer upstream official images where they exist and fit the
  security requirements (non-root, read-only root FS); build our own where not
  (Postfix has no official image). Pin by version **and** digest. Renovate
  keeps them current: patch updates may automerge only for tooling; engine
  updates always open a PR, and golden-file tests plus config validation must
  pass. Major engine updates need an ADR (config compatibility).

## 10. Documentation

**Tooling**
- Docusaurus (MIT) with docs versioning, deployed by GitHub Actions to GitHub
  Pages. Builds run on every PR with broken links failing the build.
- Generated reference: CRD API reference from Go types (e.g. `crd-ref-docs`),
  Helm values reference (e.g. `helm-docs`), metrics and alerts reference
  generated from the same source as the rules. License-check these tools too.
- Doc examples come from `examples/` and are validated in envtest, so docs
  cannot drift from the API.

**Structure (Diataxis)**
- Getting started: kind quickstart, first send and receive.
- Concepts: architecture, control vs data plane, reputation isolation, IP
  pools and claims, DKIM rotation, failure modes.
- How-to guides: install (Helm, Kustomize), DNS records, manual rDNS, IP
  pools, directories (LDAP/SQL), relay mode, mailboxes, observability with
  and without Prometheus Operator, backup and restore with k8up (including
  DKIM key handling), GitOps setup, upgrades.
- Operations: one runbook per alert, queue rescue, Dovecot failover.
- Reference: API, Helm values, metrics, alerts, compatibility matrix (engine
  and Kubernetes versions), third-party licenses.
- Project: ADRs (`docs/decisions/`), contributing, security policy, roadmap,
  changelog.

**Versioning strategy**
- `main` is published as "Next (unreleased)". A docs version is cut
  automatically for each **minor** release; patch releases update the docs of
  their minor version.
- Keep only supported minor versions in the version dropdown; archive older
  ones to avoid build-time and maintenance cost.
- While the API is `v1alpha1`, every page that describes fields states the API
  version, and upgrade notes are mandatory for every release.

## 11. Working agreements

- Plan before coding: at the start of each phase write a short plan (files to
  add, interfaces, test strategy) and then execute it.
- Small, reviewable commits with clear messages. Don't mix refactors with features.
- Don't expand scope beyond the current phase. Park ideas in `docs/backlog.md`.
- Don't invent library APIs or config options: check the source/docs, and
  say so when you can't verify something.
- Pin versions (images, tools) and record why in an ADR; check licenses.
- Keep the public API small; prefer fields that can be added later over
  fields that are hard to remove.
- If a requirement conflicts with reality (e.g. a provider can't do X), stop and
  report with options instead of silently working around it.
- Docs are part of every change, not a final phase.

## 12. Out of scope (for now)

Webmail/UI, billing, multi-cluster federation, OIDC directory, DANE/DNSSEC
automation, built-in anti-spam training UI, IMAP horizontal scaling, rDNS
backends other than `manual`, tracing, a built-in backup engine.

## 13. Open questions to resolve with the human before Phase 1

Resolved: project name, API group, repository, license, Valkey, rDNS (manual),
Dovecot in scope, single and multiple IPs per claim, latest stable engines with
Renovate, one Rspamd + Valkey set per `MailServer`, k8up for volume backups
with CRs in GitOps, docs at the default GitHub Pages URL.

1. **MTA-STS serving:** stay report-only, or let CNMS run a policy server
   (data-plane Deployment + Gateway API/Ingress route + cert-manager
   certificate)? Report-only is the default until decided.
2. **Complaint data for auto-suspend:** bounce rate only for the first
   release, or ingest ARF feedback-loop reports (and/or accept externally
   supplied complaint rates)?
3. Target CNI for egress (Cilium assumed; OSS or Enterprise, since egress
   gateway HA is Enterprise-only) and load-balancer (MetalLB assumed).
4. Target DNS provider(s) for forward records via external-dns, if any.
5. DKIM keys: bring-your-own from GitOps/external secret store as the
   documented production default, with operator-generated keys for quick
   starts? (Recommended, because k8up does not back up Secrets.)
6. GitOps tool (Argo CD, Flux, ...) to document, including how to handle
   fields the operator writes into spec (claim bindings).

---

## Kubebuilder project conventions (scaffold)


## Project Structure

**Single-group layout (default):**
```
cmd/main.go                    Manager entry (registers controllers/webhooks)
api/<version>/*_types.go       CRD schemas (+kubebuilder markers)
api/<version>/zz_generated.*   Auto-generated (DO NOT EDIT)
internal/controller/*          Reconciliation logic
internal/webhook/*             Validation/defaulting (if present)
config/crd/bases/*             Generated CRDs (DO NOT EDIT)
config/rbac/role.yaml          Generated RBAC (DO NOT EDIT)
config/samples/*               Example CRs (edit these)
Makefile                       Build/test/deploy commands
PROJECT                        Kubebuilder metadata Auto-generated (DO NOT EDIT)
```

**Multi-group layout** (for projects with multiple API groups):
```
api/<group>/<version>/*_types.go       CRD schemas by group
internal/controller/<group>/*          Controllers by group
internal/webhook/<group>/<version>/*   Webhooks by group and version (if present)
```

Multi-group layout organizes APIs by group name (e.g., `batch`, `apps`). Check the `PROJECT` file for `multigroup: true`.

**To convert to multi-group layout:**
1. Run: `kubebuilder edit --multigroup=true`
2. Move APIs: `mkdir -p api/<group> && mv api/<version> api/<group>/`
3. Move controllers: `mkdir -p internal/controller/<group> && mv internal/controller/*.go internal/controller/<group>/`
4. Move webhooks (if present): `mkdir -p internal/webhook/<group> && mv internal/webhook/<version> internal/webhook/<group>/`
5. Update import paths in all files
6. Fix `path` in `PROJECT` file for each resource
7. Update test suite CRD paths (add one more `..` to relative paths)

## Critical Rules

### Never Edit These (Auto-Generated)
- `config/crd/bases/*.yaml` - from `make manifests`
- `config/rbac/role.yaml` - from `make manifests`
- `config/webhook/manifests.yaml` - from `make manifests`
- `**/zz_generated.*.go` - from `make generate`
- `PROJECT` - from `kubebuilder [OPTIONS]`

### Never Remove Scaffold Markers
Do NOT delete `// +kubebuilder:scaffold:*` comments. CLI injects code at these markers.

### Keep Project Structure
Do not move files around. The CLI expects files in specific locations.

### Always Use CLI Commands
Always use `kubebuilder create api` and `kubebuilder create webhook` to scaffold. Do NOT create files manually.

### E2E Tests Require an Isolated Kind Cluster
The e2e tests are designed to validate the solution in an isolated environment (similar to GitHub Actions CI).
Ensure you run them against a dedicated [Kind](https://kind.sigs.k8s.io/) cluster (not your “real” dev/prod cluster).

## After Making Changes

**After editing `*_types.go` or markers:**
```
make manifests  # Regenerate CRDs/RBAC from markers
make generate   # Regenerate DeepCopy methods
```

**After editing `*.go` files:**
```
make lint-fix   # Auto-fix code style
make test       # Run unit tests
```

## CLI Commands Cheat Sheet

### Create API (your own types)
```bash
kubebuilder create api --group <group> --version <version> --kind <Kind>
```

### Deploy Image Plugin (scaffold to deploy/manage ANY container image)

Generate a controller that deploys and manages a container image (nginx, redis, memcached, your app, etc.):

```bash
# Example: deploying memcached
kubebuilder create api --group example.com --version v1alpha1 --kind Memcached \
  --image=memcached:alpine \
  --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

Scaffolds good-practice code: reconciliation logic, status conditions, finalizers, RBAC. Use as a reference implementation.


### Create Webhooks
```bash
# Validation + defaulting
kubebuilder create webhook --group <group> --version <version> --kind <Kind> \
  --defaulting --programmatic-validation

# Conversion webhook (for multi-version APIs)
kubebuilder create webhook --group <group> --version v1 --kind <Kind> \
  --conversion --spoke v2
```

### Controller for Core Kubernetes Types
```bash
# Watch Pods
kubebuilder create api --group core --version v1 --kind Pod \
  --controller=true --resource=false

# Watch Deployments
kubebuilder create api --group apps --version v1 --kind Deployment \
  --controller=true --resource=false
```

### Controller for External Types (e.g., from other operators)

Watch resources from external APIs (cert-manager, Argo CD, Istio, etc.):

```bash
# Example: watching cert-manager Certificate resources
kubebuilder create api \
  --group cert-manager --version v1 --kind Certificate \
  --controller=true --resource=false \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

**Note:** Use `--external-api-module=<module>@<version>` only if you need a specific version. Otherwise, omit `@<version>` to use what's in go.mod.

### Webhook for External Types

```bash
# Example: validating external resources
kubebuilder create webhook \
  --group cert-manager --version v1 --kind Issuer \
  --defaulting \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

## Testing & Development

```bash
make test              # Run unit tests (uses envtest: real K8s API + etcd)
make run               # Run locally (uses current kubeconfig context)
```

Tests use **Ginkgo + Gomega** (BDD style). Check `suite_test.go` for setup.

## Deployment Workflow

```bash
# 1. Regenerate manifests
make manifests generate

# 2. Build & deploy
export IMG=<registry>/<project>:tag
make docker-build docker-push IMG=$IMG  # Or: kind load docker-image $IMG --name <cluster>
make deploy IMG=$IMG

# 3. Test
kubectl apply -k config/samples/

# 4. Debug
kubectl logs -n <project>-system deployment/<project>-controller-manager -c manager -f
```

### API Design

**Key markers for** `api/<version>/*_types.go`:

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// On fields:
// +kubebuilder:validation:Required
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:MaxLength=100
// +kubebuilder:validation:Pattern="^[a-z]+$"
// +kubebuilder:default="value"
```

- **Use** `metav1.Condition` for status (not custom string fields)
- **Use predefined types**: `metav1.Time` instead of `string` for dates
- **Follow K8s API conventions**: Standard field names (`spec`, `status`, `metadata`)

### Controller Design

**RBAC markers in** `internal/controller/*_controller.go`:

```go
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
```

**Implementation rules:**
- **Idempotent reconciliation**: Safe to run multiple times
- **Re-fetch before updates**: `r.Get(ctx, req.NamespacedName, obj)` before `r.Update` to avoid conflicts
- **Structured logging**: `log := log.FromContext(ctx); log.Info("msg", "key", val)`
- **Owner references**: Enable automatic garbage collection (`SetControllerReference`)
- **Watch secondary resources**: Use `.Owns()` or `.Watches()`, not just `RequeueAfter`
- **Finalizers**: Clean up external resources (buckets, VMs, DNS entries)

### Logging

**Follow Kubernetes logging message style guidelines:**

- Start from a capital letter
- Do not end the message with a period
- Active voice: subject present (`"Deployment could not create Pod"`) or omitted (`"Could not create Pod"`)
- Past tense: `"Could not delete Pod"` not `"Cannot delete Pod"`
- Specify object type: `"Deleted Pod"` not `"Deleted"`
- Balanced key-value pairs

```go
log.Info("Starting reconciliation")
log.Info("Created Deployment", "name", deploy.Name)
log.Error(err, "Failed to create Pod", "name", name)
```

**Reference:** https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines

### Webhooks
- **Create all types together**: `--defaulting --programmatic-validation --conversion`
- **When`--force`is used**: Backup custom logic first, then restore after scaffolding
- **For multi-version APIs**: Use hub-and-spoke pattern (`--conversion --spoke v2`)
  - Hub version: Usually oldest stable version (v1)
  - Spoke versions: Newer versions that convert to/from hub (v2, v3)
  - Example: `--group crew --version v1 --kind Captain --conversion --spoke v2` (v1 is hub, v2 is spoke)

### Learning from Examples

The **deploy-image plugin** scaffolds a complete controller following good practices. Use it as a reference implementation:

```bash
kubebuilder create api --group example --version v1alpha1 --kind MyApp \
  --image=<your-image> --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

Generated code includes: status conditions (`metav1.Condition`), finalizers, owner references, events, idempotent reconciliation.

## Distribution Options

### Option 1: YAML Bundle (Kustomize)

```bash
# Generate dist/install.yaml from Kustomize manifests
make build-installer IMG=<registry>/<project>:tag
```

**Key points:**
- The `dist/install.yaml` is generated from Kustomize manifests (CRDs, RBAC, Deployment)
- Commit this file to your repository for easy distribution
- Users only need `kubectl` to install (no additional tools required)

**Example:** Users install with a single command:
```bash
kubectl apply -f https://raw.githubusercontent.com/<org>/<repo>/<tag>/dist/install.yaml
```

### Option 2: Helm Chart

```bash
kubebuilder edit --plugins=helm/v2-alpha                      # Generates dist/chart/ (default)
kubebuilder edit --plugins=helm/v2-alpha --output-dir=charts  # Generates charts/chart/
```

**For development:**
```bash
make helm-deploy IMG=<registry>/<project>:<tag>          # Deploy manager via Helm
make helm-deploy IMG=$IMG HELM_EXTRA_ARGS="--set ..."    # Deploy with custom values
make helm-status                                         # Show release status
make helm-uninstall                                      # Remove release
make helm-history                                        # View release history
make helm-rollback                                       # Rollback to previous version
```

**For end users/production:**
```bash
helm install my-release ./<output-dir>/chart/ --namespace <ns> --create-namespace
```

**Important:** If you add webhooks or modify manifests after initial chart generation:
1. Backup any customizations in `<output-dir>/chart/values.yaml` and `<output-dir>/chart/manager/manager.yaml`
2. Re-run: `kubebuilder edit --plugins=helm/v2-alpha --force` (use same `--output-dir` if customized)
3. Manually restore your custom values from the backup

### Publish Container Image

```bash
export IMG=<registry>/<project>:<version>
make docker-build docker-push IMG=$IMG
```

## References

### Essential Reading
- **Kubebuilder Book**: https://book.kubebuilder.io (comprehensive guide)
- **controller-runtime FAQ**: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md (common patterns and questions)
- **Good Practices**: https://book.kubebuilder.io/reference/good-practices.html (why reconciliation is idempotent, status conditions, etc.)
- **Logging Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines (message style, verbosity levels)

### API Design & Implementation
- **API Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- **Operator Pattern**: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
- **Markers Reference**: https://book.kubebuilder.io/reference/markers.html

### Tools & Libraries
- **controller-runtime**: https://github.com/kubernetes-sigs/controller-runtime
- **controller-tools**: https://github.com/kubernetes-sigs/controller-tools
- **Kubebuilder Repo**: https://github.com/kubernetes-sigs/kubebuilder
