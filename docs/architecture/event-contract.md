# Event Contract

BurnLink uses NATS JetStream for API to worker jobs.

## Stream

Initial configuration:

```text
stream: BURNLINK_JOBS
subject: burnlink.jobs
storage: file
retention: work queue
```

Exact deployment values come from environment variables:

- `BURNLINK_NATS_URL`
- `BURNLINK_NATS_STREAM`
- `BURNLINK_NATS_JOB_SUBJECT`

## Payload

The JSON schema is `contracts/events/job.schema.json`.

Payloads contain IDs and explicit small metadata fields only. Arbitrary
`payload` extension objects are not part of the initial contract. Payloads must
not contain:

- plaintext secret contents
- passphrases
- derived keys
- ciphertext bodies
- production credentials

Example:

```json
{
  "job_id": "job_01hxy",
  "kind": "delete_secret",
  "secret_id": "sec_01hxx",
  "reason": "expired",
  "requested_at": "2026-06-16T03:00:00Z",
  "trace_id": "trc_01hxz"
}
```

## Outbox

The API publishes through an outbox table:

1. Commit the domain change and `outbox_events` row in one `api.db`
   transaction.
2. A publisher loop reads pending rows.
3. Publish to NATS JetStream and wait for ack.
4. Mark the outbox row as `published`.
5. Retry failed publishes with backoff.

This prevents a successful API write from losing the worker job if the broker is
temporarily unavailable.

The publisher sends the stored `payload_json` bytes to
`BURNLINK_NATS_JOB_SUBJECT`. On publish ack it marks the outbox row
`published`; on publish failure it records the error and schedules the next
attempt.

## Consumer

The worker uses a durable pull consumer for the job subject.

Initial consumer defaults:

```text
durable: burnlink-worker
ack policy: explicit
max deliver: 3
batch size: 8
```

Message disposition:

- valid job processed successfully or already completed: ack
- duplicate delivery while the same job is already processing: ack
- transient processing error before the retry limit: nak
- invalid payload: term
- terminal dead-letter result: term

Invalid payloads are not retried because they cannot become valid without a new
producer write. Dead-lettered jobs are not retried because the worker already
recorded the terminal state in `worker.db`.

## Worker Semantics

Worker jobs are at-least-once. Every handler must be idempotent.

Expected behavior:

- duplicate `job_id`: do not repeat side effects after success
- missing secret: success for cleanup jobs
- missing OCI object: success for cleanup jobs
- transient API/OCI/NATS error: retry
- repeated failure: dead-letter with error summary

The worker owns `worker.db` and records receipts, attempts, and dead letters.
The worker calls internal API endpoints for API-owned mutations.
