# 2026-11-16 멀티 레플리카 아키텍처 전환 스펙 (PostgreSQL 단일화 + Valkey + 3A 배치)

이 문서는 Flick의 `flick-api`, `flick-web`, `flick-worker`를 단일 노드 SQLite + 인메모리 상태에서 **PostgreSQL 단일화 + Valkey 기반 수평 확장(`replicas >= 2`, 무중단 롤링 업데이트) 아키텍처**로 전환하기 위한 실행 설계 스펙이다. 확정된 아키텍처 결정과 Red-Team 검증에서 확인된 동시성·모니터링·CI·클러스터 제약을 모두 반영하며, GitHub 마일스톤·이슈 및 단계별 PR 구현의 기준 문서로 사용한다.

---

## 1. Goal & Non-Goals

### Goal

1. **`flick-api` · `flick-web` 수평 확장 및 무중단 롤링 배포**: `api`/`web`의 RWO 로컬 PVC(`flick-api-data`, `flick-worker-data`)와 프로세스 내 공유 상태를 외부화하여 `replicas >= 2` 및 무중단 `RollingUpdate`를 달성한다.
2. **PostgreSQL 단일화 (SQLite 완전 제거)**: `modernc.org/sqlite` 및 SQLite 전용 스키마·방언 우회 코드를 제거하고 `github.com/jackc/pgx/v5/stdlib`(`CGO_ENABLED=0`) 기반 PostgreSQL 단일 방언으로 통합한다.
3. **Live Transfer(`/api/pipe/*`) 다중 파드 지원**: Valkey 기반 대기방(rendezvous) + Pod 간 직접 HTTP 바이트 스트리밍(`/internal/pipe/stream/{codeHash}`)으로 전환하되, 기존 E2EE 0바이트 비저장 경계를 보존하고 롤링 업데이트 중 대기실 무중단 재연결 및 진행 스트림 60초 유예(`T1`)를 보장한다.
4. **오사카 2노드 분산 배치(`옵션 3A`)**: `loki`를 `osaka-micro`에서 `flick-core`로 이전해 확보한 여유 메모리에 `flick-api` #2 및 `flick-web` #2를 배치한다.

### Non-Goals

- 멀티 노드 HA PostgreSQL(Patroni/Citus), 멀티 노드 Valkey Sentinel/Cluster, NATS 멀티 레플리카 클러스터 구성 (OCI Always Free 메모리 예산 상 데이터 계층은 `flick-core` 단일 인스턴스로 유지한다).
- WebRTC P2P 전송 도입.
- 클라이언트 `age` 암호화 포맷([contracts/age-format-v1.md](../../contracts/age-format-v1.md)) 및 공개 HTTP 응답 계약([contracts/openapi.yaml](../../contracts/openapi.yaml))의 하위 호환 파괴.
- 한국 리전 노드(`kr-micro-1`, `kr-micro-2`)로의 Flick API/Worker 워크로드 배치 (오사카 DB/NATS/S3 간 크로스리전 RTT·이그레스 방지).

---

## 2. 확정된 아키텍처 결정 (2026-11-16 합의)

