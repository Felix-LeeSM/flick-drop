#!/usr/bin/env bash
set -euo pipefail

api_base="${FLICK_API_BASE_URL:-http://localhost:8080}"

if ! curl -fsS "$api_base/healthz" >/dev/null 2>&1; then
	echo "local-secret-flow: API not reachable at $api_base; skipped"
	exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
	echo "local-secret-flow: jq not installed; skipped"
	exit 0
fi

tmpdir="$(mktemp -d)"
cleanup() {
	rm -rf "$tmpdir"
}
trap cleanup EXIT

# The API checks only the age magic line and lengths, so a fixed age-shaped
# body exercises create and the streamed open without a real encryption.
printf 'age-encryption.org/v1\nlocal-body' >"$tmpdir/body.age"
jq -n \
	--arg ciphertext "$(base64 <"$tmpdir/body.age" | tr -d '\n')" \
	--argjson size "$(wc -c <"$tmpdir/body.age" | tr -d ' ')" \
	'{kind: "text", ciphertext: $ciphertext, size_bytes: $size, ttl_seconds: 600}' \
	>"$tmpdir/create.json"

create_response="$(
	curl -fsS \
		-X POST "$api_base/api/secrets" \
		-H "Content-Type: application/json" \
		--data-binary "@$tmpdir/create.json"
)"
secret_id="$(printf '%s' "$create_response" | jq -er '.id')"

get_status="$(
	curl -fsS \
		-o "$tmpdir/get.json" \
		-w "%{http_code}" \
		"$api_base/api/secrets/$secret_id"
)"
if [ "$get_status" != "200" ]; then
	echo "local-secret-flow: expected first get status 200, got $get_status" >&2
	exit 1
fi

open_status="$(
	curl -fsS \
		-o "$tmpdir/opened.age" \
		-w "%{http_code}" \
		-H "Content-Type: application/json" \
		-d '{}' \
		-X POST "$api_base/api/secrets/$secret_id/open"
)"
if [ "$open_status" != "200" ] || ! cmp -s "$tmpdir/body.age" "$tmpdir/opened.age"; then
	echo "local-secret-flow: expected open status 200 with the stored body, got $open_status" >&2
	exit 1
fi

second_open_status="$(
	curl -sS \
		-o "$tmpdir/second-open.json" \
		-w "%{http_code}" \
		-H "Content-Type: application/json" \
		-d '{}' \
		-X POST "$api_base/api/secrets/$secret_id/open"
)"
if [ "$second_open_status" != "410" ]; then
	echo "local-secret-flow: expected second open status 410, got $second_open_status" >&2
	exit 1
fi

echo "local-secret-flow: ok"
