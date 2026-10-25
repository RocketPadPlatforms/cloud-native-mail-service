#!/bin/sh
# CNMS Postfix queue-metrics sidecar (ADR 0005): samples `postqueue -j`
# (JSON lines, one object per queue file) every 30 seconds and serves a
# Prometheus text file via busybox httpd.
#
# Metrics (no sender/recipient labels, per section 7):
#   cnms_postfix_queue_messages_total
#   cnms_postfix_queue_messages{queue="<queue_name>"}
#   cnms_postfix_queue_bytes_total
#   cnms_postfix_queue_oldest_message_age_seconds
set -eu

PORT="${CNMS_QUEUE_METRICS_PORT:-9150}"
DOCROOT=/tmp/cnms-queue-metrics
mkdir -p "$DOCROOT"

generate() {
    now="$(date +%s)"
    # `postqueue -j` fails while master is not running; empty input then
    # yields a zero-valued file instead of an error. -s (slurp) makes the
    # program run once over the whole input, even when it is empty.
    postqueue -j 2>/dev/null | jq -rs --argjson now "$now" '
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
    ' > "$DOCROOT/metrics"
}

generate
while :; do
    sleep 30
    generate || :
done &

busybox httpd -f -p "0.0.0.0:${PORT}" -h "$DOCROOT"
