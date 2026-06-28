#!/usr/bin/env bash
set -euo pipefail

# Asserts the web app's security response headers are present on the HTML
# response. Run against the nginx-served build (a deployed pod, port-forward, or
# the ingress) — the vite dev server does not emit these. Skips when the target
# is unreachable so it is safe to call from any environment.
#
#   FLICK_WEB_BASE_URL=http://127.0.0.1:18091 bash scripts/smoke/web-headers.sh

web_base="${FLICK_WEB_BASE_URL:-http://flick.localhost}"

if ! curl -fsS "$web_base/healthz" >/dev/null 2>&1; then
	echo "web-headers: web not reachable at $web_base; skipped"
	exit 0
fi

headers="$(curl -fsS -D - -o /dev/null "$web_base/")"

fail=0
require() {
	local name="$1"
	local pattern="$2"
	if printf '%s' "$headers" | grep -iqE "$pattern"; then
		echo "web-headers: ok   $name"
	else
		echo "web-headers: MISSING $name" >&2
		fail=1
	fi
}

require "Content-Security-Policy"      '^content-security-policy:'
require "CSP frame-ancestors 'none'"   "content-security-policy:.*frame-ancestors 'none'"
require "CSP connect-src 'self'"       "content-security-policy:.*connect-src 'self'"
require "X-Frame-Options: DENY"        '^x-frame-options:[[:space:]]*DENY'
require "X-Content-Type-Options"       '^x-content-type-options:[[:space:]]*nosniff'
require "Referrer-Policy"              '^referrer-policy:[[:space:]]*no-referrer'
require "Permissions-Policy"           '^permissions-policy:'

# connect-src is the exfiltration control; a wildcard or scheme-only source
# (`*`, `http:`, `https:`) would defeat it. Concrete origins (scheme://host) pass.
connect_src="$(printf '%s' "$headers" | grep -ioE 'connect-src[^;]*' || true)"
if printf '%s' "$connect_src" | grep -qE '(^|[[:space:]])(\*|https?:)([[:space:]]|$)'; then
	echo "web-headers: OVER-PERMISSIVE connect-src ($connect_src)" >&2
	fail=1
else
	echo "web-headers: ok   connect-src not wildcarded"
fi

if [ "$fail" -ne 0 ]; then
	echo "web-headers: one or more security headers are missing" >&2
	exit 1
fi
echo "web-headers: all security headers present"
