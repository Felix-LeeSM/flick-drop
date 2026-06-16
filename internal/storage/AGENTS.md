# `internal/storage/` Guide

This package owns encrypted payload storage adapters for SQLite BLOB and OCI
Object Storage.

Directory structure:

- Keep backend-specific code named by backend, such as SQLite payload storage
  and OCI object storage.
- If backend implementations need subdirectories, use backend names and add
  local `AGENTS.md` files.
- Do not add generic `blob`, `file`, or `utils` directories without a clear
  ownership boundary.

Rules:

- Storage adapters see ciphertext and safe metadata only.
- OCI behavior should be verified against a real development bucket for
  provider-specific behavior.
- Local filesystem storage is not the default persistence model.
- Cleanup operations must be idempotent.
