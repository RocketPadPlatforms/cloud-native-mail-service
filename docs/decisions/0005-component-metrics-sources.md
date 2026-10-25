# ADR 0005: Component metrics sources

- **Status:** Accepted
- **Date:** 2026-10-25

## Context

Section 7 of the project plan requires every component to expose
Prometheus-format metrics on a named port with a Service, and the
operator to integrate with the Prometheus Operator when its CRDs are
present. Metric sources per component, verified on 2026-10-25:

- **Postfix** has no native metrics endpoint. Queue state (length,
  age, deferred) is readable via `postqueue -j`; delivery/deferral/
  bounce counters require log parsing, and CNMS logs to stdout, not
  journald.
- Existing Postfix exporters, evaluated:
  - `kumina/postfix_exporter` (de facto standard): queue size/age
    metrics fit, but its delivery counters are parsed from the systemd
    journal, which CNMS does not use; queue access assumes the
    exporter can see the spool, which is a per-replica PVC.
  - `vinted/postfix-exporter`: queue counters only.
  - `prometheus-postfix-exporter` (Debian package): marked for
    autoremoval from trixie; upstream unmaintained.
  None fits the stdout-logging + queue-PVC setup well enough to adopt
  as-is, which is the fallback the project plan anticipated ("write a
  minimal CNMS sidecar exporter").
- **Rspamd 4.2**: the `metric_exporter` module is Graphite push-only;
  the Prometheus endpoint is served by the controller worker at
  `GET /metrics` (OpenMetrics), verified against the 4.2 protocol
  documentation.
- **Valkey** has no native Prometheus endpoint; `oliver006/redis_exporter`
  is maintained, MIT licensed, and explicitly supports Valkey 9.x
  (v1.96.0 as of 2026-10-25).
- **Dovecot 2.4** has a native OpenMetrics stats service; it is used
  from Phase 4 (Dovecot metrics), not Phase 2.

## Decision

1. **Postfix queue metrics:** a minimal CNMS sidecar inside the
   Postfix image (ADR-0004). A shell loop runs `postqueue -j` as the
   `postfix` user against the shared queue volume every 30 seconds and
   writes a Prometheus text file (queue sizes per queue type
   incoming/active/deferred/hold, oldest message age derived from the
   queue-ID creation timestamp); `busybox httpd` (ISC, installed in
   the image) serves it on named port `postfix-metrics` (9150).
   No new image, no Go binary, license-clean.
   Delivery/deferral/bounce counters from the logs are deferred to
   Phase 9 (full metrics set); this ADR is revisited then — candidates
   are extending the sidecar to parse stdout or adopting a maintained
   exporter at that time.
2. **Rspamd metrics:** expose the controller worker's `/metrics`
   (OpenMetrics) on named port `rspamd-metrics` (11334, the Rspamd
   4.x controller worker default; verified against the 4.2.3
   configuration) with a Service. No controller password in v1: each
   `MailServer` has its own Rspamd set, access is restricted by the
   NetworkPolicies of ADR-0006, and the metrics path is the only one
   the operator needs.
   The write endpoints of the controller are reachable in-cluster;
   this trade-off is documented in the observability docs.
3. **Valkey metrics:** `oliver006/redis_exporter` v1.96.0 (MIT) as a
   sidecar on the Valkey pod, targeting
   `redis://127.0.0.1:6379`, on named port `valkey-metrics`
   (9121).
4. **Integration model:** every metrics endpoint gets a Service. The
   operator checks at startup and periodically (API discovery) whether
   `monitoring.coreos.com/v1` CRDs are available; if available and
   `MailServer` observability settings allow (default `auto`), it
   creates the corresponding ServiceMonitor/PodMonitor; otherwise it
   sets a condition explaining why and continues normally. CRDs
   installed later are picked up without restarting the operator.
   Alert rules (PrometheusRule) and dashboards stay in Phase 9.

## Consequences

- Postfix queue metrics have a 30s sampling granularity; bursty queue
  spikes between samples are not visible (documented).
- The Rspamd controller endpoint is unauthenticated in-cluster; the
  NetworkPolicies are the only access control. A per-MailServer
  controller password or a dedicated metrics-only listener can be
  added later (additive).
- The `redis_exporter` license (MIT) was checked at the pinned
  version; Renovate PRs must re-check it on major updates (section 9).
- Queue metrics are per replica; a scrape of the headless Service
  yields one series set per Postfix pod (labels distinguish them).
