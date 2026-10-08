# Worker cleanup API

Both endpoints use `X-Flick-Internal-Token: <FLICK_INTERNAL_TOKEN>`, accept JSON,
and are implemented in `internal/httpapi/router.go`. A missing or invalid internal
token returns `401`. The worker never writes `api.db` directly.

## Payload cleanup

`POST /internal/secrets/{id}/cleanup` accepts `{"job_id":"<job-id>","reason":"consumed"}`.
The existing reasons are `consumed`, `expired`, `orphan`, `manual`, and `retry`.
It returns `200 {"id":"<id>","cleaned":true}` when an inline payload was removed;
`cleaned:false` is an idempotent already-absent result. Cleanup does not extend
management authority or erase an unexpired terminal management outcome.

## Terminal object acknowledgement

`POST /internal/object-reconciliation/ack` accepts exactly:

```json
{"job_id":"<job-id>","object_key":"managed/secrets/<opaque-id>"}
```

The body is bounded at 64 KiB. `job_id` must be nonblank, and the object key must
be inside `managed/secrets/` with a nonempty suffix. Other object namespaces are
rejected. Malformed/unknown JSON fields or invalid identifiers return `400`.
Database failures return `503`, never an acknowledgement.

A valid request returns `204` with no body. In `api.db`, it removes only the
`object_reconciliation_pending` row matching both key and job ID. Missing claims,
duplicate acknowledgements, and old job IDs return the same `204`; an old job
cannot clear a newer pending claim for the key. The request does not remove
objects or alter delivery state.

The worker calls only after its terminal success/dead receipt commits, including
on redelivery of a terminal receipt, and before NATS Ack/Term. API failures retry
acknowledgement without repeating completed DELETE operations or spending the
handler failure budget. Acknowledging a dead receipt releases its pending claim
for a future scan; the dead letter remains a failure record, not proof of deletion.
