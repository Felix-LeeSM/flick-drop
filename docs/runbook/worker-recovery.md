---
type: Runbook
title: Worker Cleanup Recovery
description: Requeueing previously stranded worker cleanup jobs from flick_api outbox_events within PublishedRetention while preserving idempotency.
tags: [runbook, worker, recovery]
status: stable
---

# Worker Cleanup Recovery

Current workers retry interrupted cleanup after the one-minute receipt lease
(`ProcessingLease` in `internal/worker/store.go`) expires. Duplicate delivery
during a live lease retries without acknowledging the message. See the
[event contract](../architecture/event-contract.md). Redelivery also completes
dead-lettering for an unfinished receipt that already has three recorded
handler failures; the worker does not run cleanup again.

An older worker could acknowledge a `processing` receipt without completing its
cleanup, or exhaust the former three-delivery broker limit. Updating the worker
cannot recreate a message already removed from JetStream. `flick_worker`
receipts contain the job ID and kind, not the object key or full event, and an
object delete's dead letter omits its key; the matching `outbox_events` row in
`flick_api` is the recovery source. The API reaper prunes published outbox rows
one hour after publish (`PublishedRetention` in `internal/events/outbox.go`), so
recover within that hour; after it, managed object keys are found again by
object reconciliation. Do not infer object keys or delete a bucket.

All SQL below runs in `psql` inside the postgres pod, which connects as the
`postgres` superuser over the local socket. The `flick-api` and `flick-worker`
images have no `psql`.

```sh
# Worker database (job_receipts, job_attempts, dead_letters)
kubectl -n flick exec -it deploy/postgres -- psql -U postgres -d flick_worker

# API database (outbox_events)
kubectl -n flick exec -it deploy/postgres -- psql -U postgres -d flick_api
```

## Recover a Previously Stranded Job

1. Upgrade the worker first. Check that the durable consumer now has
   `MaxDeliver = -1` (`DefaultMaxDeliver` in `internal/events/consumer.go`).
   Make sure no old worker pod remains, including terminating pods left from a
   rolling deployment; an old worker could consume the replayed message and
   acknowledge it again without cleanup:

   ```sh
   kubectl -n flick get pods -l app.kubernetes.io/name=flick-worker -o wide
   ```

   Take a [database backup](backup-restore.md#back-up-the-databases) before
   changing any row. flick-api and flick-worker can keep running: PostgreSQL
   serves both while you inspect and update, and the requeue in step 4 is one
   guarded statement.
2. In `flick_worker`, list unfinished receipts and inspect the affected job
   before selecting it:

   ```sql
   SELECT job_id, kind, state, attempts, updated_at, last_error
   FROM job_receipts WHERE state IN ('processing', 'failed');
   ```

   `succeeded` receipts need no replay. Investigate `dead` receipts separately
   (`SELECT job_id, kind, error, created_at FROM dead_letters;`); replaying a
   dead receipt does not reset its terminal decision.
3. In `flick_api`, find the exact outbox row by that job ID. The outbox row ID
   is the event's job ID (`EnqueueTx` in `internal/events/outbox.go`). Replace
   `job_REPLACE_ME` in this step and in step 4 with the inspected ID:

   ```sql
   SELECT id, subject, payload_json, state, published_at FROM outbox_events
   WHERE id = 'job_REPLACE_ME';
   ```

   Require exactly one matching row and verify the event's `job_id`, `kind`,
   `secret_id` or `object_key`, and cleanup reason against the incident. If the
   row is missing, stop and recover it from a verified backup; receipt metadata
   alone cannot reconstruct object deletion.
4. In `flick_api`, requeue only that inspected row. Preserve the original event
   and job ID so the worker's idempotency checks still apply:

   ```sql
   BEGIN;
   UPDATE outbox_events
   SET state = 'pending', next_attempt_at = now(), updated_at = now()
   WHERE id = 'job_REPLACE_ME' AND state = 'published'
   RETURNING id, state, next_attempt_at;
   -- psql must print exactly one returned row and "UPDATE 1".
   COMMIT;   -- or ROLLBACK; if it printed "UPDATE 0"
   ```

   Require `UPDATE 1` before `COMMIT`. `UPDATE 0` means the row is no longer
   `published`: a `pending` or `failed` outbox row already belongs to the
   publisher's retry flow and needs investigation rather than a broader update,
   and a row the reaper already pruned needs the backup path from step 3. Never
   clear worker receipts or rewrite all published outbox rows.

   The publisher on any flick-api replica claims the row on its next tick
   (`ClaimDueTx` picks `pending` rows whose `next_attempt_at` has passed) and
   marks it `published` again with a new `published_at`.
5. Allow the lease and retry delay to pass. Verify the selected receipt reaches
   `succeeded` in `flick_worker`, or inspect its dead letter if the underlying
   deletion still fails:

   ```sql
   SELECT job_id, state, attempts, completed_at, last_error
   FROM job_receipts WHERE job_id = 'job_REPLACE_ME';
   ```

   For an object-delete job, verify absence of that exact object through the
   configured object store. A published outbox row alone does not prove
   deletion.
