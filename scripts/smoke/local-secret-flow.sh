#!/usr/bin/env bash
set -euo pipefail

api_base="${BURNLINK_API_BASE_URL:-http://localhost:8080}"

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

cat >"$tmpdir/create.json" <<'JSON'
{
  "kind": "text",
  "ciphertext": "Y2lwaGVydGV4dA==",
  "nonce": "bm9uY2U=",
  "kdf": {
    "algorithm": "PBKDF2-SHA-256",
    "salt": "c2FsdA==",
    "iterations": 600000,
    "key_length_bits": 256
  },
  "size_bytes": 10,
  "ttl_seconds": 600
}
JSON

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

consume_status="$(
	curl -fsS \
		-o "$tmpdir/consume.json" \
		-w "%{http_code}" \
		-X POST "$api_base/api/secrets/$secret_id/consume"
)"
if [ "$consume_status" != "202" ]; then
	echo "local-secret-flow: expected consume status 202, got $consume_status" >&2
	exit 1
fi

second_get_status="$(
	curl -sS \
		-o "$tmpdir/second-get.json" \
		-w "%{http_code}" \
		"$api_base/api/secrets/$secret_id"
)"
if [ "$second_get_status" != "410" ]; then
	echo "local-secret-flow: expected second get status 410, got $second_get_status" >&2
	exit 1
fi

second_consume_status="$(
	curl -sS \
		-o "$tmpdir/second-consume.json" \
		-w "%{http_code}" \
		-X POST "$api_base/api/secrets/$secret_id/consume"
)"
if [ "$second_consume_status" != "409" ]; then
	echo "local-secret-flow: expected second consume status 409, got $second_consume_status" >&2
	exit 1
fi

echo "local-secret-flow: ok"
