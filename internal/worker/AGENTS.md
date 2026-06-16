# `internal/worker/` Guide

This package owns worker job execution, retry, idempotency, and dead-letter
behavior.

Directory structure:

- Keep runner setup, job handlers, receipts, retries, and dead-letter behavior
  easy to locate.
- If job families become large, add named subdirectories with local `AGENTS.md`
  files before splitting.
- Do not add API-owned database repositories here.

Rules:

- Worker state belongs in `worker.db`.
- API-owned mutations must go through internal API calls.
- Every job handler must be safe to replay after partial success.
- Job payloads should contain IDs and safe metadata only.
