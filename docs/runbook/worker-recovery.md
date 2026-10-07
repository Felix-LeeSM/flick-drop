# Worker Cleanup Recovery

Current workers retry interrupted cleanup after the one-minute receipt lease
expires. Duplicate delivery during a live lease retries without acknowledging
the message. See the [event contract](../architecture/event-contract.md).

An older worker could acknowledge a `processing` receipt without completing its
cleanup, or exhaust the former three-delivery broker limit. Updating the worker
cannot recreate a message already removed from JetStream. `worker.db` receipts
contain the job ID and kind, not the object key or full event; the matching API
outbox row is the recovery source. Do not infer object keys or delete a bucket.

## Recover a Previously Stranded Job

1. Upgrade the worker first. Check that the durable consumer now has
   `MaxDeliver = -1`. Stop API and worker writers, back up both SQLite files,
   and use the maintenance pods described in
   [SQLite maintenance](sqlite-maintenance.md). Stop all replicas, including old
   pods left from a rolling deployment.
2. In the worker maintenance pod, list unfinished receipts and inspect the
   affected job before selecting it:

   ```sql
   SELECT job_id, kind, state, attempts, updated_at, last_error
   FROM job_receipts WHERE state IN ('processing', 'failed');
   ```

   `succeeded` receipts need no replay. Investigate `dead` receipts separately;
   replaying a dead receipt does not reset its terminal decision.
3. In the API maintenance pod, find the exact outbox row by that job ID. Replace
   `job_REPLACE_ME` in both statements with the inspected ID:

   ```sql
   SELECT id, subject, payload_json, state FROM outbox_events
   WHERE id = 'job_REPLACE_ME';
   ```

   Require exactly one matching row and verify the event's `job_id`, `kind`,
   `secret_id` or `object_key`, and cleanup reason against the incident. If the
   row is missing, stop and recover it from a verified backup; receipt metadata
   alone cannot reconstruct object deletion.
4. Requeue only that inspected row in `api.db`. Preserve the original event and
   job ID so the worker's idempotency checks still apply:

   ```sql
   BEGIN IMMEDIATE;
   UPDATE outbox_events SET state = 'pending',
     next_attempt_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
   WHERE id = 'job_REPLACE_ME' AND state = 'published';
   SELECT changes();
   COMMIT;
   ```

   Require `changes() = 1`; a pending or failed outbox row already belongs to
   the publisher's retry flow and needs investigation rather than a broader
   update. Never clear worker receipts or rewrite all published outbox rows.
5. Remove maintenance pods, restore the prior API and worker replica counts,
   and allow the lease and retry delay to pass. Verify the selected receipt
   reaches `succeeded`, or inspect its dead letter if the underlying deletion
   still fails. For an object-delete job, verify absence of that exact object
   through the configured object store. A published outbox row alone does not
   prove deletion.
