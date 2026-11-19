#!/usr/bin/env bash
set -euo pipefail

export FLICK_ENV="${FLICK_ENV:-development}"
export FLICK_LOG_LEVEL="${FLICK_LOG_LEVEL:-debug}"
export FLICK_PUBLIC_BASE_URL="${FLICK_PUBLIC_BASE_URL:-http://localhost:5173}"
export PUBLIC_FLICK_API_BASE_URL="${PUBLIC_FLICK_API_BASE_URL:-http://localhost:8080}"
export FLICK_API_BASE_URL="${FLICK_API_BASE_URL:-http://localhost:8080}"
export FLICK_INTERNAL_API_BASE_URL="${FLICK_INTERNAL_API_BASE_URL:-http://localhost:8080}"
export FLICK_INTERNAL_TOKEN="${FLICK_INTERNAL_TOKEN:-change-me-local}"
export FLICK_METRICS_TOKEN="${FLICK_METRICS_TOKEN:-change-me-metrics-local}"
export FLICK_API_ADDR="${FLICK_API_ADDR:-:8080}"
export FLICK_NATS_URL="${FLICK_NATS_URL:-nats://127.0.0.1:4222}"
export FLICK_NATS_STREAM="${FLICK_NATS_STREAM:-FLICK_JOBS}"
export FLICK_NATS_JOB_SUBJECT="${FLICK_NATS_JOB_SUBJECT:-flick.jobs}"
# M15: the API and worker each own their PostgreSQL logical database.
# compose.yaml + scripts/dev/postgres-init.sql create the local roles and
# databases; both entrypoints fail fast when their DSN is empty.
export FLICK_DATABASE_URL="${FLICK_DATABASE_URL:-postgres://flick_api:flick_api@127.0.0.1:5432/flick_api?sslmode=disable}"
export FLICK_WORKER_DATABASE_URL="${FLICK_WORKER_DATABASE_URL:-postgres://flick_worker:flick_worker@127.0.0.1:5432/flick_worker?sslmode=disable}"
# Live transfer (/api/pipe/*) keeps its waiting rooms in Valkey and the rate
# limiter shares buckets there; without FLICK_VALKEY_ADDR the API answers
# /api/pipe/* with 503. FLICK_DEV_SKIP_VALKEY=1 runs without it.
export FLICK_VALKEY_ADDR="${FLICK_VALKEY_ADDR-127.0.0.1:6379}"
export FLICK_RATELIMIT_KEY_SALT="${FLICK_RATELIMIT_KEY_SALT:-change-me-ratelimit-local}"
export FLICK_PAYLOAD_INLINE_MAX_BYTES="${FLICK_PAYLOAD_INLINE_MAX_BYTES:-1048576}"
export FLICK_MAX_FILE_BYTES="${FLICK_MAX_FILE_BYTES:-268435456}"
export FLICK_DEFAULT_TTL_SECONDS="${FLICK_DEFAULT_TTL_SECONDS:-3600}"
export FLICK_MIN_TTL_SECONDS="${FLICK_MIN_TTL_SECONDS:-300}"
export FLICK_MAX_TTL_SECONDS="${FLICK_MAX_TTL_SECONDS:-604800}"
export FLICK_OPEN_RATE_PER_MIN="${FLICK_OPEN_RATE_PER_MIN:-10}"
export FLICK_TRUSTED_PROXIES="${FLICK_TRUSTED_PROXIES:-}"
export FLICK_CREATE_RATE_PER_MIN="${FLICK_CREATE_RATE_PER_MIN:-5}"
# M3: dev turns on S3-compatible storage against local MinIO. Set
# FLICK_DEV_SKIP_MINIO=1 to keep storage disabled (CI/test without docker).
# This is the only place the backend is defaulted — an earlier
# "${...:-disabled}" export here made this ":-s3" unreachable and quietly kept
# large uploads off in local dev.
export FLICK_STORAGE_BACKENDS="${FLICK_STORAGE_BACKENDS-minio}"
export FLICK_STORAGE_MINIO_ENDPOINT="${FLICK_STORAGE_MINIO_ENDPOINT:-http://localhost:9000}"
export FLICK_STORAGE_MINIO_REGION="${FLICK_STORAGE_MINIO_REGION:-us-east-1}"
export FLICK_STORAGE_MINIO_BUCKET="${FLICK_STORAGE_MINIO_BUCKET:-flick-dev}"
export FLICK_STORAGE_MINIO_ACCESS_KEY_ID="${FLICK_STORAGE_MINIO_ACCESS_KEY_ID:-minioadmin}"
export FLICK_STORAGE_MINIO_SECRET_ACCESS_KEY="${FLICK_STORAGE_MINIO_SECRET_ACCESS_KEY:-minioadmin}"
export FLICK_STORAGE_MINIO_PATH_STYLE="${FLICK_STORAGE_MINIO_PATH_STYLE:-true}"

