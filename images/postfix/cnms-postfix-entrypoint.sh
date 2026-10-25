#!/bin/sh
# CNMS Postfix entrypoint (ADR 0004): run the mail system and stop it
# cleanly on SIGTERM/SIGINT (pod termination).
#
# postfix(1) is superuser-only; the master drops privileges to mail_owner
# for all daemons. `postfix start-fg` runs the master with -s, which keeps
# its stdout open, so postlogd(8) writes the daemon logs to the pod stdout
# (maillog_file=/dev/stdout); plain `postfix start` (master -w) would
# discard the master's stdout to /dev/null and the daemon logs would be
# lost. The master never forks, so this shell (PID 1) runs it in the
# background and polls it in a short sleep loop. The loop exists because
# dash defers signal traps until the current foreground command finishes,
# so a long blocking foreground wait would swallow SIGTERM; the 1s loop
# bounds the signal latency, and the liveness check exits non-zero
# (k8s restart) if the master dies on its own.
set -eu

trap 'postfix stop -q 2>/dev/null || true; exit 0' TERM INT

postfix start-fg &

while :; do
    sleep 1
    # master -t: non-zero exit = lock held = the master is running.
    /usr/libexec/postfix/master -t >/dev/null 2>&1 || continue
    echo 'master stopped; exiting for restart' >&2
    exit 1
done
