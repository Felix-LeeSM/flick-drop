# Roadmap

## MVP

Goal: a deployable self-hosted one-time secret/file drop service.

In scope:

- SvelteKit web UI.
- Go API service.
- Go worker service.
- NATS JetStream job delivery.
- SQLite `api.db` and `worker.db`.
- Browser-side AES-GCM encryption.
- Model A passphrase input with browser-side KDF, or Model B random-key links.
- Text secret creation and one-time open.
- File secret creation and one-time download.
- Preset and custom TTLs within runtime bounds; defaults are 5 minutes to
  7 days (`FLICK_MIN_TTL_SECONDS` / `FLICK_MAX_TTL_SECONDS` in `.env.example`).
- Small payload storage in SQLite BLOBs.
- Larger encrypted file storage in S3-compatible object storage.
- Worker cleanup for consumed and expired secrets.
- `/healthz`, `/readyz`, `/metrics`.
- Container image definitions for web, API, and worker.
- Generic Kubernetes manifests.
- Local NATS compose smoke test.

Out of scope:

- user accounts
- organizations or teams
- long-term storage
- server-side plaintext preview
- server-side content inspection
- public file drive behavior
- complex admin dashboard

## Milestones

### 1. Repository Scaffold

- Go module and command skeletons.
- SvelteKit app skeleton.
- CI checks wired to real Go/web commands.
- OpenAPI and event schema validation.

### 2. Text Secret Flow

- Browser encrypts text payload.
- API stores ciphertext in `api.db`.
- API returns secret ID.
- Web creates an ID path; Model B includes the browser-only `#key=...` fragment.
- Recipient decrypts in the browser with the passphrase or fragment key.
- Consume blocks second open.

### 3. Worker and NATS Flow

- API outbox table.
- NATS JetStream stream and consumer.
- Worker job receipt table.
- Consumed/expired cleanup job.
- Idempotent retries and dead-letter handling.

### 4. File Flow

- Browser encrypts files.
- API stores files up to 1 MiB in SQLite BLOBs.
- File download decrypts in browser.
- Size and content-type limits enforced.

### Structured Credentials

- Browser-side credential templates for login, card, identity, and custom
  fields.
- Credential payloads are serialized as `FLCR1:` text and encrypted through the
  existing text-secret path.
- API, DB, OpenAPI, storage, and worker behavior remain unchanged: structured
  credentials are stored as encrypted `kind:"text"` payloads.
- `secret` field flags are UI masking hints, not server-enforced access
  controls.

### 5. S3-Compatible Object Storage

- S3-compatible adapter (AWS SDK for Go v2) for larger ciphertext payloads.
- Direct browser ciphertext upload using a presigned PUT, followed by API
  finalization.
- MinIO integration test as the interop double.
- Real dev bucket smoke test.
- Object delete cleanup job.
- Runbook for bucket, credentials, and lifecycle checks.

### 6. Kubernetes Deployment

- Web, API, and worker container images.
- Generic manifests for web, API, worker, NATS, PVCs, Secret, ConfigMap, and
  Ingress.
- k3d smoke test.
- OCI Free Tier resource budget verification.

### 7. Operational Hardening

- backup/restore runbook ([runbook](runbook/backup-restore.md))
- SQLite checkpoint/vacuum runbook ([runbook](runbook/sqlite-maintenance.md))
- rate limiting
- audit event viewer or export
- CSP and security header tightening

## Planned Product Milestones

The historical implementation slices above are not GitHub milestone numbers.
The following GitHub milestones are planned work, not completed features.

### M8: Sharing UX and Anonymous Link Management

Goal: improve the free one-time sharing flow without accounts.

