# `internal/storage/` Guide

This package owns the S3-compatible object store for large secret payloads
(SQLite BLOB stays the default for small payloads in `internal/secrets`).

Directory structure:

- Keep backend-specific code named by surface: S3 client construction, presigned
  POST signing, and object HEAD/GET/DELETE wrappers live in named files.
- Do not add generic `blob`, `file`, or `utils` directories without a clear
  ownership boundary.

Rules:

- Storage adapters see ciphertext and safe metadata only — never plaintext or keys.
- Presigned POST is built manually (`presigned.go`) because the Go AWS SDK v2
  only supports presigned PUT. The signature derives the standard SigV4 key chain
  (`deriveSigningKey`) and signs the base64 policy directly; the AWS SigV4 test
  vector in `presigned_test.go` is the regression anchor.
- S3-compatible behavior must be verified against a real MinIO bucket
  (`minio_integration_test.go`, build tag `integration`) — it is the only proof
  the manual signing interoperates with a bucket.
- Auth is a static key pair (MinIO dev / OCI Customer Secret Key prod). Instance
  principal is deferred because the AWS SDK cannot speak it directly.
- Cleanup operations must be idempotent (a missing object on DELETE is not an error).
