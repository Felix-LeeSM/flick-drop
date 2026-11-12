# Work Note (done): Live Relay Code Drop (Zero-Storage Real-Time Transfer)

**Date**: 2026-10-30  
**Context**: Replaces WebRTC and persistent device address books (#255).  
**Parent Issue**: [#255](https://github.com/Felix-LeeSM/flick-drop/issues/255)  
**Milestone**: M14: Live Relay Code Drop  
**Sub-issues**: [#318](https://github.com/Felix-LeeSM/flick-drop/issues/318), [#319](https://github.com/Felix-LeeSM/flick-drop/issues/319), [#320](https://github.com/Felix-LeeSM/flick-drop/issues/320)  

> **Completed (2026-11-12).** Milestone M14: Live Relay Code Drop is closed on
> GitHub, and #399 made the transfer end-to-end encrypted to the receiver's age
> key. This note moved from `docs/work/active/` to `docs/work/done/` as history.
>
> **Superseded in part (2026-11-11, PR #392).** This note is the original plan.
> The durable design now lives in `docs/architecture/security-model.md` (Live
> Relay), `docs/architecture/service-topology.md`, and `contracts/openapi.yaml`.
> Where this note disagrees with them, they win. In particular:
>
> - There are **no failed-attempt counters and no failure burn**. A wrong code
>   never reaches a session, so a counter could only count right-code holders.
>   Online guessing is bounded by the `/join` rate limit
>   (`FLICK_OPEN_RATE_PER_MIN` per client network, IPv6 collapsed to /64) and
>   the 3-minute window. The `/receive` "5 failed code attempts" line under
>   Section 3 "HTTP Endpoints" and the Section 4 brute-force row are superseded.
> - Receivers call `POST /api/pipe/join` first. With confirmation on they
>   long-poll `GET /api/pipe/status` and open `/receive` only once selected; the
>   sender long-polls `/wait` and calls `/select`, `/decline`, `/cancel`, and
>   `/send` with a sender token. The data-flow diagram in Section 3 shows the
>   older receive-first flow.
> - There is **no server salt and no code-derived key** (superseded
>   2026-11-12). The receiving browser sends a fresh age recipient on `/join`,
>   the sender gets it from `/wait` and encrypts to it, so a passive server
>   cannot decrypt. Every `salt`, `scrypt(code, salt)`, and extra-passphrase
>   line below is superseded by `docs/architecture/security-model.md` (Live
>   Relay).

---

## 1. Executive Summary & Problem Statement

Flick users frequently need to transfer files or credentials directly between their own nearby devices (e.g. laptop to smartphone, or PC to laptop). 
- Existing **one-time links** require copying and pasting URLs into third-party messengers (Slack, KakaoTalk, Email), which exposes links in transit and defeats self-hosted privacy.
- **QR code scanning** is practical only when scanning a large screen with a mobile camera; pointing a laptop's fixed webcam at a phone or between camera-less desktop PCs is exceptionally cumbersome or impossible.
- **WebRTC P2P (PairDrop style)** suffers from high NAT traversal failure rates (10~20% without TURN), requires complex signaling, and fails in mobile carrier CGNAT environments.
- **Persistent device address books / blind inboxes** violate Flick's core principle as an ephemeral delivery tool ([`AGENTS.md`](../../../AGENTS.md), "Product Principles") and risk storage exhaustion and Burn DoS.

**The Solution**: **Live Relay Code Drop (실시간 HTTP 파이프 릴레이)**.
A 100% ephemeral, real-time streaming transfer gated by a single-use 6-digit code. The server acts strictly as an in-memory byte pipe (`io.Pipe`) with **0 bytes written to Object Storage (S3) or SQLite disk**, consuming only a bounded 64 KiB RAM buffer during transfer.

---

## 2. Core Invariants & Goals

1. **Zero Persistent Storage (S3: 0B, Disk: 0B)**:
   - Data exists on the server only in volatile RAM as an in-flight byte stream between the sender's HTTP request body and receiver's HTTP response writer.
   - Once transfer completes (or if interrupted), all bytes vanish immediately.
2. **Encrypted Wire Transfer & Honest-Server Threat Model**:
   - The payload is encrypted in the sender's browser using standard `age` streaming format ([`contracts/age-format-v1.md`](../../../contracts/age-format-v1.md)).
   - **Threat Model**: A 6-digit numeric code provides $\approx 20$ bits of entropy, which defends against passive network eavesdroppers, reverse proxy caches, and offline disk scrapers (0 bytes stored). For users requiring protection against an actively hostile server, an optional passphrase field can be enabled, wrapping the stream in high-work-factor scrypt.
3. **100% Network Traversal Reliability**:
   - Standard single-port HTTPS (443) communication. Works seamlessly through Cloudflare proxies, cellular CGNAT, and corporate firewalls without STUN/TURN servers.
4. **Zero Accounts, Zero Address Books**:
   - No persistent pairing, no device registries, and no IndexedDB address books. Pure single-use rendezvous.
5. **No Key Material in Query Strings**:
   - Codes and hashes travel exclusively in HTTP request headers (`X-Flick-Pipe-Code`) or request bodies, and are explicitly redacted from HTTP logs and tracing spans.
6. **Ultra-Low Memory Footprint on OCI Free Tier**:
   - Pipe buffer: 64 KiB per active transfer.
   - Rendezvous table: ~150 bytes per active 3-minute code.
   - Strictly bounded concurrency to fit within `flick-api`'s 256 MiB memory ceiling ([`docs/architecture/deployment-target.md`](../../architecture/deployment-target.md)).

---

## 3. Architecture & Data Flow

```text
[Sender Browser (Laptop)]                  [Flick API (In-Memory Pipe)]               [Receiver Browser (Mobile)]
           │                                            │                                            │
           ├── 1. POST /api/v1/pipe/init ──────────────>│ (Generates 6-digit code: "741-928")        │
           │ <── Returns { code: "741928", salt } ──────┤ (Stores rendezvous channel in RAM, TTL 3m) │
           │                                            │                                            │
           │  [Displays "741 - 928" on screen]          │                                            │
           │                                            │ <── 2. GET /api/v1/pipe/receive ───────────┤
           │                                            │     Header: X-Flick-Pipe-Code: 741928      │
           │                                            │     (Validates code, waits on pipe)        │
           │                                            │                                            │
           │ <── 3. WebSocket / Long-poll wakeup ───────┤                                            │
           │     ("Receiver connected! Start stream")   │                                            │
           │                                            │                                            │
           ├── 4. POST /api/v1/pipe/send ──────────────>│                                            │
           │      Header: X-Flick-Pipe-Code: 741928     │                                            │
           │      (Encrypted age stream: 64KB chunks)   │ ──(io.Copy via io.Pipe, RAM 64KB buffer)──>│
           │                                            │                                            ├── 5. Decrypts age stream
           │                                            │                                            │      on-the-fly with scrypt key
           │                                            │                                            │      and triggers file save
           │ <── 6. 200 OK (Transfer complete) ─────────┼── 6. 200 OK (Stream closed) ───────────────┤
           │                                            │                                            │
           │                                            │ (Rendezvous channel burned & freed)        │
```

### Detailed Component Boundaries

#### 1. In-Memory Rendezvous Coordinator (`internal/piperelay/coordinator.go`)
- Maintains an in-memory concurrent map: `map[codeHash]*Rendezvous`.
- `Rendezvous` struct:
  - `CodeHash [32]byte`: `SHA256(normalizedCode)`
  - `Salt []byte`: 16-byte random salt for client-side scrypt KDF.
  - `CreatedAt time.Time`, `ExpiresAt time.Time`: hard 3-minute expiry.
  - `ReceiverReady chan struct{}`: signaled when receiver connects.
  - `PipeReader *io.PipeReader`, `PipeWriter *io.PipeWriter`: instantiated on connection.
  - `Done chan struct{}`: signaled on completion or client disconnect.
- **Garbage Collection**: Background ticker cleans up entries older than 3 minutes every 30 seconds. Max active entries bounded by `FLICK_PIPE_MAX_CONCURRENT` (default 100).
- **Cluster Deployment Note**: This implementation runs on a single `flick-api` instance per OCI Free Tier topology ([`docs/architecture/deployment-target.md`](../../architecture/deployment-target.md)). If scaled horizontally in the future, session affinity on Ingress or NATS Core broadcast must be introduced.

#### 2. HTTP Endpoints (`internal/httpapi/piperelay.go`)
- `POST /api/v1/pipe/init`:
  - Rate limit: max 5 requests per minute per IP.
  - Generates uncolliding 6-digit code (e.g. `100000` to `999999`) and 16-byte salt.
  - Returns `{ "code": "741928", "salt": "<base64>", "expires_in": 180 }`.
- `GET /api/v1/pipe/receive`:
  - Request header: `X-Flick-Pipe-Code: 741928` (never in query parameters).
  - ~~Rate limit: max 5 failed code attempts per 15 minutes per IP.~~
    Superseded: no failed-attempt counter exists; `/join` carries the
    per-network rate limit (see the note at the top).
  - Looks up `SHA256(code)`. If matched, hooks up `PipeReader` to `http.ResponseWriter` with streaming headers (`Content-Type: application/octet-stream`, `Transfer-Encoding: chunked`).
- `POST /api/v1/pipe/send`:
  - Request header: `X-Flick-Pipe-Code: 741928`.
  - Streams incoming `r.Body` directly into `PipeWriter` using `io.CopyBuffer` with a 64 KiB buffer.
  - Burns the rendezvous on completion or error.

#### 3. Client Cryptography & Streaming (`web/src/lib/crypto/pipe.ts`)
- **Key Derivation**: `derivedKey = scrypt(code, salt, N=131072, r=8, p=1)`.
- **Encryption**: Standard age streaming cipher (`contracts/age-format-v1.md`) wrapped with derived key.
- **Decryption**: Receiver browser consumes stream chunks via `ReadableStreamDefaultReader`, authenticates 64 KiB age payload blocks, and emits decrypted bytes directly to a `Blob` or stream downloader.

---

## 4. Threat Model & Security Defenses

| Threat Vector | Severity | Mitigation in Live Relay |
| :--- | :--- | :--- |
| **6-Digit Code Brute-Force** | High | *(Updated 2026-11-11; the original failed-attempt counter and 3-failure burn were removed.)*<br>1) Code expires in **3 minutes**.<br>2) `POST /api/pipe/join` is rate-limited by `FLICK_OPEN_RATE_PER_MIN` per client network (IPv4 address or IPv6 /64), one bucket across `/api/pipe` and `/api/v1/pipe`.<br>3) With confirmation on, a guesser who finds the code only joins the waiting room; the sender picks the row by the 3-digit number the real receiver reads out. See `docs/architecture/security-model.md` (Live Relay). |
| **Access Log / Proxy Leaks** | High | Code is transmitted **strictly in request headers (`X-Flick-Pipe-Code`)**, never in URLs or query strings. Access loggers redact this header. |
| **Storage Exhaustion / Disk DoS** | High | **0 bytes written to disk or S3**. Data flows through a 64 KiB RAM pipe. If sender or receiver closes the connection, the pipe tears down immediately. |
| **Connection Leak / OOM** | Medium | Hard context timeouts (max 10 minutes per transfer session). Bounded concurrent pipes (`FLICK_PIPE_MAX_CONCURRENT = 100`). Empty buffer pools reused via `sync.Pool`. |

---

## 5. Phased Implementation Plan

### Phase 1: API & In-Memory Coordinator (PR 1 - Issue #318)
- Update public contracts:
  - Add pipe endpoints to `contracts/openapi.yaml`.
  - Document `FLICK_PIPE_RELAY_ENABLED` and `FLICK_PIPE_MAX_CONCURRENT` in `docs/architecture/env-contract.md` and `.env.example`.
- Create `internal/piperelay` package:
  - `Coordinator` with mutex-protected map, timeout cleaner, and bounded concurrency.
  - Unit tests in `internal/piperelay/coordinator_test.go` verifying code collision prevention, TTL expiration, and pipe streaming.
- Register endpoints in `internal/httpapi/router.go`:
  - `POST /api/v1/pipe/init`
  - `GET /api/v1/pipe/wait` (long-poll notification when receiver connects)
  - `POST /api/v1/pipe/send`
  - `GET /api/v1/pipe/receive`
- Integration tests in `internal/httpapi/piperelay_test.go`.

### Phase 2: Web Client Crypto & Streaming Pipe (PR 2 - Issue #319)
- Implement `web/src/lib/crypto/pipe.ts`:
  - Client-side key derivation via Web Workers (`scrypt.worker.ts`).
  - Streaming encryption/decryption hooks compatible with `fetch` stream bodies.
- Unit tests in `web/src/lib/crypto/pipe.test.ts`.

### Phase 3: Frontend UI & User Interaction (PR 3 - Issue #320)
- In `web/src/lib/components/CreateSecretPage.svelte`:
  - Add `[ Direct Code Drop ]` action next to standard `Create secret link`.
  - Display large, readable 6-digit code `[ 741 - 928 ]` with 3:00 countdown timer and copy helper.
- In `web/src/lib/components/SiteHeader.svelte` and home view:
  - Add `[ Enter Code ]` quick-action button in header.
  - Dedicated code entry dialog: 6-digit numeric input with auto-submit.
- Add Playwright browser tests in `web/src/lib/components/LiveRelay.browser.mjs`:
  - Two simulated browser contexts: Sender creates code drop, Receiver enters code, verifies real-time decryption and file download.
  - Expired code error state and wrong code lockout handling.
- Verify light/dark theme styling, accessible names (`aria-label`), and mobile responsive layout.

---

## 6. Rollback & Operational Verification

- **Feature Toggle**:
  - Controlled by env var `FLICK_PIPE_RELAY_ENABLED` (default: `true`).
  - If disabled, endpoints return `503 Service Unavailable`, and UI hides the Code Drop option without breaking standard secret delivery.
- **Definition of Done (DOD)**:
  - `scripts/ci/all.sh` passes completely (contracts, lint, Go unit, Web check & test, integration tests).
  - Browser tests verify real-time transfer of a 10 MiB payload between two mock browser sessions without writing to S3.
  - Memory footprint remains stable (< 256 MiB) under concurrent stream stress.
