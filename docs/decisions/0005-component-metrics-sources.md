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
   The sidecar mounts the queue volume read-only (postqueue only talks
   to the showq socket). `postqueue` runs on its own, not in a pipe:
   a failed sample sets `cnms_postfix_queue_up 0` and keeps the last
   good values instead of reporting an empty queue; a successful one
   sets `cnms_postfix_queue_up 1`. The file is replaced atomically
   and served with `Content-Type: text/plain` (Prometheus 3 rejects a
   missing type). busybox httpd derives the type from the file
   extension, so the file is `/metrics/index.txt` and a scrape of
   `/metrics` follows a redirect to `/metrics/`; the scrape path
   stays the default `/metrics`.
   No new image, no Go binary, license-clean.
   Delivery/deferral/bounce counters from the logs are deferred to
   Phase 9 (full metrics set); this ADR is revisited then — candidates
   are extending the sidecar to parse stdout or adopting a maintained
   exporter at that time.
2. **Rspamd metrics:** expose the controller worker's `/metrics`
   (OpenMetrics) on named port `rspamd-metrics` (11334, the Rspamd
   4.x controller worker default; verified against the 4.2.3
   configuration) with a Service. The original assumption (a
   controller without a password serves `/metrics` to everyone) did
   not hold, see the correction in the consequences.
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
- Correction (verified against Rspamd 4.2.3, `src/controller.c` and a
  running 4.2.3 container): `password = null` does not give remote
  clients read-only access. The shipped `secure_ip` (127.0.0.1, ::1)
  stays in effect, and every other client gets 401 on all endpoints
  including `/metrics` (only `/ping` answers). The learn, fuzzy and
  map endpoints are therefore closed to other pods, but the Rspamd
  `/metrics` cannot be scraped from another pod yet, so the Rspamd
  ServiceMonitor target is down. Serving it safely needs a decision
  (password in a per-server Secret, or a metrics sidecar on
  loopback); the options are in `docs/backlog.md`. Dropping
  `secure_ip` to make `/metrics` public would also expose the
  read-only endpoints (history, statistics) of every tenant to any
  pod and is not an option.
- The `redis_exporter` license (MIT) was checked at the pinned
  version; Renovate PRs must re-check it on major updates (section 9).
- Queue metrics are per replica; a scrape of the headless Service
  yields one series set per Postfix pod (labels distinguish them).
