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

Inline [request links](request-links.md) need no worker event: open/revoke
remove their BLOB and encrypted metadata in the same API SQLite transaction,
and the API reaper cascades payload deletion when purging expired requests.
No request capability, public key, envelope, or ciphertext enters the outbox.
Large requests reuse `delete_oci_object` and the existing reasons. Request
open/revoke/expiry/reservation expiry/abandon commit key-only cleanup with their
state transition. Request scans use reason `orphan` and independent
`request_reconciliation_cursor`/`request_reconciliation_pending` tables; the
event JSON shape does not change.

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

## M8 Cancellation and Reconciliation

[Sender management v1](../../contracts/sender-management-v1.md) cancellation commits
its outcome, API access block, inline removal, and required outbox rows together.
Existing deletion kinds and reason `manual` cover cancellation. Recurring scans
use `delete_oci_object` with reason `orphan`; the event JSON shape is unchanged.

The API scans one bounded page of `managed/secrets/` per reaper tick and keeps one
pending reconciliation job per object key plus a durable, generation-fenced cursor.
A scan never queues deletion of an unexpired live active or pending upload.
Database/list/enqueue failures cannot be treated as an absent live object or an
empty successful page. Claims have no lease timeout.

The API also scans one independent bounded page of `managed/requests/` each tick,
protecting active reservation keys and accepted final keys using request-owned
references. Failures in either scan do not suppress the other scan.

For every `delete_oci_object` in `managed/secrets/` or `managed/requests/`, the
worker persists the terminal success/dead receipt first, then calls
`POST /internal/object-reconciliation/ack` with its object key and job ID, before
NATS Ack/Term. The [internal endpoint contract](../../contracts/internal-api.md)
defines authentication and idempotency. Immediate cleanup jobs also acknowledge;
an ID that has no matching reconciliation claim is a harmless no-op.

API acknowledgement failures retry independently of the handler failure budget.
After a crash or terminal redelivery, the worker repeats the acknowledgement
without repeating DELETE. Both success and dead-letter acknowledgements release
the matching pending claim, allowing a subsequent listing to queue a fresh job ID
if an object remains or appears later. A stale acknowledgement cannot remove a
newer claim. A dead-letter receipt still records failure; it does not prove erasure.

For an upgrade, replace **all** workers with the acknowledgement-capable version
supporting **both namespaces** before enabling the new API scanner. A #201
worker supports the sender prefix only and must also be replaced for request
reconciliation. Wait for every old worker pod to disappear,
including terminating pods; Deployment rollout readiness alone is insufficient.
An old worker can DELETE and NATS Ack without releasing the API claim. If a late
PUT follows, that claim prevents later scans from scheduling another deletion.

During the worker-first rollout, an old API returns HTTP 404 for the acknowledgement
endpoint (or HTTP 400 for an unsupported request prefix). The updated worker
retains the NATS delivery for retry until the API is upgraded. Never roll back a worker to a version without acknowledgement support
while reconciliation jobs or claims can exist, even if the API is rolled back.
See the [safe upgrade procedure](../runbook/k3s-base.md#apply).

A worker with object storage disabled fails managed deletions instead of reporting
success. Recurring cleanup is eventual while the API, broker, worker, listing, and
delete permissions are available; no finite PUT-completion or deletion SLA is assumed.
