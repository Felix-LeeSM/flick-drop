# Flick Service Concept

> **Status: original concept document, kept for orientation.** The durable
> source of truth is `docs/architecture/*` (start with
> [security model](docs/architecture/security-model.md),
> [storage model](docs/architecture/storage-model.md), and
> [service topology](docs/architecture/service-topology.md)) plus the contracts
> in `contracts/`. Statements here were corrected on 2026-11-12 where they had
> become wrong; where this document and those files disagree, those files win.

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
Text and file secrets use the same `/s/{id}` route.

Example links:

```text
https://drop.example.com/s/abc123
https://drop.example.com/s/abc123#key=AGE-SECRET-KEY-PQ-1...
```

With a passphrase (Model A, the default), the URL path contains only the secret
ID and the recipient enters the passphrase in the browser. Without one
(Model B), the decryption key travels in the URL fragment (`#key=...`), which
browsers do not send to the server. Either way, the API, ingress logs, NATS
messages, SQLite files, and object storage never receive the passphrase, the
derived key, or the decryption identity.

Two flows were added after this concept: one-time request links (`/request`,
`/r/{id}`; see [request links](docs/architecture/request-links.md)) and send
with code, a live relay through API memory with nothing stored (see "Live
Relay (Send with Code)" in the [security model](docs/architecture/security-model.md)).

## Security Model

Principle:

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

Upload flow:

1. Browser generates a fresh age identity for the secret and encrypts the text
   or file to it as a standard age file in 64 KiB chunks
   ([age format v1](contracts/age-format-v1.md)).
2. Model A: the browser wraps that identity in an age scrypt passphrase file
   and derives a separate PBKDF2 access proof from the same passphrase.
   Model B: the identity goes into the link fragment instead.
3. Browser uploads the age file, the age-encrypted filename, safe metadata, and
   for Model A the wrapped identity, access-proof KDF parameters, and access
   proof.
4. API stores a small age file in SQLite. For a larger file, the API returns a
   presigned PUT and the browser uploads the age file directly to object
   storage, then calls `/finalize`.
5. API returns the secret ID.
6. Browser creates the share link. A Model A passphrase must be shared through
   a separate channel or pre-agreed with the recipient.

Download flow:

1. Recipient opens the share link.
2. Model A: the browser asks for the passphrase and proves it with the access
   proof; the API releases the age file and wrapped identity only on a matching
   proof hash. Model B: the browser reads the identity from the fragment.
3. Browser decrypts locally and shows content only after the final chunk
   authenticates.
4. API deletes the secret row in the open transaction (one-time open).
5. The API reaper hard-deletes expired rows, and the worker receives
   object-delete jobs through NATS JetStream and deletes object-storage bodies
   of opened, revoked, or expired secrets.

Server-side components store:

- secret ID
- age file body (inline) or its object-storage backend and key
- Model A only: the passphrase-wrapped identity, access-proof KDF parameters,
  and the access proof hash
- age-encrypted filename
- content type
- age file size
- expiration time
- state and failed access-attempt count
- daily aggregate usage counters (`usage_counters`), with no IDs

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
max file size: 256 MiB
TTL presets: 10 minutes, 1 hour (default), 24 hours, 7 days
custom TTL: 5 minutes to 7 days (FLICK_MIN_TTL_SECONDS / FLICK_MAX_TTL_SECONDS)
default max views: 1
```

SQLite files:

```text
api.db (main tables; full list in docs/architecture/database-schema.md):
  secrets
  secret_payloads
  secret_management
  requests
  request_payloads
  outbox_events
  usage_counters

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
- browser-side age encryption (AES-GCM until M13 replaced it)
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

Direct-to-object-storage upload was listed as a non-goal here but is
implemented: large files upload with a presigned PUT
(`internal/storage/presigned.go`).

See [roadmap](docs/ROADMAP.md).
