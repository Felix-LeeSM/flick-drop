# `internal/storage/` Guide

This package owns the S3-compatible object store for large secret payloads
(SQLite BLOB stays the default for small payloads in `internal/secrets`).

Directory structure:

- Keep backend-specific code named by surface: S3 client construction
  (`client.go`), presigned PUT signing (`presigned.go`), object
  PUT/HEAD/GET/DELETE wrappers (`object.go`), prefix listing (`list.go`), and
  the named-backend registry (`registry.go`) live in named files.
- Do not add generic `blob`, `file`, or `utils` directories without a clear
  ownership boundary.

Rules:

- Storage adapters see ciphertext and safe metadata only — never plaintext or keys.
- Browser uploads use a presigned PUT: `presigned.go:Client.PresignPUT` calls
  the AWS SDK v2 `s3.NewPresignClient(...).PresignPutObject` with
  `ContentLength` set, so the signature covers `Content-Length` and the bucket
  rejects a body that is not exactly the reserved size. The returned
  `client.go:UploadInstruction.Headers` must be echoed verbatim by the client.
  Presigned POST is not used because Oracle's S3 Compatibility API does not
  implement POST Object (explained in the `presigned.go:Client.PresignPUT` doc
  comment). `presigned_test.go:TestPresignPUTSignatureCoversSize` is the unit
  regression anchor.
- S3-compatible behavior must be verified against a real MinIO bucket
  (`minio_integration_test.go`, build tag `integration`;
  `TestMinIORejectsOversized` proves the signed `Content-Length` is enforced by
  a real bucket).
- `registry.go:Registry` holds up to two named backends:
  `registry.go:PrimaryBackend` (`s3_object`) and the optional
  `registry.go:SecondaryBackend` (`r2_object`). `Registry.For` resolves the
  `storage_backend` column value of a row (empty means primary) and returns an
  error for an unknown or unconfigured backend, so a delete never silently
  targets the wrong bucket. `registry.go:ChooseBackend` places a new object on
  the primary first and on the secondary only when the primary's byte ceiling
  cannot fit it; it returns `""` when no backend has room.
- Auth is a static key pair (MinIO dev / OCI Customer Secret Key prod). Instance
  principal is deferred because the AWS SDK cannot speak it directly.
- Cleanup operations must be idempotent (a missing object on DELETE is not an error).

- `list.go:Client.List` uses `ListObjectsV2` for bounded prefix pages. Listing
  failures return errors; `ErrInvalidCursor` permits the API reaper to restart
  a persisted pass. Listing errors must not expose private endpoint URLs.

- `object.go:Client.PutStream` writes request finalize's verified stream to a
  server-only final key, spooling through a temporary file so the SDK signs a
  seekable body. Its `verify` callback runs after the spool and before the PUT,
  so finalize refuses bad bytes without writing them. Request finalize must not
  COPY a mutable staging object: the bytes it hashes are the bytes it writes.
