#!/usr/bin/env bash
set -euo pipefail

# Synthetic canary: one create -> open round trip per check (health|text|file).
# Creates are capped at 5/min per client network, so a failed attempt retries once after
# CANARY_RETRY_DELAY_SECONDS (65 s refills the bucket); never loop tightly.
# The share link carries the decryption key: never print it (mode-600 file, ::add-mask::).
# The CLI's own HTTP timeout is 30 min; worst case per check 2 x 2 x 90 s + 65 s = 425 s
# fits the 8-minute step timeout in .github/workflows/canary.yml.

base_url="${FLICK_CANARY_URL:?FLICK_CANARY_URL is required}"
base_url="${base_url%/}"
flick_bin="${FLICK_BIN:-flick}"
retry_delay="${CANARY_RETRY_DELAY_SECONDS:-65}"
cli_timeout="${CANARY_CLI_TIMEOUT_SECONDS:-90}"
# Shortest TTL the API accepts (defaultMinTTLSeconds).
ttl="5m"

check="${1:-}"

umask 077
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

log() {
	echo "canary[$check]: $*" >&2
}

# Signed presigned-PUT URLs (X-Amz-Signature) must never reach logs.
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

send_secret() {
	flick send -url "$base_url" -ttl "$ttl" "$@" >"$work/link" 2>"$work/send.err" || {
		log "send failed: $(redact "$work/send.err")"
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
	flick open "$(cat "$work/link")" >"$work/opened.txt" 2>"$work/open.err" || {
		log "open failed: $(redact "$work/open.err")"
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
	# Just past the inline threshold forces the presigned object-storage upload.
	size=$((inline_max + 65536))
	if [ "$size" -gt "$max_file" ]; then
		log "file size $size exceeds max_file_bytes $max_file; object storage is likely disabled"
		return 1
	fi

	head -c "$size" /dev/urandom >"$work/canary.bin"
	send_secret -file "$work/canary.bin" </dev/null || return 1
	flick open -output "$work/opened.bin" "$(cat "$work/link")" >/dev/null 2>"$work/open.err" || {
		log "open failed: $(redact "$work/open.err")"
		return 1
	}
	local sent_sum opened_sum
	sent_sum="$(sha256sum_of "$work/canary.bin")"
	opened_sum="$(sha256sum_of "$work/opened.bin")"
	# errexit is off inside `if run_file`, so a failed hash yields "" on both sides.
	if [ -z "$sent_sum" ] || [ -z "$opened_sum" ]; then
		log "sha256 could not be computed"
		return 1
	fi
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
