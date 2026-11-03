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
   queue-ID creation timestamp); `busybox httpd` (GPL-2.0-only,
   installed in the image, see the license note) serves it on named
   port `postfix-metrics` (9150).
   The sidecar mounts the queue volume read-only (postqueue only talks
   to the showq socket). `postqueue` runs on its own, not in a pipe:
   a failed sample sets `cnms_postfix_queue_up 0` and keeps the last
   good values instead of reporting an empty queue; a successful one
   sets `cnms_postfix_queue_up 1`. The file is replaced atomically
   and served with `Content-Type: text/plain` (Prometheus 3 rejects a
   missing type). busybox httpd derives the type from the file
   extension, so the file is `/metrics/index.txt` and a scrape of
   `/metrics` follows a redirect to `/metrics/`; the scrape path
   stays the default `/metrics`. The document root is a `www`
   directory that holds only `metrics/`; the httpd config and working
   files live next to it, so nothing else is served (the Rspamd
   sidecar follows the same layout).
   No new image, no Go binary, license-clean.
   Delivery/deferral/bounce counters from the logs are deferred to
   Phase 9 (full metrics set); this ADR is revisited then — candidates
   are extending the sidecar to parse stdout or adopting a maintained
   exporter at that time.
2. **Rspamd metrics:** a loopback sidecar `rspamd-metrics` in the
   Rspamd pod. Every 15 seconds it runs `busybox wget` against
   `http://127.0.0.1:11334/metrics` (the controller worker; loopback
   clients need no password) and serves the copy on named port
   `rspamd-metrics` (9151) with a Service, in the same way as the
   Postfix sidecar (atomic replace, `text/plain` via
   `/metrics/index.txt`, redirect from `/metrics`). The copy is
   prefixed with the gauge `cnms_rspamd_metrics_up` (1 after a
   successful fetch, 0 after a failed one, in which case the last
   good body is kept), so stale data is detectable. The controller
   port (11334) stays loopback-only: it is no Service port and not in
   the NetworkPolicy; the kubelet probes still reach it. The sidecar
   is a shell script inlined in the pod template (no image rebuild),
   runs as `nobody` with a read-only root FS, all capabilities
   dropped and an `emptyDir` on `/tmp`. It uses the CNMS Postfix
   image because that already ships `busybox` (wget, httpd), which
   the Rspamd image lacks; the Rspamd pod therefore depends on the
   Postfix image tag. The sidecar is metrics only: a request to its
   port cannot reach any controller endpoint.
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
- Rspamd access model (verified against Rspamd 4.2.3,
  `src/controller.c` and a running 4.2.3 container): the shipped
  `secure_ip` (127.0.0.1, ::1) stays in effect and no password is
  configured (`password = null` removes the shipped default), so
  loopback clients are trusted and every other client gets 401 on all
  endpoints including `/metrics` (only `/ping` answers). Removing
  `secure_ip` would open every endpoint to any pod, including
  `/learnspam`, `/fuzzyadd` and `/savemap`, and is not an option;
  `/metrics` on the normal worker (11333) is unauthenticated but
  cannot be separated from the scan API, so that port stays closed in
  the NetworkPolicy. The sidecar is the only way to the metrics.
- The Rspamd history is disabled (`history_redis.conf` with
  `enabled = false`, `options.inc` with `history_rows = 0`). With
  Valkey configured, the history stores sender, recipients, subject,
  Message-ID, client IP and authenticated user of every message
  without an expiry, which the controller serves to loopback clients
  and the Valkey data keeps. The WebUI history is therefore
  unavailable (the endpoint answers 500). To enable it later, render
  `history_redis.conf` with `expire` (seconds; the module default is
  no expiry), `nrows` and optionally `subject_privacy = true`
  (options verified in the 4.2.3 `history_redis` module), and a
  non-zero `history_rows` in `options.inc`, after a retention
  decision for that personal data. There is no API field for it
  yet (backlog).
- Rspamd metrics have a 15s granularity and one extra container per
  Rspamd pod.
- **Licence note (resolved).** `busybox` is licensed GPL-2.0-only
  (upstream `LICENSE`; the Debian `copyright` file of the installed
  1:1.37.0 package says GPL version 2). BusyBox runs unmodified as a
  separate program in the CNMS Postfix image, and the Debian base
  image carries more GPL software. The licence policy (section 9 of
  the project plan, ADR 0001) allows strong copyleft for exactly this
  case: unmodified, separate programs inside images, never linked into
  the operator binary. BusyBox and the Debian base packages are listed
  in `NOTICE` and on the data-plane images page
  (`docs/reference/data-plane-licenses.md`), which also states where
  the Debian package sources come from.
- The `redis_exporter` license (MIT) was checked at the pinned
  version; Renovate PRs must re-check it on major updates (section 9).
- Queue metrics are per replica; a scrape of the headless Service
  yields one series set per Postfix pod (labels distinguish them).
