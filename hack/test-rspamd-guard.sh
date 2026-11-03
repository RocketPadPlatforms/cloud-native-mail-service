#!/bin/sh
# Never-unsigned guard and signing matrix (ADR 0013, section 6).
#
# Starts the pinned Rspamd image (internal/images/images.go) the way the
# manifests do (read-only root, non-root user, no capabilities), with the
# configuration of the production renderer and generated test keys
# (hack/render-rspamd-fixture), and scans messages through the normal
# worker with rspamc. Needs docker, go and jq. The container has no
# network, so no DNS or map download slows or varies a run; the only
# network use is pulling the image when it is not present.
#
# Usage: hack/test-rspamd-guard.sh   (or: make test-rspamd-guard)
set -eu

umask 022 # the container user is not the owner of the fixture files
cd "$(dirname "$0")/.."

for tool in docker go jq; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "test-rspamd-guard: $tool is required" >&2
		exit 2
	}
done

IMAGE=$(sed -n 's/^[[:space:]]*Rspamd = "\(.*\)"$/\1/p' internal/images/images.go)
[ -n "$IMAGE" ] || {
	echo "test-rspamd-guard: no Rspamd image in internal/images/images.go" >&2
	exit 2
}

# The rspamd user of the image, as in the manifests, and the test keys'
# selectors, as in hack/render-rspamd-fixture.
RSPAMD_ID=11333
R=cnms-20261103
E=cnms-ed25519-20261103
# The exact soft-reject message of the guard (ADR 0013).
DEFER_MSG='DKIM signing unavailable, try again later'

WORK=$(mktemp -d)
chmod 755 "$WORK"
PREFIX=cnms-rspamd-guard-$$
FAILS=0
CHECKS=0

