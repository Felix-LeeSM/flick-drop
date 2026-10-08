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

## Planned request storage

M9 [request links](request-links.md) use separate API-owned request metadata
and inline ciphertext, with the original request deadline bounding both
submission and retrieval. Large request uploads will use attempt-specific
objects and a separate `managed/requests/` reconciliation namespace. Request
storage is not implemented yet; it must not be swept using sender-secret
live-row checks.

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

## M8 Management Retention and Planned Late-Upload Reconciliation

[Sender management v1](../../contracts/sender-management-v1.md) defines the
management lifecycle. #200 implements management records, status, and expiry;
object reconciliation remains planned in #201. Management hashes and minimal terminal outcomes expire at the
original secret deadline. Early payload cleanup does not erase that outcome;
expiry sweeps purge management records and consumed secret metadata after open
or lockout. Cancellation remains planned.
No account history or content-retention extension is introduced.

New managed S3 payloads use the exclusive, never-reused `managed/secrets/{id}` namespace.
Immediate transactional cleanup jobs remain the existing deletion path. Planned
recurring
API-owned `ListObjectsV2` reconciliation of `managed/secrets/` supplies the second path:
check live-row protection, enqueue deletion through the outbox, and let workers
delete. Persisted pagination and bounded per-key pending claims survive restarts.
Each late reappearance needs a fresh job ID after the preceding job terminates.
No finite PUT completion deadline or permanently retained secret tombstone is
assumed. Legacy keys and objects outside `managed/secrets/` are excluded.

Managed storage adds prefix listing to existing Get/Put/Delete permissions.
Reconciliation removes current objects; historical versions, replicas, backups,
and retention locks remain operator-owned residual risks. Failed listing or
blocked deletion must stay visible and retryable, not count as successful
erasure. No new bucket-setting startup gate or automatic policy change is
introduced. Provider lifecycle is an optional backstop, not an assumed
configuration or deletion SLA. #201 implements the contract's failure recovery
and pending-claim acknowledgements.