| 결정 영역 | 확정 선택 | 핵심 근거 및 대가 |
|---|---|---|
| **1. DB 방언** | **PostgreSQL 단일화 (`Option A2`)** | `? → $1` 단순 변환만으로는 `julianday`, `datetime`, `LastInsertId`, `READ COMMITTED` 동시성 버그를 막지 못하며 이중 방언 유지 비용이 크다. 대신 Compose/CI와 `release-cli.yml` 테스트 범위를 Postgres 전제에 맞게 함께 재구성한다. |
| **2. 노드 배치** | **오사카 2노드 분산 (`Option 3A`)** | 실측상 `grafana`(277Mi)는 이미 `flick-core`(가용 3,458Mi)에 있고, `osaka-micro`(총 952Mi / 가용 401Mi)에는 `loki`(상한 384Mi, `5Gi` RWO PVC)가 있다. `loki`를 `flick-core`로 옮겨 `osaka-micro` 여유를 확보하고, `flick-api`×2 · `flick-web`×2를 `flick-core`와 `osaka-micro`에 분산한다(`RollingUpdate maxSurge:0, maxUnavailable:1`, `flick-worker`는 오사카 `flick-core` 유지). |
| **3. Live Transfer** | **Option B1 (Valkey Rendezvous + Pod 간 HTTP 스트림) + 60초 유예 (`T1`)** | Valkey에는 암호문 0바이트만 둔다(B3 Valkey Streams는 Slow Receiver 시 Valkey OOM 유발, B2 전체 프록시는 파드 종료 시 대기실 강제 종료 및 IP/레이트리밋 오염). 대기실 롱폴(`/wait`, `/status`)은 `SIGTERM` 시 즉시 반환 + 클라이언트 자동 재연결, 진행 중 스트림은 `terminationGracePeriodSeconds: 60` 유예를 적용한다. |
| **4. Valkey 경계** | **비영속 + 해시 키 + Fail-Open 폴백** | `--save "" --appendonly no`로 디스크 저장을 원천 차단하고 Rate limit 키는 HMAC/SHA-256 해시 + 짧은 TTL(`<= 10m`)만 사용한다. Valkey 장애 시 `/readyz`는 실패시키지 않으며(DB+NATS만 검사), Rate limiter는 인메모리 폴백으로 전환하고 `/api/pipe/*`만 `503`으로 격리한다. |

---

## 3. 컴포넌트별 상세 설계 및 불변식

### 3.1 PostgreSQL 아키텍처 (`internal/db`, `usage`, `events`, `secrets`, `requests`, `worker`)

1. **논리 DB 및 자격증명 분리 (`database-schema.md` 준수)**
   - `flick-core` 위 단일 PostgreSQL 17(`postgres:17-alpine`) 인스턴스 안에 **`flick_api` DB (`flick_api` Role)** 와 **`flick_worker` DB (`flick_worker` Role)** 를 분리 생성한다.
   - `deploy/base/api.yaml`과 `deploy/base/worker.yaml`의 기존 전체 `envFrom: secretRef: flick-secrets` 구성을 정리하여, API 파드는 Worker DSN을, Worker 파드는 API DSN을 참조할 수 없도록 명시적 `secretKeyRef`로 분리한다.
2. **드라이버·풀·마이그레이션 락**
   - `github.com/jackc/pgx/v5/stdlib` (`database/sql`, `CGO_ENABLED=0` 유지) 사용. 기존 SQLite `SetMaxOpenConns(1)` 제약을 해제하고 환경/역할에 맞는 풀 크기(`MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`)를 설정한다.
   - `MigrateAPI` 및 `MigrateWorker` 진입 시 세션 수준 **`pg_advisory_lock`**을 획득해 다중 레플리카 동시 기동 시 `pg_type` 카탈로그 경합 크래시를 방지한다.
