# Storage Model

BurnLink stores only ciphertext and metadata on the server side.

## Databases

```text
api.db
  owner: burnlink-api
  contains: secrets, small ciphertext BLOBs, audit_events, outbox_events

worker.db
  owner: burnlink-worker
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
file > 1 MiB: OCI Object Storage when enabled
max file size: 25 MiB
```

Filesystem storage is not a persistent backend. It is allowed only for temporary
upload files, local scratch, and test fixtures.

## OCI Object Storage

OCI is tested with a real development bucket, not MinIO. MinIO is S3-compatible,
not an OCI simulator, and is not part of the default local development path.

Object Storage receives browser-encrypted ciphertext only. Bucket names,
credentials, pre-authenticated URLs, and production domains must not be committed
to the public repository.

## Deletion Semantics

Deleting a secret means BurnLink no longer serves the ciphertext and no server
component knows the passphrase or derived key.

SQLite BLOB deletion may leave bytes in WAL/freelist pages until checkpoint or
vacuum. Backups may also retain old ciphertext. Runbooks must document this
residual risk.

Cleanup jobs are idempotent:

- missing secret: success
- missing SQLite payload: success
- missing OCI object: success
- already consumed/expired: success
