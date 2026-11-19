# `internal/storage/` Guide

This package owns the S3-compatible object store for large secret payloads
(inline `bytea` rows stay the default for small payloads in `internal/secrets`).

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
- `registry.go:Registry` holds one or more operator-named backends, built from
  `FLICK_STORAGE_BACKENDS` (`internal/config/config.go:loadStorageBackends`).
  A name must satisfy `registry.go:ValidBackendName` (`^[a-z][a-z0-9]{0,15}$`)
  because it is stored in the `storage_backend` column and in delete events,
  and it can never equal the inline value `sqlite_blob`. `registry.go:NewRegistry`
  rejects duplicate names and, when several backends are configured, any
  backend without a positive `MaxTotalBytes`.
- `registry.go:Registry.For` resolves the `storage_backend` column value of a
  row and returns an error for any name that is not configured (an empty name
  included), so a read or delete never silently targets the wrong bucket.
- `registry.go:Registry.Choose` places a new object: among backends whose
  ceiling still fits it (`MaxTotalBytes` 0 = unlimited, allowed only for a
  single backend), it picks at random weighted by remaining space
  (`MaxTotalBytes` minus usage from `internal/db/storage_usage.go:StorageObjectBytes`).
  No backend is primary. It returns `""` when no backend has room, which callers
  map to `storage_full`. `registry_test.go` drives the weighting through
  `registry.go:chooseWith` with an injected random source.
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
