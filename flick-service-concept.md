# Flick Service Concept

Flick is a self-hosted, open-source service for sharing short-lived secrets
and files through one-time links.

The service is intentionally small enough to deploy on modest infrastructure,
including an OCI Always Free-style environment, while still keeping separate web,
API, worker, broker, storage, and deployment contracts.

## Purpose

Flick exists for temporary delivery, not long-term storage.

Core goals:

- Do not keep secrets longer than needed.
- Do not store plaintext secrets on the server.
- Do not send passphrases or derived keys to the API, worker, NATS, OCI, logs,
  or metrics.
- Default to one-time open or one-time download.
- Automatically remove consumed or expired data.
- Make a public repository safe by keeping credentials and private deployment
  settings outside the repo.

Flick is not a password manager, team vault, permanent file drive, or Dropbox
replacement.

## Product Flow

Users create a text or file secret in the web UI and share the generated link.

Example links:

```text
https://drop.example.com/s/abc123
https://drop.example.com/f/xyz789
```

The URL contains only the secret ID. The recipient must enter the passphrase in
the browser to decrypt the payload. The API, ingress logs, NATS messages,
SQLite files, and S3-compatible object storage must never receive the passphrase or the
derived key.

## Security Model

Principle:

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

Upload flow:

1. Browser asks the sender for a passphrase.
2. Browser generates a random salt and derives an encryption key from the
   passphrase.
3. Browser encrypts text or file data with Web Crypto AES-GCM.
4. Browser uploads ciphertext, nonce, KDF salt/parameters, and safe metadata.
5. API stores small ciphertext in SQLite or larger ciphertext in OCI Object
   Storage.
6. API returns the secret ID.
7. Browser creates an ID-only share link. The passphrase must be shared through
   a separate channel or pre-agreed with the recipient.

Download flow:

1. Recipient opens the share link.
2. Browser asks the recipient for the passphrase.
3. Browser requests ciphertext and KDF metadata by ID.
4. Browser derives the key from the passphrase and decrypts locally.
5. API records consume state.
6. Worker receives cleanup jobs through NATS JetStream and deletes consumed or
   expired data.

Server-side components store:

- secret ID
- ciphertext location
- nonce
- KDF algorithm, salt, and parameters
- encrypted filename
- content type
- ciphertext size
- expiration time
- consume state
- audit events without sensitive values

Server-side components never store:

- plaintext text
- plaintext files
- passphrases
- derived keys
- plaintext filenames

See [security model](docs/architecture/security-model.md).

## Runtime Shape

Initial services:

```text
flick-web:
  SvelteKit UI

flick-api:
  Go HTTP/JSON API
  owner of api.db
  outbox publisher to NATS JetStream

flick-worker:
  Go worker
  owner of worker.db
  NATS JetStream consumer
  cleanup and async job executor

nats:
  NATS JetStream broker
  durable file-backed job stream
```

The API and worker are separate services. They do not directly share SQLite
write ownership. The worker calls internal API endpoints when a job needs to
mutate API-owned state.

See [service topology](docs/architecture/service-topology.md).

## Storage Strategy

Initial storage policy:

```text
text secret: api.db BLOB
file <= 1 MiB: api.db BLOB
file > 1 MiB: S3-compatible object storage when enabled
max file size: 50 MiB
TTL options: 10 minutes, 1 hour, 24 hours
default max views: 1
```

SQLite files:

```text
api.db:
  secrets
  secret_payloads
  audit_events
  outbox_events

worker.db:
  job_receipts
  job_attempts
  dead_letters
```

Filesystem storage is not a persistent backend. It is only for temporary files,
local scratch, or test fixtures.

S3-compatible object storage is tested with a real development bucket via the
MinIO integration test, then verified against the real OCI bucket in
S3-compatibility mode. MinIO is S3-compatible but is not an OCI simulator, so
it is not the default development stand-in for OCI behavior.

See [storage model](docs/architecture/storage-model.md).

## OCI Free Tier Target

Flick should be deployable without managed database services.

The intended small deployment uses:

- small compute nodes
- Kubernetes persistent volume for SQLite and NATS data
- OCI Object Storage (S3-compatibility mode) for larger encrypted files
- Kubernetes Secret/ConfigMap for runtime configuration
- Ingress with HTTPS termination

OCI Free Tier policies, quotas, and request limits can change. Operators must
verify current tenancy limits before production use.

See [deployment target](docs/architecture/deployment-target.md).

## Public Repository Boundary

This repository may contain:

```text
application source code
web source code
Dockerfile
local development compose file
example environment file
generic deploy/base manifests
documentation
threat model
runbook templates
```

This repository must not contain:

```text
OCI API private keys
~/.oci/config
~/.ssh/*
kubeconfig
.env.local
admin token
Object Storage pre-authenticated request URLs
actual bucket credentials
production domain settings
SQLite database files
PVC dumps
backup archives
```

Production-specific configuration should live in a private ops repository or a
local private overlay.

## MVP Scope

MVP includes:

- text secret creation
- file secret creation
- browser-side AES-GCM encryption
- one-time consume flow
- TTL expiration
- SQLite BLOB storage for small payloads
- S3-compatible object storage adapter for larger payloads
- NATS JetStream job delivery
- worker cleanup loop
- `/healthz`, `/readyz`, `/metrics`
- generic k8s manifests

Initial non-goals:

- accounts and organizations
- long-term file storage
- server-side plaintext preview
- server-side content inspection
- public file drive behavior
- complex admin dashboard
- direct-to-object-storage upload

See [roadmap](docs/ROADMAP.md).