- [#197](https://github.com/Felix-LeeSM/flick-drop/issues/197): native recipient sharing.
- [#198](https://github.com/Felix-LeeSM/flick-drop/issues/198): truthful encrypted-file upload progress.
- [#199](https://github.com/Felix-LeeSM/flick-drop/issues/199) →
  [#200](https://github.com/Felix-LeeSM/flick-drop/issues/200) →
  [#201](https://github.com/Felix-LeeSM/flick-drop/issues/201) →
  [#202](https://github.com/Felix-LeeSM/flick-drop/issues/202): management contract,
  capability/status API, atomic cancellation/cleanup, and management page.
- Follow [sender management v1](../contracts/sender-management-v1.md): separate
  recipient/management links, original-TTL authority, no recovery, and repeated
  object reconciliation for late PUTs.
- Verify browser create/manage/share/open, refresh/new-device limits, legacy
  compatibility, token separation, migrations, races, and inline/S3 cleanup.
- Exclude accounts, dashboards, new CLI commands, browser extensions, larger
  file limits, and advertising integration.

### M9: One-Time Request Links

Goal: one request → one accepted text/file submission → one requester retrieval
→ expiry/cleanup, without a persistent inbox.

- Follow [request links v1](architecture/request-links.md) for cryptography,
  public submission versus private retrieval authority, key custody, and
  retention before API/browser implementation (#203–#207).
- Implement inline delivery and then large-object upload/finalization/cleanup;
  S3 verification is required before declaring the milestone complete.
- Reuse reviewed M8 capability/lifecycle decisions, never an encryption key as
  an owner credential. Preserve existing outbound web and CLI shares.
- Verify crypto vectors, duplicate/racing submission and retrieval, missing or
  wrong capabilities, abandoned uploads, expiry, and actual object cleanup.
- Exclude repeated collection, teams, accounts, recovery, server-held keys, and
  changes to the existing outbound encryption protocol.

### M10: Public Discovery and Advertising Experiment

Goal: measure useful public traffic and optional sponsorship against operating
cost and usability; revenue and approval remain unknown until measured.

- Complete the advertising/security boundary and measurement plan before public
  guides or an optional first-party static sponsor placement. Research may run
  alongside M8/M9; creating the milestone enables no advertising.
- Review current official publisher policy, public/sensitive origin separation,
  CSP/network behavior, crawl metadata, mobile access, and absence of secret
  IDs, keys, tokens, or content in sponsor requests/metrics. Define an experiment
  window and explicit continue/stop criteria.
- Sponsor placement stays disabled without reviewed creative/configuration.
  Exclude third-party scripts on sensitive pages, profiling, mandatory ad
  interactions, provider enrollment, paid acquisition, and paid account features.

### M11: Mobile Sharing UX

Goal: make the create, request, submit, and retrieval pages comfortable on
phones, and let request submitters send the same secret types as the create page.

- [#235](https://github.com/Felix-LeeSM/flick-drop/issues/235): request
  submission with the shared secret-type selector and structured credentials,
  using the existing `FLCR1:` text encoding.
- [#236](https://github.com/Felix-LeeSM/flick-drop/issues/236): phone layouts
  for the type selector, lifetime picker, and private pages; concise request-page
  copy with a visible waiting state.
- Verify browser create/request/submit/receive flows at phone and desktop widths,
  reduced motion, and the deployed frontend.
- Exclude API, worker, crypto, or request-contract changes; new secret types;
  file-limit changes; accounts, inboxes, or repeat collection.

### M12: Visual Polish

Goal: apply the chosen design direction, which keeps the teal and serif
identity, and fix the size, hierarchy, and consistency problems found in the
2026-10-11 design review.

- [#239](https://github.com/Felix-LeeSM/flick-drop/issues/239): one shared
  header with a larger wordmark and one header-link style, including the guide
  pages.
- [#240](https://github.com/Felix-LeeSM/flick-drop/issues/240): calmer eyebrow
  labels, larger page titles, a 48px primary button, and a disabled primary
  button that reads as disabled in both themes.
- [#241](https://github.com/Felix-LeeSM/flick-drop/issues/241): a visible
  passphrase label, lifetime chips at one size, and quieter guide links on the
  create form.
- [#242](https://github.com/Felix-LeeSM/flick-drop/issues/242): a clear button
  hierarchy, one container rule, and one remaining-time style on the
  management, requester, and submit pages.
- Verify the changed pages at phone and desktop widths in light and dark
  themes, and the deployed frontend.
- Exclude page flows, security-warning wording, API, worker, crypto, or
  contract changes, and a new palette or typeface.

### M13: age Streaming Encryption

Goal: encrypt and decrypt files up to 256 MiB in 64 KiB chunks with the
standard age format, so neither client holds a whole file in memory.

- [#253](https://github.com/Felix-LeeSM/flick-drop/issues/253): the
  [age format contract](../contracts/age-format-v1.md) with Go and browser
  interop vectors.
- Then, one issue each: API and database switch with removal of the AES-GCM
  format; raw ciphertext streamed through the API on open, keeping atomic
  one-time consumption and no presigned GET URL; browser send and open; CLI;
  request links on age recipients; the 256 MiB limit everywhere.
- Verify Go/browser interop vectors, browser create/open at phone and desktop
  widths, and an S3 round trip of a 256 MiB file.
- Exclude custom cryptography, keeping the AES-GCM format for existing links,
  presigned GET URLs, and browser-to-browser transfer.
