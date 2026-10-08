# Event Contract

Flick uses NATS JetStream for API to worker jobs.

## Stream

Initial configuration:

```text
stream: FLICK_JOBS
subject: flick.jobs
storage: file
retention: work queue
```

Exact deployment values come from environment variables:

- `FLICK_NATS_URL`
- `FLICK_NATS_STREAM`
- `FLICK_NATS_JOB_SUBJECT`

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
`FLICK_NATS_JOB_SUBJECT`. On publish ack it marks the outbox row
`published`; on publish failure it records the error and schedules the next
attempt.

## Consumer

The worker uses a durable pull consumer for the job subject.

Initial consumer defaults:

```text
durable: flick-worker
ack policy: explicit
max deliver: unlimited (-1)
batch size: 8
retry delay: 5 seconds
```

Message disposition:

- valid job processed successfully or already completed: ack
- duplicate delivery while the same job is already processing: delayed nak
- transient processing error before the retry limit: delayed nak
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

`internal/worker/store.go` claims each attempt for one minute, measured from
`job_receipts.updated_at`. A delivery during that lease retries without invoking
the handler or acknowledging the job. After the lease expires, the next delivery
closes the interrupted attempt and claims a new one in the same transaction.
This recovers persisted receipts after a crash without resetting live work when
another worker process starts during a rolling deployment. Receipt state still
fences late completion by attempt number. Workers sharing a database must use
the same clock; scaling across separate worker databases remains unsupported.

`internal/worker/processor.go` gives each handler a 30-second context deadline,
leaving the rest of the lease for cancellation and receipt persistence. Handlers
must honor that context and remain idempotent: a crash after deletion but before
completion can repeat a deletion. Shutdown cancellation leaves the receipt for
lease recovery. Expired attempts are recorded as `failed` with a NULL `error`,
which distinguishes an unknown outcome from a reported handler failure.

Three recorded handler failures dead-letter a job. Interruptions, active
duplicates, and receipt-storage errors do not spend that budget. JetStream
delivery is unlimited because the broker cannot distinguish those cases from
handler failures. A permanent worker-database failure therefore needs operator
intervention; it cannot silently exhaust broker delivery and hide cleanup.

Recording a failure, checking its budget, and writing a terminal receipt/dead
letter happen in one transaction, fenced by the current attempt number. A failed
dead-letter write rolls back the attempt failure too. Before starting a retry, the worker
also checks persisted failures: a receipt left by an older worker after its
third failure becomes dead-lettered without another handler call. An existing
live processing lease still expires before recovery can change its receipt.

For receipts or messages stranded by an older release, see
[worker cleanup recovery](../runbook/worker-recovery.md).