3. **SQL 방언 및 동시성 레이스 필수 수정 (Red-Team 검증 반영)**
   - **Boolean 덧셈 제거**: `internal/db/storage_usage.go:StorageObjectBytes`의 `((upload_key is not null) + (final_key is not null))`를 `CASE WHEN ... THEN 1 ELSE 0 END`로 교체하고 `julianday`를 native `TIMESTAMPTZ` 비교로 변경한다.
   - **`LastInsertId()` 제거**: `internal/worker/store.go:ReceiptStore.Start`의 `job_attempts` 삽입을 `INSERT INTO job_attempts (...) VALUES (...) RETURNING id`로 교체한다.
   - **Worker 경합 및 NATS `AckWait` 수정**: `ReceiptStore.Start`에 `INSERT ... ON CONFLICT (job_id) DO UPDATE` / `FOR UPDATE` 기반 원자적 리스 획득을 적용하고, `internal/events/consumer.go`의 JetStream `ConsumerConfig`에 `AckWait`를 `worker.JobTimeout(30s)`보다 크게(예: `45s`) 명시한다.
   - **Unlock I/O 불변식 유지 (`TestRevokeWinsAgainstInFlightObjectRead`)**: `internal/secrets/store.go`의 `OpenTx`와 `Finalize`는 S3 `objects.Open` / `objects.Head` I/O 동안 DB 행 잠금(`FOR UPDATE`)을 잡지 않는다.
     - `Finalize`: 조건부 활성화(`WHERE state = 'pending_upload'`)가 `affected == 0`이면 상태를 재조회해 이미 `'active'`인 경우 idempotent `nil`을 반환한다(`ErrNotFound` 오반환 방지).
     - `Finalize`의 조건부 활성화는 `reclaim_enqueued_at is null`도 요구한다. 다른 레플리카의 reaper가 먼저 회수를 claim했으면 활성화하지 않고 `ErrNotFound`를 돌려준다.
     - `Revoke` (`internal/secrets/management.go`): 저장소 메타데이터 조회가 행을 못 찾거나 조건부 삭제가 `RowsAffected() == 0`이면 `management()`로 결과를 다시 읽는다. 열람에 졌으면 `ErrManagementUnavailable`(`404 management_unavailable`, 미존재 ID와 바이트 단위 동일), 오답 잠금에 졌으면 `ErrNotCancellable`(409), 다른 취소에 졌으면 멱등 `cancelled`(200)로 답한다. `503 management_status_failed`로 새지 않는다.
     - `recordFailedAccessTx`의 `UPDATE ... RETURNING`이 0행이면 행 존재를 다시 확인해 삭제됐으면 `ErrNotFound`, 잠겼으면 `ErrConsumed`를 돌려준다.
   - **Model A 오답 카운터 원자화**: `secrets/store.go:recordFailedAccessTx`를 `UPDATE ... SET failed_access_count = failed_access_count + 1 WHERE id = $1 AND state = 'active' RETURNING failed_access_count, consumed_at`로 실행해 동시 4·5회 오답 요청이 이중 락아웃/이중 `usage_counters` 집계를 일으키지 않게 한다.
   - **Outbox & Reaper 멀티 레플리카**: `events/outbox.go`(`ListDue`)와 `secrets/reaper.go`(`claimReclaimableSQL`), `requests/cleanup.go`(`PurgeExpiredTx`)에 `FOR UPDATE SKIP LOCKED` + Postgres native `TIMESTAMPTZ` / `LEAST` / `ON CONFLICT DO NOTHING`을 적용한다. `requests/store.go`의 SQLite 전용 raw `"rollback"` 더미 락은 표준 `BeginTx`로 대체한다.

### 3.2 Live Transfer (`/api/pipe/*`) — Option B1 세부 프로토콜

1. **Valkey 상태 모델 (암호문 0바이트)**
   - Valkey 키(`pipe:room:{codeHash}`, `TTL <= 3m`)에는 방 메타데이터(`state`, `sender_token_hash`, `selected_connection_id`, `receiver_pod_ip`, `stream_secret_hash`, `expires_at`, `version`)와 대기 연결 목록만 저장한다.
   - **`polls` 누수 방지**: 기존 인메모리 `conn.polls` 카운터 대신 TTL 35초(`presenceTimeout`) 기반 Redis/Valkey lease 키(`pipe:poll:{codeHash}:{connID}`)를 사용해 파드 크래시 시에도 35초 내 자동 해제된다.
   - Pub/Sub 채널(`pipe:ev:{codeHash}`)로 상태 변경·선택·거절·취소·`receiver_pod_ip` 준비 이벤트를 모든 API 파드에 즉시 브로드캐스트한다.
