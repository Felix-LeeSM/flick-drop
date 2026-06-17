# Roadmap

## MVP

Goal: a deployable self-hosted one-time secret/file drop service.

In scope:

- SvelteKit web UI.
- Go API service.
- Go worker service.
- NATS JetStream job delivery.
- SQLite `api.db` and `worker.db`.
- Browser-side AES-GCM encryption.
- Required passphrase input with browser-side KDF.
- Text secret creation and one-time open.
- File secret creation and one-time download.
- TTL options: 10 minutes, 1 hour, 24 hours.
- Small payload storage in SQLite BLOBs.
- Larger encrypted file storage in OCI Object Storage.
- Worker cleanup for consumed and expired secrets.
- `/healthz`, `/readyz`, `/metrics`.
- Container image definitions for web, API, and worker.
- Generic Kubernetes manifests.
- Local NATS compose smoke test.

Out of scope:

- user accounts
- organizations or teams
- long-term storage
- server-side plaintext preview
- server-side content inspection
- public file drive behavior
- direct-to-Object-Storage browser upload
- complex admin dashboard

## Milestones

### 1. Repository Scaffold

- Go module and command skeletons.
- SvelteKit app skeleton.
- CI checks wired to real Go/web commands.
- OpenAPI and event schema validation.

### 2. Text Secret Flow

- Browser encrypts text payload.
- API stores ciphertext in `api.db`.
- API returns secret ID.
- Web creates ID-only share URL.
- Recipient enters passphrase and decrypts in browser.
- Consume blocks second open.

### 3. Worker and NATS Flow

- API outbox table.
- NATS JetStream stream and consumer.
- Worker job receipt table.
- Consumed/expired cleanup job.
- Idempotent retries and dead-letter handling.

### 4. File Flow

- Browser encrypts files.
- API stores files up to 1 MiB in SQLite BLOBs.
- File download decrypts in browser.
- Size and content-type limits enforced.

### Structured Credentials

- Browser-side credential templates for login, card, identity, and custom
  fields.
- Credential payloads are serialized as `FLCR1:` text and encrypted through the
  existing text-secret path.
- API, DB, OpenAPI, storage, and worker behavior remain unchanged: structured
  credentials are stored as encrypted `kind:"text"` payloads.
- `secret` field flags are UI masking hints, not server-enforced access
  controls.

### 5. OCI Object Storage

- OCI adapter for larger ciphertext payloads.
- Real dev bucket smoke test.
- Object delete cleanup job.
- Runbook for bucket, credentials, and lifecycle checks.

### 6. Kubernetes Deployment

- Web, API, and worker container images.
- Generic manifests for web, API, worker, NATS, PVCs, Secret, ConfigMap, and
  Ingress.
- k3d smoke test.
- OCI Free Tier resource budget verification.

### 7. Operational Hardening

- backup/restore runbook
- SQLite checkpoint/vacuum runbook
- rate limiting
- audit event viewer or export
- CSP and security header tightening