cleanup() {
	docker rm -f "$PREFIX-good" "$PREFIX-broken" "$PREFIX-source" >/dev/null 2>&1 || true
	chmod -R u+rwX "$WORK" 2>/dev/null || true # the unreadable key
	rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

TIMEOUT=""
if command -v timeout >/dev/null 2>&1; then
	TIMEOUT="timeout 120"
fi

docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull -q "$IMAGE" >/dev/null

# --- fixture: rendered configuration, keys, guard variants, messages ---

go run ./hack/render-rspamd-fixture -out "$WORK/fx"
FX=$WORK/fx
# Keys that exist but that the Rspamd user may not read.
chmod 000 "$FX/dkim/unreadable.example/$R.key"

# The guard with an error in its check function (the error path), one
# with an error in its source function (the source cannot be determined),
# and two scripts that must not load.
GOOD_LUA=$FX/rspamd.local.lua
sed '/^local function check(task)$/a\
  error("guard test")' "$GOOD_LUA" >"$WORK/broken.lua"
grep -q 'error("guard test")' "$WORK/broken.lua" || {
	echo "test-rspamd-guard: guard.lua changed, cannot inject the error" >&2
	exit 2
}
sed '/^local function source(task)$/a\
  error("source test")' "$GOOD_LUA" >"$WORK/source.lua"
grep -q 'error("source test")' "$WORK/source.lua" || {
	echo "test-rspamd-guard: guard.lua changed, cannot inject the source error" >&2
	exit 2
}
{
	cat "$GOOD_LUA"
	echo 'this is not lua ('
} >"$WORK/syntax.lua"
{
	cat "$GOOD_LUA"
	echo 'error("load time error")'
} >"$WORK/loaderr.lua"

mkdir "$WORK/msgs"
chmod 755 "$WORK/msgs"
# The Date header is the time of the run: a fixed one ages into
# DATE_IN_PAST and tips unauthenticated mail over the greylist score.
DATE=$(LC_ALL=C date -u '+%a, %d %b %Y %H:%M:%S +0000')
mkmsg() { # mkmsg <file> <from-domain> [body]
	printf 'From: Bob <bob@%s>\nTo: alice@example.net\nSubject: test\nDate: %s\nMessage-ID: <%s@%s>\n\n%s\n' \
		"$2" "$DATE" "$1" "$2" "${3:-hello}" >"$WORK/msgs/$1.eml"
}
for d in dual rsa partial missing unreadable nokeys; do mkmsg "$d" "$d.example"; done
mkmsg other other.example
mkmsg sub sub.dual.example
mkmsg gtube missing.example 'XJS*C4JDBQADN1.NSBN3*2IDNEN*GTUBE-STANDARD-ANTI-UBE-TEST-EMAIL*C.34X'
mkhdr() { # mkhdr <file> <From header line>...: a message with the given From lines
	f=$1
	shift
	{
		for h in "$@"; do printf '%s\n' "$h"; done
		printf 'To: alice@example.net\nSubject: test\nDate: %s\nMessage-ID: <%s@dual.example>\n\nhello\n' "$DATE" "$f"
	} >"$WORK/msgs/$f.eml"
}
# One From header with a served and an unserved address.
mkhdr multi 'From: Bob <bob@dual.example>, Eve <eve@other.example>'
# Two From header lines, one of them served.
mkhdr twofrom 'From: Bob <bob@dual.example>' 'From: Eve <eve@other.example>'
# Several addresses, none of them in a served domain.
mkhdr multiother 'From: Bob <bob@other.example>, Eve <eve@sub.dual.example>'
# No From header at all.
mkhdr nofrom 'X-Test: no from'

# --- containers ---

# rspamd_args <lua-file>: docker run arguments of an Rspamd with the
# fixture, mounted like the manifests do (internal/manifests).
rspamd_args() {
	printf '%s\n' --network none --read-only --user "$RSPAMD_ID:$RSPAMD_ID" \
		--cap-drop ALL --security-opt no-new-privileges \
		-v "$FX/local.d:/etc/rspamd/local.d:ro" \
		-v "$1:/etc/rspamd/rspamd.local.lua:ro" \
		-v "$FX/dkim:/etc/rspamd/dkim:ro" \
		-v "$WORK/msgs:/msgs:ro" \
		--tmpfs "/var/lib/rspamd:uid=$RSPAMD_ID,gid=$RSPAMD_ID"
}

start() { # start <name> <lua-file>
	# shellcheck disable=SC2046 # the arguments hold no spaces
	docker run -d --name "$1" $(rspamd_args "$2") "$IMAGE" >/dev/null
	i=0
	until docker exec "$1" rspamc -t 5 -h 127.0.0.1:11333 /msgs/dual.eml >/dev/null 2>&1; do
		i=$((i + 1))
		if [ "$i" -gt 60 ] || [ "$(docker inspect -f '{{.State.Running}}' "$1")" != true ]; then
			docker logs --tail 40 "$1" >&2 || true
			echo "test-rspamd-guard: $1 did not become ready" >&2
			exit 1
		fi
		sleep 1
	done
}

# --- assertions ---

JQ='[ .action,
      ((.messages // {}) | [.[]] | join(" ")),
      ((.symbols.DKIM_SIGNED.options // []) | sort | join(",")),
      ([(.["dkim-signature"] // []) | if type == "array" then .[] else . end
        | capture("a=(?<a>[^;]+);").a] | sort | join(","))
    ] | join("|")'

NONE='no action|||'
# Which signatures a deferred message carries does not matter: none leaves.
DEFERRED="soft reject|$DEFER_MSG|*"
# A message with several From addresses of a served domain can never be
# signed: it is rejected, not deferred.
MULTI="reject|multiple From addresses cannot be DKIM signed|*"
# The guard did not act: no message of its own and no signature. The action
# is not pinned, because Rspamd may score such a message (MULTIPLE_FROM,
# MISSING_FROM).
UNTOUCHED='*|||'

fail() {
	FAILS=$((FAILS + 1))
	echo "FAIL: $*" >&2
}

# check <container> <label> <message> <want> [rspamc args]: scans
# /msgs/<message>.eml and compares action|message|DKIM_SIGNED|algorithms
# with <want>, a glob pattern.
check() {
	c=$1 label=$2 msg=$3 want=$4
	shift 4
	CHECKS=$((CHECKS + 1))
	raw=$(docker exec "$c" rspamc --json -t 15 -h 127.0.0.1:11333 --rcpt alice@example.net "$@" "/msgs/$msg.eml" 2>"$WORK/rspamc.err") || {
		fail "$label: rspamc failed: $raw $(cat "$WORK/rspamc.err")"
		return
	}
	got=$(printf '%s' "$raw" | jq -r "$JQ" 2>&1) || {
		fail "$label: unreadable rspamc output: $raw $(cat "$WORK/rspamc.err")"
		return
	}
	# shellcheck disable=SC2254 # want is a pattern on purpose
	case $got in
	$want) echo "ok   $label" ;;
	*) fail "$label: got '$got', want '$want'" ;;
	esac
}

# configtest <name> <lua-file> <want-exit>: rspamadm configtest of the
# fixture with the given guard script.
configtest() {
	CHECKS=$((CHECKS + 1))
	rc=0
	# shellcheck disable=SC2046,SC2086 # the arguments hold no spaces
	$TIMEOUT docker run --rm $(rspamd_args "$2") "$IMAGE" $(cat "$FX/validate.args") >"$WORK/configtest.log" 2>&1 || rc=$?
	if [ "$rc" -eq "$3" ]; then
		echo "ok   configtest $1 (exit $rc)"
	else
		tail -n 20 "$WORK/configtest.log" >&2
		fail "configtest $1: exit $rc, want $3"
	fi
}

# --- the matrix ---

configtest "good guard, keys missing or unreadable" "$GOOD_LUA" 0
configtest "guard with a syntax error" "$WORK/syntax.lua" 1
configtest "guard that fails at load time" "$WORK/loaderr.lua" 1

G=$PREFIX-good
start "$G" "$GOOD_LUA"

DUAL="dual.example:s=$R,dual.example:s=$E"
BOTH='ed25519-sha256,rsa-sha256'
IP=203.0.113.9

# Authenticated submission is signed with every active key of the domain.
check "$G" "authenticated, RSA and Ed25519" dual "no action||$DUAL|$BOTH" --ip $IP --user alice --from bob@dual.example
check "$G" "authenticated from a pod address, RSA and Ed25519" dual "no action||$DUAL|$BOTH" --ip 10.1.2.3 --user alice --from bob@dual.example
check "$G" "authenticated, RSA only" rsa "no action||rsa.example:s=$R|rsa-sha256" --ip $IP --user alice --from bob@rsa.example

# A required signature that cannot be made defers the message.
check "$G" "authenticated, one of two keys missing" partial "$DEFERRED" --ip $IP --user alice --from bob@partial.example
check "$G" "authenticated, key file missing" missing "$DEFERRED" --ip $IP --user alice --from bob@missing.example
check "$G" "authenticated, key file unreadable" unreadable "$DEFERRED" --ip $IP --user alice --from bob@unreadable.example
check "$G" "authenticated, served domain without a key" nokeys "$DEFERRED" --ip $IP --user alice --from bob@nokeys.example
check "$G" "authenticated, empty sender is not exempt" missing "$DEFERRED" --ip $IP --user alice --from '<>'

# Mail that is not signed by CNMS is left alone, never deferred.
check "$G" "unauthenticated, Internet address" dual "$NONE" --ip $IP --from bob@dual.example
check "$G" "unauthenticated, pod address (not signed)" dual "$NONE" --ip 10.1.2.3 --from bob@dual.example
check "$G" "unauthenticated, key missing (not deferred)" missing "$NONE" --ip $IP --from bob@missing.example
check "$G" "authenticated, domain not served" other "$NONE" --ip $IP --user alice --from bob@other.example
check "$G" "authenticated, subdomain of a served domain" sub "$NONE" --ip $IP --user alice --from bob@sub.dual.example

# Postfix's own non-SMTP mail comes from loopback without a login.
check "$G" "loopback IPv4, signed" dual "no action||$DUAL|$BOTH" --ip 127.0.0.1 --from bob@dual.example
check "$G" "loopback IPv6, signed" dual "no action||$DUAL|$BOTH" --ip ::1 --from bob@dual.example
check "$G" "loopback, key missing" missing "$DEFERRED" --ip 127.0.0.1 --from bob@missing.example
check "$G" "loopback, empty sender (bounce) exempt" missing "$NONE" --ip 127.0.0.1 --from '<>'

# A message that is going to be rejected is not turned into a deferral.
check "$G" "authenticated, rejected, key missing" gtube "reject|*" --ip $IP --user alice --from bob@missing.example

# Several From addresses, one of them served, can never be signed: reject.
check "$G" "authenticated, several addresses in one From" multi "$MULTI" --ip $IP --user alice --from bob@dual.example
check "$G" "authenticated, two From headers" twofrom "$MULTI" --ip $IP --user alice --from bob@dual.example
check "$G" "loopback with a sender, several From addresses" multi "$MULTI" --ip 127.0.0.1 --from bob@dual.example
# Without a served address, without a login or without a From header the
# guard does not act.
check "$G" "authenticated, several From addresses, none served" multiother "$UNTOUCHED" --ip $IP --user alice --from bob@other.example
check "$G" "unauthenticated, several From addresses" multi "$UNTOUCHED" --ip $IP --from bob@dual.example
check "$G" "authenticated, no From header" nofrom "$UNTOUCHED" --ip $IP --user alice --from bob@dual.example

# An error in the guard defers authenticated mail and loopback mail with a
# sender, and nothing else, and is logged.
B=$PREFIX-broken
start "$B" "$WORK/broken.lua"
check "$B" "guard error, authenticated" dual "$DEFERRED" --ip $IP --user alice --from bob@dual.example
check "$B" "guard error, unauthenticated untouched" dual "$NONE" --ip $IP --from bob@dual.example
check "$B" "guard error, loopback with a sender" missing "$DEFERRED" --ip 127.0.0.1 --from bob@missing.example
check "$B" "guard error, loopback empty sender untouched" missing "$NONE" --ip 127.0.0.1 --from '<>'
CHECKS=$((CHECKS + 1))
if docker logs "$B" 2>&1 | grep -q 'CNMS DKIM guard failed'; then
	echo "ok   guard error is logged"
else
	fail "guard error is not in the log of $B"
fi

# When the source of a message cannot be determined, only a login is
# deferred.
S=$PREFIX-source
start "$S" "$WORK/source.lua"
check "$S" "source error, authenticated" dual "$DEFERRED" --ip $IP --user alice --from bob@dual.example
check "$S" "source error, loopback with a sender untouched" missing "$NONE" --ip 127.0.0.1 --from bob@missing.example
check "$S" "source error, unauthenticated untouched" dual "$NONE" --ip $IP --from bob@dual.example

echo "$CHECKS checks, $FAILS failed"
if [ "$FAILS" -ne 0 ]; then
	docker logs "$G" 2>&1 | grep 'cannot load\|guard' | sort -u | tail -n 20 >&2 || true
	exit 1
fi
