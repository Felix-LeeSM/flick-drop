#!/usr/bin/env bash
set -euo pipefail

# Live relay smoke: one file round trip against a running deployment.
# The sender prints the 6-digit code and waits; this script joins as the only
# receiver, so -confirm auto picks exactly that device. The relay holds
# nothing durable and the room lives 3 minutes, so a failed run leaves
# nothing behind. The 6-digit code routes the room: keep it out of logs
# (::add-mask::), and never print the received payload.

base_url="${FLICK_LIVE_URL:?FLICK_LIVE_URL is required}"
base_url="${base_url%/}"
flick_bin="${FLICK_BIN:-flick}"
code_wait_seconds="${LIVE_CODE_WAIT_SECONDS:-30}"
cli_timeout="${LIVE_CLI_TIMEOUT_SECONDS:-90}"

umask 077
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

log() {
	echo "live: $*" >&2
}

# Error output may name the deployment; signed URLs must never reach logs.
redact() {
	sed -E 's#https?://[^" ]+#<url>#g' "$1"
}

# macOS ships no coreutils timeout, so a local run there goes uncapped.
flick() {
	if command -v timeout >/dev/null 2>&1; then
		local rc=0
		timeout "$cli_timeout" "$flick_bin" "$@" || rc=$?
		if [ "$rc" -eq 124 ]; then
			echo "timed out after ${cli_timeout}s" >&2
		fi
		return "$rc"
	else
		"$flick_bin" "$@"
	fi
}

expected="$work/expected.txt"
printf 'live smoke payload\n' > "$expected"

log "starting the sender"
flick live -url "$base_url" -file "$expected" -confirm auto \
	>"$work/code" 2>"$work/live.err" &
sender=$!

code=""
waited=0
while [ "$waited" -lt $((code_wait_seconds * 2)) ]; do
	if [ -s "$work/code" ]; then
		code="$(head -n1 "$work/code")"
		break
	fi
	if ! kill -0 "$sender" 2>/dev/null; then
		break
	fi
	sleep 0.5
	waited=$((waited + 1))
done

if [ -z "$code" ]; then
	wait "$sender" || true
	log "the sender never printed a code"
	redact "$work/live.err" >&2
	exit 1
fi
if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
	echo "::add-mask::$code"
fi
log "joining with the 6-digit code"

if ! flick join -url "$base_url" -output "$work/received.txt" "$code" 2>"$work/join.err"; then
	log "join failed"
	redact "$work/join.err" >&2
	kill "$sender" 2>/dev/null || true
	wait "$sender" 2>/dev/null || true
	exit 1
fi

if ! wait "$sender"; then
	log "the sender failed"
	redact "$work/live.err" >&2
	exit 1
fi

if ! cmp -s "$expected" "$work/received.txt"; then
	log "the received bytes differ from what was sent"
	exit 1
fi
if ! grep -q "Sent to the device" "$work/live.err"; then
	log "the sender did not report a completed transfer"
	redact "$work/live.err" >&2
	exit 1
fi

log "live transfer round trip ok"