2. **양방향 도착 및 스트림 백프레셔**
   - 브라우저는 `POST /api/pipe/select` 직후 `POST /api/pipe/send`를 먼저 호출하므로([web/src/lib/state/pipe-relay.svelte.ts](../../../web/src/lib/state/pipe-relay.svelte.ts#L206-L235)), 송신자가 붙은 Pod B는 `receiver_pod_ip`가 등록될 때까지 Pub/Sub로 최대 `35s` 대기한 뒤 미접속 시 `chosenAbandoned`로 방을 원자적으로 종료한다.
   - 수신자가 붙은 Pod A의 `GET /api/pipe/receive`는 `POD_IP`(Kubernetes Downward API `status.podIP`) + 고정 내부 포트 + 1회용 `stream_secret`을 Valkey에 등록한 뒤, Pod B로부터 `POST http://<receiver_pod_ip>:<port>/internal/pipe/stream/{codeHash}` 요청이 도착할 때까지 HTTP `200 OK` 응답 헤더(`Flick-Kind`, `Flick-Encrypted-Filename`) 기록을 지연한다.
   - Pod A의 `/internal/pipe/stream/{codeHash}`는 수신자 TCP 소켓에 `io.CopyBuffer`가 정상 `EOF`를 마칠 때만 `200 OK`를 응답한다. 수신자 조기 단절 시 Pod B가 이를 감지해 송신자에게 `502 receiver_disconnected`를 반환한다.
3. **보안·타임아웃·무중단 드레이닝**
   - `/internal/pipe/stream/{codeHash}` 인증은 `X-Flick-Internal-Token` + 세션 1회용 `X-Flick-Pipe-Stream-Secret` 이중 검증을 수행하고, Pod B의 다이얼 대상은 파싱된 IPv4/IPv6 리터럴 + 고정 내부 포트만 허용해 SSRF를 차단한다.
   - `cmd/flick-api/main.go`의 서버 전역 `ReadTimeout: 30s`에 잘리지 않도록 `/internal/pipe/stream/{codeHash}` 핸들러도 `http.NewResponseController(w)`로 15분 Read/Write deadline을 설정한다.
   - **종료 시퀀스 정합성 (`T1 = 60s`)**: 현재 `cmd/flick-api/main.go:271`은 `server.Shutdown` 타임아웃이 `10s`로 고정돼 있다. K8s `preStop: sleep 5` + `terminationGracePeriodSeconds: 60` 안에서 진행 중 전송이 최대 ~50초까지 완료될 수 있도록:
     1. `SIGTERM` 수신 즉시 대기실 롱폴(`/wait`, `/status`) 컨텍스트를 깨워 현재 스냅샷 `200 OK`를 즉시 반환하게 해,
     2. HTTP `Shutdown` 타임아웃을 `50s`로 늘려 진행 중인 `/send`·`/receive`·`/internal/pipe/stream` 연결은 완료될 때까지 드레이닝한다.
   - 프론트엔드 [web/src/lib/crypto/pipe.ts](../../../web/src/lib/crypto/pipe.ts#L251-L320)의 `waitForPipeUpdate`(`/wait`)에 `5xx`/네트워크 `TypeError` 안전 재시도를 추가하고, `pollPipeStatus`(`/status`)도 소켓 단절(`TypeError`) 1회 재시도를 잡도록 보강한다.

### 3.3 Rate Limiter · Telemetry · Monitoring

- **Rate Limiter**: Valkey Lua 토큰버킷 스크립트로 기존 8개 리미터를 분산 공유하되, 키(`pipe/secrets/requests` 각 스코프)는 원시 `clientNetwork` 대신 Deployment 시크릿 솔트 기반 SHA-256/HMAC 해시 + `<= 10m` TTL로 저장한다. Valkey 호출 타임아웃/에러 시 즉시 프로세스 내 기존 인메모리 `rateLimiter`로 폴백하고 경고 로그·메트릭을 남긴다.
- **Scrape-Time Metrics (`internal/telemetry/`)**: `flick_active_uploads`, `flick_outbox_pending`, `flick_storage_bytes_used`를 기동/쓰기 시점 캐시 갱신에서 **스크랩 시점 DB `prometheus.Collector`(2초 타임아웃)** 로 전환한다.
- **Prometheus / Grafana / Alloy (`deploy/base/monitoring/`)**:
  - `flick-api-headless`, `flick-worker-metrics-headless` Service 추가 및 `vmsingle.yaml` `dns_sd_configs` 전환.
  - `grafana.yaml` 패널 쿼리 수정: `max(flick_active_uploads)`, `max by (storage) (flick_storage_bytes_used)`, `max(flick_outbox_pending)`.
  - `alloy.yaml` 로그 ID 마스킹 정규식에 `/internal/pipe/` 경로(`codeHash`)를 추가하고 Postgres/Valkey 파드 로그에 비밀 정보가 남지 않도록 로그 수준을 검토한다.

### 3.4 클러스터 배치 (`Option 3A`) & 리소스 예산 (실측 기준)

| 노드 | 역할 및 주요 워크로드 | 실측/예상 메모리 (Working Set 기준) | 비고 |
|---|---|---|---|
| **`flick-core`** (`arm64`, ~5.9Gi / 실측 가용 3,458Mi, 디스크 40G 여유) | `traefik`(155Mi), `postgres` 17(150~300Mi / max 512Mi), `valkey` 8(20~64Mi / max 128Mi), `nats-0`(30Mi), `grafana`(277Mi), `loki`(65Mi / limit 384Mi), `flick-worker`(5Mi), `flick-api` #1(10~80Mi), `flick-web` #1(2~20Mi), 시스템/DS(~480Mi) | 합산 ~1.3–1.7 GiB / 총 5.9 GiB | `loki-data` RWO PVC(`5Gi`)를 `flick-core`에 신규 바인딩(7일 접근로그 리셋 기본) 또는 수동 콜드 복사 |
| **`osaka-micro`** (`amd64`, ~952Mi / 현재 가용 401Mi + `loki` 제거 시 +65Mi RSS / limit 384Mi 회수) | DaemonSets(129Mi) + `cert-manager-cainjector`(26Mi) + `flick-api` #2(limit 256Mi, `/tmp` 600Mi) + `flick-web` #2(limit 128Mi) | 상시 working set ~170–220Mi (여유 ~730Mi) | **`RollingUpdate (maxSurge: 0, maxUnavailable: 1)` 필수** (`osaka-micro` 내 API 이중 기동 OOM 차단). 사전 체크: `osaka-micro` `df -h` >= 2GiB 여유 확인 |
| **`kr-micro-1`, `kr-micro-2`** (한국 리전) | 모니터링(`kube-state-metrics`, `vmsingle`), `cert-manager-webhook` 유지 | 변경 없음 | `flick-api`/`flick-web`/`flick-worker`는 오사카 풀/호스트 `nodeAffinity`로 한국 노드 스케줄링 차단 |

---

## 4. 단계별 PR 구현 계획 (Dependency-Ordered)

모든 작업은 `docs/architecture/agent-workflow.md`를 따르며(GitHub Milestone -> Issue -> `main` 기반 토픽 브랜치 -> CI 통과 + 독립 PR 리뷰어 서브에이전트 `Decision: approve` 후 머지), 각 PR 머지 시점마다 `main` CI가 깨지지 않는 순서를 유지한다.

- **Phase 0 (착수 준비, 코드 변경 전)**:
  1. GitHub 마일스톤 생성(예: `M15: Multi-Replica PostgreSQL & Valkey`) 및 아래 PR 1~6에 대응하는 이슈 6건 생성.
  2. 라이브 클러스터 사전 확인 2건: `osaka-micro` 로컬 디스크 여유(`df -h`) 확인 및 `loki` 기존 7일 접근로그 리셋 허용 최종 확인(기본값: 리셋), `flick-core` ↔ `osaka-micro` Flannel 오버레이 `:5432`/`:6379`/`:8080` 통신 확인.

- **PR 1 — 아키텍처 문서, Env 계약 및 CI Postgres/Valkey 테스트 하네스** (`area:docs`, `area:ci`)
  - **대상**: `docs/architecture/{implementation-choices,service-topology,database-schema,storage-model,security-model,env-contract,deployment-target,ci-testing}.md`, `docs/ROADMAP.md`, `.env.example`, `compose.yaml`, `.github/workflows/ci.yml`, `.github/workflows/release-cli.yml`, `scripts/ci/{go.sh,storage-integration.sh,compose-nats-smoke.sh}`.
  - **핵심**: Postgres 17 + Valkey 8 서비스·환경변수(`FLICK_DATABASE_URL`, `FLICK_WORKER_DATABASE_URL`, `FLICK_VALKEY_ADDR`, `FLICK_RATELIMIT_KEY_SALT`, `FLICK_INTERNAL_HTTP_PORT`, `FLICK_POD_IP` 등)를 문서/Compose/CI에 먼저 고정하고, `release-cli.yml`은 DB 비의존 CLI 패키지(`./cmd/flick/... ./internal/flickcli/... ./internal/clientcrypto/...`) 테스트로 분리하며, `ci.yml`에는 Postgres + Valkey 서비스 컨테이너를 추가한다. (기존 SQLite 코드는 PR 1에서 아직 제거하지 않아 100% 통과).
  - **Verify**: `bash scripts/ci/repo-structure.sh && bash scripts/ci/env-contract.sh && bash scripts/ci/contracts.sh && docker compose config && bash scripts/ci/go.sh`

- **PR 2 — `internal/db` Postgres 풀·어드바이저리 락 도입 + `flick-worker` Postgres 이관** (`area:worker`, `area:db`)
  - **대상**: `go.mod`/`go.sum`(`pgx/v5/stdlib`), `internal/db/postgres.go`(+테스트 격리 스키마 헬퍼), `internal/db/migrations_worker.go`, `internal/events/consumer.go`(`AckWait: 45s`), `internal/worker/{store,store_test,...}.go`, `cmd/flick-worker/main.go`.
  - **핵심**: `flick_worker` 스키마·어드바이저리 락 마이그레이션·`RETURNING id`·`ON CONFLICT` 리스 점유를 Postgres로 이전한다. (`api.db`는 PR 3까지 SQLite 유지).
  - **Verify**: `FLICK_TEST_POSTGRES_URL=postgres://... bash scripts/ci/go.sh && FLICK_NATS_INTEGRATION=1 FLICK_TEST_POSTGRES_URL=postgres://... go test -tags integration ./internal/events/...`

- **PR 3 — `api` DB Postgres 단일화, 동시성 레이스 수정 및 `modernc.org/sqlite` 완전 제거** (`risk:high`, 원자적 이관 규모 예외 명시)
  - **대상**: `internal/db/{migrations,requests,storage_usage}.go`(SQLite 제거), `internal/usage/usage.go`, `internal/events/outbox.go`, `internal/secrets/{store,management,reaper,reconciliation,*_test}.go`, `internal/requests/{store,large,cleanup,*_test}.go`, `internal/httpapi/*_test.go`, `cmd/flick-api/main.go`, `scripts/dev/up.sh`, `go.mod`(`modernc.org/sqlite` 삭제).
  - **핵심**: §3.1의 모든 방언·동시성 불변식(`TestRevokeWinsAgainstInFlightObjectRead` 무잠금 I/O + idempotent `Finalize` + `Revoke` 경합 404 + `recordFailedAccessTx` 원자 `RETURNING` + `FOR UPDATE SKIP LOCKED` Outbox/Reaper/Request) 구현 및 SQLite 완전 삭제.
  - **Verify**: `CGO_ENABLED=0 go build ./cmd/... && FLICK_TEST_POSTGRES_URL=... bash scripts/ci/all.sh && bash scripts/ci/storage-integration.sh && bash scripts/ci/compose-nats-smoke.sh`

- **PR 4 — Valkey 분산 Rate Limiter + Scrape-Time 메트릭 및 멀티 레플리카 모니터링** (`area:api`, `area:ops`)
  - **대상**: `go.mod`(`valkey-go` 또는 `go-redis/v9` pure-Go), `internal/httpapi/{ratelimit,router,ratelimit_test}.go`, `internal/telemetry/`, `cmd/flick-api/main.go`, `deploy/base/monitoring/{vmsingle,grafana,alloy}.yaml`, `deploy/base/api.yaml`(헤드리스 서비스).
  - **핵심**: HMAC/SHA-256 해시 키 Lua 레이트리미터 + Valkey 장애 시 인메모리 폴백 + `/readyz` Postgres+NATS 한정 + 3종 게이지 DB 스크랩 컬렉터 + `vmsingle` `dns_sd_configs` + `grafana` `max(...)` + `alloy` `/internal/pipe/` 마스킹.
  - **Verify**: `FLICK_TEST_POSTGRES_URL=... FLICK_TEST_VALKEY_ADDR=... bash scripts/ci/all.sh && bash scripts/ci/k8s-manifests.sh`

- **PR 5 — Live Transfer (`/api/pipe/*`) Valkey Rendezvous + Pod 간 HTTP 스트리밍 (`B1` + `60s` 드레이닝)** (`area:api`, `area:web`)
  - **대상**: `internal/piperelay/`, `internal/httpapi/{piperelay,router}.go`, `cmd/flick-api/main.go`(`Shutdown` `50s` + `/wait`/`/status` 조기 wake-up), `web/src/lib/crypto/pipe.ts`(+테스트), `web/nginx.conf`(필요 시 `/api/pipe/` 버퍼링/타임아웃 점검).
  - **핵심**: §3.2의 양방향 `/send`↔`/receive` 동기화, 지연 헤더 응답, 이중 토큰 + IP 리터럴 SSRF 차단, TTL 35초 poll lease, `15m` Request deadline, 클라이언트 `/wait`·`/status` 재시도 구현.
  - **Verify**: 2-인스턴스 `httpapi` 크로스파드 `/api/pipe/*` 통합 테스트(`go test`), `pnpm --dir web test:browser`, 2-레플리카 대상 `tests/e2e/pipe-relay.live.mjs`.

- **PR 6 — SQLite→Postgres 컷오버 도구/런북 및 k8s `3A` 멀티 레플리카·`k3d-smoke` 반영** (`area:deploy`, `area:ops`)
  - **대상**: `scripts/ops/migrate-sqlite-to-postgres.sh`(또는 SQLite CLI `sqlite3`+`psql` 검증 스크립트, `go.mod`에 SQLite 드라이버 미재도입), `docs/runbook/postgres-cutover.md`, `deploy/base/{postgres,valkey,pvc,secret,api,worker,web,ingress,kustomization}.yaml`, `deploy/base/monitoring/loki.yaml`(`flick-core` 이전), `deploy/k3d/kustomization.yaml`, `scripts/ci/{repo-structure.sh,k3d-smoke.sh}`.
  - **핵심**: RWO API/Worker PVC 제거, `api`/`worker` Secret 자격증명 분리, `Option 3A` 노드 배치(`loki`→`flick-core`, `api`×2/`web`×2→오사카 2노드, `RollingUpdate maxSurge:0 maxUnavailable:1`, `preStop: sleep 5`, `terminationGracePeriodSeconds: 60`), Postgres `PGDATA` 서브디렉터리 + `readOnlyRootFilesystem` 호환 볼륨, Valkey `--save "" --appendonly no`.
  - **Verify**: `bash scripts/ci/all.sh && bash scripts/ci/k8s-manifests.sh && bash scripts/ci/k3d-smoke.sh` (k3d 2노드 멀티 레플리카에서 `/api/secrets`, `/api/requests`, `/api/pipe/*` 전체 통과).

---

## 5. 일회성 운영 컷오버 및 롤백 절차 (Type 1 One-Way Door)

빈 PostgreSQL에 동일 OCI 버킷을 붙이면 `ReconcileOnce`가 기존 유효 객체를 고아로 오인해 삭제하므로, **운영 반영 시 반드시 아래 절차를 지킨다**:

1. **사전 점검**: `osaka-micro` 디스크 여유(`df -h` >= 2GiB) 확인, 기존 `loki` 로그 보존 필요 시 백업.
2. **쓰기 중지 (계획 다운타임 1~5분)**: `flick-api`와 `flick-worker` `replicas=0` 스케일 다운.
3. **원본 백업 및 WAL 플러시**: 기존 `api.db`, `worker.db`에 `PRAGMA wal_checkpoint(TRUNCATE);` 실행 후 호스트 외 안전한 경로에 콜드 백업(`.backup`).
4. **전체 이관 및 시퀀스 동기화**:
   - `flick_api` DB 16개 테이블 전체(`secrets`, `secret_payloads`, `secret_management`, `secret_open_grace`, `object_reconciliation_*`, `requests`, `request_payloads`, `request_reconciliation_*`, `outbox_events`, `usage_counters` 등) + `flick_worker` DB 3개 테이블(`job_receipts`, `job_attempts`, `dead_letters`) 이관.
   - `job_attempts` 아이덴티티/시퀀스를 `SELECT setval(..., COALESCE(MAX(id), 1))`로 재설정(미실행 시 첫 잡에서 PK `23505` 충돌 발생).
5. **정합성 검증**: SQLite vs Postgres 테이블별 `COUNT(*)`, `SUM(size_bytes)`, `usage_counters` 합계, `state='active'` 비밀 건수 일치 확인.
6. **매니페스트 적용 및 검증**: `loki` PVC 정리 후 `flick-core`로 재배포 -> `flick-worker` 선기동 -> `flick-api` ×2, `flick-web` ×2 기동 -> `/readyz`, 비밀 생성/열람, Live Transfer 크로스파드 전송 확인.
7. **롤백 윈도우**: 트래픽 재개 전 검증 실패 시 원본 `api.db`/`worker.db`는 수정하지 않았으므로 기존 k3s 매니페스트로 원복한다. 트래픽 재개 후 생성된 신규 비밀은 되돌릴 수 없다(Type 1).
