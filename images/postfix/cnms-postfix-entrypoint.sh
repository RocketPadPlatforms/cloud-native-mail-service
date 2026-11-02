#!/bin/sh
# CNMS Postfix entrypoint (ADR 0004): run the mail system and stop it
# cleanly on SIGTERM/SIGINT (pod termination).
#
# postfix(1) is superuser-only; the master drops privileges to mail_owner
# for all daemons. `postfix start-fg` runs the master with -s, which keeps
# its stdout open, so postlogd(8) writes the daemon logs to the pod stdout
# (maillog_file=/dev/stdout); plain `postfix start` (master -w) would
# discard the master's stdout to /dev/null and the daemon logs would be
# lost. The master never forks, so this shell runs it in the background
# and blocks in `wait`. (In the pod the shell is not PID 1: the pod
# shares one process namespace, so the pause container is. The runtime
# still delivers the termination signal to this shell, the container's
# main process.) A trapped signal interrupts `wait`
# immediately (POSIX), so SIGTERM is handled without polling. If the
# master dies on its own the script exits non-zero (k8s restart).
set -eu

postfix start-fg &
pid=$!

# `postfix stop` signals the master and returns; wait for it to finish
# its shutdown. The master's own exit status is irrelevant after a
# requested stop, so `wait` must not fail the handler.
trap 'postfix stop; wait "$pid" || :; exit 0' TERM INT

# Same here: the status of `wait` is not interesting, the master is gone.
wait "$pid" || :
echo 'master stopped; exiting for restart' >&2
exit 1
