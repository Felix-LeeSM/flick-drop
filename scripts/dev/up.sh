#!/usr/bin/env bash
set -euo pipefail

export BURNLINK_ENV="${BURNLINK_ENV:-development}"
export BURNLINK_LOG_LEVEL="${BURNLINK_LOG_LEVEL:-debug}"
export BURNLINK_PUBLIC_BASE_URL="${BURNLINK_PUBLIC_BASE_URL:-http://localhost:5173}"
export PUBLIC_BURNLINK_API_BASE_URL="${PUBLIC_BURNLINK_API_BASE_URL:-http://localhost:8080}"
export BURNLINK_API_BASE_URL="${BURNLINK_API_BASE_URL:-http://localhost:8080}"
export BURNLINK_INTERNAL_API_BASE_URL="${BURNLINK_INTERNAL_API_BASE_URL:-http://localhost:8080}"
export BURNLINK_INTERNAL_TOKEN="${BURNLINK_INTERNAL_TOKEN:-change-me-local}"
export BURNLINK_API_ADDR="${BURNLINK_API_ADDR:-:8080}"
export BURNLINK_NATS_URL="${BURNLINK_NATS_URL:-nats://127.0.0.1:4222}"
export BURNLINK_NATS_STREAM="${BURNLINK_NATS_STREAM:-BURNLINK_JOBS}"
export BURNLINK_NATS_JOB_SUBJECT="${BURNLINK_NATS_JOB_SUBJECT:-burnlink.jobs}"
export BURNLINK_DATA_DIR="${BURNLINK_DATA_DIR:-./var}"
export BURNLINK_API_DB_PATH="${BURNLINK_API_DB_PATH:-./var/api.db}"
export BURNLINK_WORKER_DB_PATH="${BURNLINK_WORKER_DB_PATH:-./var/worker.db}"
export BURNLINK_PAYLOAD_INLINE_MAX_BYTES="${BURNLINK_PAYLOAD_INLINE_MAX_BYTES:-1048576}"
export BURNLINK_MAX_FILE_BYTES="${BURNLINK_MAX_FILE_BYTES:-26214400}"
export BURNLINK_DEFAULT_TTL_SECONDS="${BURNLINK_DEFAULT_TTL_SECONDS:-3600}"
export BURNLINK_ALLOWED_TTL_SECONDS="${BURNLINK_ALLOWED_TTL_SECONDS:-600,3600,86400}"
export BURNLINK_STORAGE_LARGE_BACKEND="${BURNLINK_STORAGE_LARGE_BACKEND:-disabled}"
export BURNLINK_WORKER_ID="${BURNLINK_WORKER_ID:-local-worker-1}"
export BURNLINK_WORKER_CONCURRENCY="${BURNLINK_WORKER_CONCURRENCY:-2}"

compose_project="${BURNLINK_DEV_COMPOSE_PROJECT:-burnlink-dev}"
nats_monitor_url="${BURNLINK_DEV_NATS_MONITOR_URL:-http://127.0.0.1:8222/varz}"
nats_started=0
stopping=0
pids=()
names=()

cleanup() {
	local exit_code="$1"
	if [ "$stopping" -eq 1 ]; then
		return
	fi
	stopping=1

	echo "dev: stopping local services"
	for pid in "${pids[@]}"; do
		if kill -0 "$pid" >/dev/null 2>&1; then
			kill -TERM "$pid" >/dev/null 2>&1 || true
		fi
	done
	for pid in "${pids[@]}"; do
		wait "$pid" >/dev/null 2>&1 || true
	done

	if [ "$nats_started" -eq 1 ]; then
		docker compose -p "$compose_project" down >/dev/null 2>&1 || true
	fi
	exit "$exit_code"
}

trap 'cleanup 130' INT
trap 'cleanup 143' TERM
trap 'cleanup $?' EXIT

wait_for_http() {
	local name="$1"
	local url="$2"
	local pid="${3:-}"

	for _ in $(seq 1 60); do
		if curl -fsS "$url" >/dev/null 2>&1; then
			echo "dev: $name ready at $url"
			return 0
		fi
		if [ -n "$pid" ] && ! kill -0 "$pid" >/dev/null 2>&1; then
			wait "$pid" || true
			echo "dev: $name stopped before becoming ready" >&2
			return 1
		fi
		sleep 1
	done

	echo "dev: $name did not become ready at $url" >&2
	return 1
}

ensure_nats() {
	if [ "${BURNLINK_DEV_SKIP_NATS:-}" = "1" ]; then
		echo "dev: skipping NATS startup because BURNLINK_DEV_SKIP_NATS=1"
		return
	fi

	if curl -fsS "$nats_monitor_url" >/dev/null 2>&1; then
		echo "dev: using existing NATS at $nats_monitor_url"
		return
	fi

	if ! command -v docker >/dev/null 2>&1; then
		echo "dev: docker is required to start local NATS" >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "dev: docker daemon is not available" >&2
		exit 1
	fi

	echo "dev: starting NATS"
	docker compose -p "$compose_project" up -d nats
	nats_started=1
	wait_for_http "nats" "$nats_monitor_url"
}

start_process() {
	local name="$1"
	shift

	echo "dev: starting $name"
	"$@" &
	pids+=("$!")
	names+=("$name")
}

install_web_deps() {
	if [ -d web/node_modules ]; then
		return
	fi

	echo "dev: installing web dependencies"
	pnpm --dir web install
}

monitor_processes() {
	while true; do
		for index in "${!pids[@]}"; do
			local pid="${pids[$index]}"
			local name="${names[$index]}"
			if ! kill -0 "$pid" >/dev/null 2>&1; then
				if wait "$pid"; then
					echo "dev: $name stopped"
					cleanup 0
				else
					local status="$?"
					echo "dev: $name failed with status $status" >&2
					cleanup "$status"
				fi
			fi
		done
		sleep 1
	done
}

ensure_nats
install_web_deps

start_process "api" go run ./cmd/burnlink-api
wait_for_http "api" "${BURNLINK_API_BASE_URL%/}/healthz" "${pids[0]}"

start_process "worker" go run ./cmd/burnlink-worker
start_process "web" pnpm --dir web dev --host 127.0.0.1
wait_for_http "web" "$BURNLINK_PUBLIC_BASE_URL" "${pids[2]}"

echo "dev: all services are running"
echo "dev: web ${BURNLINK_PUBLIC_BASE_URL}"
echo "dev: api ${BURNLINK_API_BASE_URL}"
echo "dev: nats monitor ${nats_monitor_url%/varz}"
echo "dev: press Ctrl-C to stop"

monitor_processes
