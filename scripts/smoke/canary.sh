#!/usr/bin/env bash
set -euo pipefail

# External synthetic canary: one real create -> open round trip against a live
# Flick deployment with the flick CLI. .github/workflows/canary.yml runs each
# check as its own step on a schedule; it also runs by hand:
#
#   go build -o /tmp/flick ./cmd/flick
#   FLICK_CANARY_URL=https://flick.example FLICK_BIN=/tmp/flick \
#     bash scripts/smoke/canary.sh text
#
# Checks:
#   health  GET /healthz on the public origin answers 2xx (no secret created).
#   text    random text secret -> open -> byte-exact compare (inline path).
#   file    random file just above /api/config payload_inline_max_bytes ->
#           open -> sha256 compare (presigned object-storage upload path).
#
# Each text/file run creates exactly one secret per attempt and opens it at
# once, which consumes it, so nothing is left behind. The deployment limits
# creates to 5 per minute per client network
# (internal/config/defaults.go:defaultCreateRatePerMinute), so a failed attempt
# is retried once, after CANARY_RETRY_DELAY_SECONDS (default 65 s, past the
# one-minute window), and never in a tight loop. Two checks with one retry each
# stay at 4 creates or fewer per run.
#
# The share link carries the decryption key in its fragment, so it is never
# printed: it lives in a mode-600 temp file, and under GitHub Actions it is
# registered with ::add-mask:: before anything else could echo it.

base_url="${FLICK_CANARY_URL:?FLICK_CANARY_URL is required}"
base_url="${base_url%/}"
flick_bin="${FLICK_BIN:-flick}"
retry_delay="${CANARY_RETRY_DELAY_SECONDS:-65}"
# 300 s is the shortest TTL the API accepts
# (internal/config/defaults.go:defaultMinTTLSeconds).
ttl="5m"

check="${1:-}"

umask 077
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

log() {
	echo "canary[$check]: $*" >&2
}

# send_secret writes the share link to $work/link. Remaining args go to
# `flick send`; stdin is passed through.
send_secret() {
	"$flick_bin" send -url "$base_url" -ttl "$ttl" "$@" >"$work/link" 2>"$work/send.err" || {
		log "send failed: $(cat "$work/send.err")"
		return 1
	}
	local link
	link="$(cat "$work/link")"
	if [ -z "$link" ]; then
		log "send printed no link"
		return 1
	fi
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		echo "::add-mask::$link"
	fi
}

run_text() {
	head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$work/sent.txt"
	send_secret <"$work/sent.txt" || return 1
	"$flick_bin" open "$(cat "$work/link")" >"$work/opened.txt" 2>"$work/open.err" || {
		log "open failed: $(cat "$work/open.err")"
		return 1
	}
	if ! cmp -s "$work/sent.txt" "$work/opened.txt"; then
		log "opened text differs from sent text"
		return 1
	fi
	log "ok ($(wc -c <"$work/sent.txt" | tr -d ' ') bytes, inline)"
}

run_file() {
	local config inline_max max_file size
	config="$(curl -fsS --max-time 20 "$base_url/api/config")" || {
		log "GET /api/config failed"
		return 1
	}
	inline_max="$(printf '%s' "$config" | sed -n 's/.*"payload_inline_max_bytes":\([0-9]*\).*/\1/p')"
	max_file="$(printf '%s' "$config" | sed -n 's/.*"max_file_bytes":\([0-9]*\).*/\1/p')"
	if [ -z "$inline_max" ] || [ -z "$max_file" ]; then
		log "/api/config lacks payload_inline_max_bytes or max_file_bytes"
		return 1
	fi
	# Just past the inline threshold forces the presigned object-storage upload
	# (internal/flickcli/send.go); 64 KiB keeps the transfer small.
	size=$((inline_max + 65536))
	if [ "$size" -gt "$max_file" ]; then
		log "file size $size exceeds max_file_bytes $max_file; object storage is likely disabled"
		return 1
	fi

	head -c "$size" /dev/urandom >"$work/canary.bin"
	send_secret -file "$work/canary.bin" </dev/null || return 1
	"$flick_bin" open -output "$work/opened.bin" "$(cat "$work/link")" >/dev/null 2>"$work/open.err" || {
		log "open failed: $(cat "$work/open.err")"
		return 1
	}
	local sent_sum opened_sum
	sent_sum="$(sha256sum_of "$work/canary.bin")"
	opened_sum="$(sha256sum_of "$work/opened.bin")"
	if [ "$sent_sum" != "$opened_sum" ]; then
		log "opened file sha256 $opened_sum differs from sent $sent_sum"
		return 1
	fi
	log "ok ($size bytes, object storage, sha256 match)"
}

sha256sum_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

run_health() {
	curl -fsS --max-time 20 -o /dev/null "$base_url/healthz" || {
		log "GET /healthz failed"
		return 1
	}
	log "ok"
}

case "$check" in
health)
	run_health
	exit
	;;
text | file) ;;
*)
	echo "usage: canary.sh health|text|file" >&2
	exit 2
	;;
esac

if "run_$check"; then
	exit 0
fi
log "retrying once in ${retry_delay}s"
sleep "$retry_delay"
rm -f "$work"/*
"run_$check"
