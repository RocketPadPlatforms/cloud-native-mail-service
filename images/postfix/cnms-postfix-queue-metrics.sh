#!/bin/sh
# CNMS Postfix queue-metrics sidecar (ADR 0005): samples `postqueue -j`
# (JSON lines, one object per queue file) every 30 seconds and serves a
# Prometheus text file via busybox httpd.
#
# Metrics (no sender/recipient labels, per section 7):
#   cnms_postfix_queue_up                (1 = last sample succeeded)
#   cnms_postfix_queue_messages_total
#   cnms_postfix_queue_messages{queue="<queue_name>"}
#   cnms_postfix_queue_bytes_total
#   cnms_postfix_queue_oldest_message_age_seconds
#   cnms_postfix_queue_bytes{queue="<queue_name>"}
#
# A failed sample (master not running, unreadable queue, jq error) sets
# cnms_postfix_queue_up to 0 and keeps serving the last good values, so
# an outage is never reported as an empty queue.
#
# httpd serves no Content-Type for a file without an extension and
# Prometheus 3 rejects such a scrape, so the file is served as
# /metrics/index.txt (`.txt` maps to text/plain in httpd.conf); a
# request for /metrics is redirected to /metrics/ (scrapers follow it).
set -eu

PORT="${CNMS_QUEUE_METRICS_PORT:-9150}"
WORKDIR=/tmp/cnms-queue-metrics
# Only DOCROOT is served; the httpd config stays outside it.
DOCROOT="$WORKDIR/www"
mkdir -p "$DOCROOT/metrics"
printf 'I:index.txt\n.txt:text/plain\n' > "$WORKDIR/httpd.conf"

# Values of the last successful sample ("" until the first one).
last_body=""

# sample prints the metric lines for the queue listing on stdin.
sample() {
    jq -rs --argjson now "$1" '
        . as $q
        | ($q | length) as $total
        | (if $total > 0 then ($q | map(.arrival_time) | min) else null end) as $oldest
        | (if $total > 0 then ($q | map(.message_size) | add) else 0 end) as $bytes
        | ( $q | group_by(.queue_name)
            | map({ key: .[0].queue_name, value: length })
            | from_entries ) as $byqueue
        | ( [ $q | group_by(.queue_name)[]
              | { key: .[0].queue_name, value: (map(.message_size) | add) } ]
            | from_entries ) as $bytesbyqueue
        | [
            "# HELP cnms_postfix_queue_messages_total Number of messages in the Postfix queue.",
            "# TYPE cnms_postfix_queue_messages_total gauge",
            "cnms_postfix_queue_messages_total \($total)",
            "# HELP cnms_postfix_queue_messages Number of messages per Postfix queue.",
            "# TYPE cnms_postfix_queue_messages gauge",
            ( $byqueue | to_entries[] | "cnms_postfix_queue_messages{queue=\"\(.key)\"} \(.value)" ),
            "# HELP cnms_postfix_queue_bytes_total Total size in bytes of the messages in the Postfix queue.",
            "# TYPE cnms_postfix_queue_bytes_total gauge",
            "cnms_postfix_queue_bytes_total \($bytes)",
            "# HELP cnms_postfix_queue_oldest_message_age_seconds Age of the oldest message in the Postfix queue in seconds.",
            "# TYPE cnms_postfix_queue_oldest_message_age_seconds gauge",
            (if $oldest == null then "cnms_postfix_queue_oldest_message_age_seconds 0"
             else "cnms_postfix_queue_oldest_message_age_seconds \($now - $oldest)" end),
            "# HELP cnms_postfix_queue_bytes Size in bytes of the messages per Postfix queue.",
            "# TYPE cnms_postfix_queue_bytes gauge",
            ( $bytesbyqueue | to_entries[] | "cnms_postfix_queue_bytes{queue=\"\(.key)\"} \(.value)" )
          ]
        | .[]
    '
}

generate() {
    up=1
    # No pipe: a failing postqueue must not be swallowed by jq's exit
    # status. An empty queue prints nothing and exits 0.
    if listing="$(postqueue -j 2>/dev/null)" \
        && body="$(printf '%s\n' "$listing" | sample "$(date +%s)")"; then
        last_body="$body"
    else
        up=0
    fi
    {
        echo "# HELP cnms_postfix_queue_up Whether the last Postfix queue sample succeeded."
        echo "# TYPE cnms_postfix_queue_up gauge"
        echo "cnms_postfix_queue_up $up"
        [ -z "$last_body" ] || printf '%s\n' "$last_body"
    } > "$DOCROOT/metrics/.index.tmp"
    mv "$DOCROOT/metrics/.index.tmp" "$DOCROOT/metrics/index.txt"
}

generate
while :; do
    sleep 30
    generate || echo "could not write the metrics file" >&2
done &

exec busybox httpd -f -p "0.0.0.0:${PORT}" -h "$DOCROOT" -c "$WORKDIR/httpd.conf"
