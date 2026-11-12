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

`POST /internal/object-reconciliation/ack` accepts exactly these fields
(`internal/httpapi/secrets.go:acknowledgeObjectCleanup`):

```json
{"backend":"s3_object","job_id":"<job-id>","object_key":"managed/secrets/<opaque-id>"}
```

`backend` is optional and names the bucket the finished delete ran against
(`s3_object` primary, `r2_object` secondary). The worker copies it from the job
event's `backend` field (`internal/worker/cleanup.go:AcknowledgeObjectCleanup`)
and omits it when the event has none. An absent or empty `backend` means the
primary, which is where every claim written by an API predating dual-backend
support lives. The API does not validate the value against the enum; an unknown
backend matches no claim and returns the same `204` as a missing claim.

The body is bounded at 64 KiB. `job_id` must be nonblank, and the object key must
be inside `managed/secrets/` or `managed/requests/` with a nonempty suffix. Other object namespaces are
rejected. Malformed/unknown JSON fields or invalid identifiers return `400`.
Database failures return `503`, never an acknowledgement.

A valid request returns `204` with no body. In `api.db`, it removes only the
`object_reconciliation_pending_v2` (sender) or `request_reconciliation_pending_v2`
(request) row matching backend, key, and job ID
(`internal/secrets/reconciliation.go:AcknowledgeObjectCleanup`,
`internal/requests/cleanup.go:AcknowledgeObjectCleanup`). The prefix selects the owner; one
namespace can never clear the other namespace's claim. Missing claims,
duplicate acknowledgements, and old job IDs return the same `204`; an old job
cannot clear a newer pending claim for the key. The request does not remove
objects or alter delivery state.

The worker calls only after its terminal success/dead receipt commits, including
on redelivery of a terminal receipt, and before NATS Ack/Term. API failures retry
acknowledgement without repeating completed DELETE operations or spending the
handler failure budget. Acknowledging a dead receipt releases its pending claim
for a future scan; the dead letter remains a failure record, not proof of deletion.

Upgrade every worker to acknowledge **both namespaces** before enabling the
request scanner. A worker from #201 may acknowledge sender keys but still drop
request cleanup deliveries without the API acknowledgement. During the worker-first
rollout, an older API can return 400 for the new prefix (or 404 for the endpoint);
the worker retains the delivery for retry. Never roll workers back to a version
that omits either active namespace while reconciliation jobs/claims can exist.
