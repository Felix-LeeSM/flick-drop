# Storage Model

Flick stores only ciphertext and metadata on the server side.

## Databases

```text
api.db
  owner: flick-api
  contains: secrets, small ciphertext BLOBs, audit_events, outbox_events

worker.db
  owner: flick-worker
  contains: job receipts, attempts, dead-letter metadata, worker-local state

NATS JetStream filestore
  owner: nats
  contains: durable job messages with IDs and small metadata
```

The API and worker do not directly share SQLite write ownership.

## Payload Storage

Initial thresholds:

```text
text secret: api.db BLOB
file <= 1 MiB: api.db BLOB
file > 1 MiB: S3-compatible object storage when enabled
max file size: 50 MiB
```

The local web flow accepts files only up to the SQLite inline threshold while
large-object storage is disabled.

Filesystem storage is not a persistent backend. It is allowed only for temporary
upload files, local scratch, and test fixtures.

## Large-Object Storage

Large-object storage speaks the S3 API via the AWS SDK for Go v2
(`internal/storage`). Any S3-compatible provider works — MinIO dev, OCI
S3-compatibility prod, AWS S3, Cloudflare R2 — and MinIO is the integration-test
double (`minio_integration_test.go`).

Object Storage receives browser-encrypted ciphertext only. Bucket names,
credentials, presigned URLs, and production domains must not be committed to the
public repository.

## Deletion Semantics

Deleting a secret means Flick no longer serves the ciphertext and no server
component knows the passphrase or derived key.

SQLite BLOB deletion may leave bytes in WAL/freelist pages until checkpoint or
vacuum. Backups may also retain old ciphertext. Runbooks must document this
residual risk.

Cleanup jobs are idempotent:

- missing secret: success
- missing SQLite payload: success
- missing object-storage object: success
- already consumed/expired: success