compose_project="${FLICK_DEV_COMPOSE_PROJECT:-flick-dev}"
nats_monitor_url="${FLICK_DEV_NATS_MONITOR_URL:-http://127.0.0.1:8222/varz}"
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

ensure_postgres() {
	if [ "${FLICK_DEV_SKIP_POSTGRES:-}" = "1" ]; then
		echo "dev: skipping Postgres startup because FLICK_DEV_SKIP_POSTGRES=1"
		return
	fi

	if nc -z 127.0.0.1 5432 >/dev/null 2>&1; then
		echo "dev: using existing Postgres at 127.0.0.1:5432"
		return
	fi

	if ! command -v docker >/dev/null 2>&1; then
		echo "dev: docker is required to start local Postgres" >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "dev: docker daemon is not available" >&2
		exit 1
	fi

	echo "dev: starting Postgres"
	docker compose -p "$compose_project" up -d postgres
	for _ in $(seq 1 60); do
		if docker compose -p "$compose_project" exec -T postgres pg_isready -U postgres >/dev/null 2>&1; then
			echo "dev: postgres ready at 127.0.0.1:5432"
			return 0
		fi
		sleep 1
	done
	echo "dev: postgres did not become ready at 127.0.0.1:5432" >&2
	return 1
}

ensure_valkey() {
	if [ "${FLICK_DEV_SKIP_VALKEY:-}" = "1" ]; then
		echo "dev: skipping Valkey because FLICK_DEV_SKIP_VALKEY=1 (live transfer answers 503)"
		export FLICK_VALKEY_ADDR=""
		return
	fi

	if nc -z 127.0.0.1 6379 >/dev/null 2>&1; then
		echo "dev: using existing Valkey at 127.0.0.1:6379"
		return
	fi

	if ! command -v docker >/dev/null 2>&1; then
		echo "dev: docker is required to start local Valkey" >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "dev: docker daemon is not available" >&2
		exit 1
	fi

	echo "dev: starting Valkey"
	docker compose -p "$compose_project" up -d valkey
	for _ in $(seq 1 60); do
		if docker compose -p "$compose_project" exec -T valkey valkey-cli ping >/dev/null 2>&1; then
			echo "dev: valkey ready at 127.0.0.1:6379"
			return 0
		fi
		sleep 1
	done
	echo "dev: valkey did not become ready at 127.0.0.1:6379" >&2
	return 1
}

ensure_nats() {
	if [ "${FLICK_DEV_SKIP_NATS:-}" = "1" ]; then
		echo "dev: skipping NATS startup because FLICK_DEV_SKIP_NATS=1"
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

ensure_minio() {
	if [ "${FLICK_DEV_SKIP_MINIO:-}" = "1" ]; then
		echo "dev: skipping MinIO startup because FLICK_DEV_SKIP_MINIO=1"
		export FLICK_STORAGE_BACKENDS=
		return
	fi

	if curl -fsS http://127.0.0.1:9000/minio/health/live >/dev/null 2>&1; then
		echo "dev: using existing MinIO at http://127.0.0.1:9000"
		return
	fi

	if ! command -v docker >/dev/null 2>&1; then
		echo "dev: docker is required to start local MinIO" >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "dev: docker daemon is not available" >&2
		exit 1
	fi

	echo "dev: starting MinIO"
	docker compose -p "$compose_project" up -d minio createbuckets >/dev/null 2>&1 || true
	for _ in $(seq 1 60); do
		if curl -fsS http://127.0.0.1:9000/minio/health/live >/dev/null 2>&1; then
			echo "dev: minio ready at http://127.0.0.1:9000"
			return 0
		fi
		sleep 1
	done
	echo "dev: minio did not become ready at http://127.0.0.1:9000" >&2
	return 1
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

ensure_postgres
ensure_valkey
ensure_nats
ensure_minio
install_web_deps

start_process "api" go run ./cmd/flick-api
wait_for_http "api" "${FLICK_API_BASE_URL%/}/healthz" "${pids[0]}"

start_process "worker" go run ./cmd/flick-worker
start_process "web" pnpm --dir web dev --host 127.0.0.1
wait_for_http "web" "$FLICK_PUBLIC_BASE_URL" "${pids[2]}"

echo "dev: all services are running"
echo "dev: web ${FLICK_PUBLIC_BASE_URL}"
echo "dev: api ${FLICK_API_BASE_URL}"
echo "dev: nats monitor ${nats_monitor_url%/varz}"
echo "dev: press Ctrl-C to stop"

monitor_processes
