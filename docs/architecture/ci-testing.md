# CI and Testing

Flick separates fast PR checks from slower infrastructure smoke tests.

## Layers

```text
PR checks
  shell
  repo structure
  Kubernetes manifest structure
  env contract
  contracts
  Go checks
  MinIO storage integration tests
  web checks
  container image builds
  NATS compose smoke

manual/nightly checks
  k3d deploy smoke
  OCI dev bucket smoke when credentials are configured

release checks
  Docker Hub image publish on manual dispatch or v* tag push
  flick CLI binary release on manual dispatch or cli/v* tag push
```

## Local Entry Points

```sh
mise run check
mise run manifests
mise run images
mise run smoke-nats
mise run smoke-k3d
```

The `check` task includes shell, repo-structure, Kubernetes manifest structure,
env-contract, contract, Go, MinIO storage integration, web, and local container
image checks. The MinIO and image checks skip only when Docker is not available
locally; CI treats a missing Docker daemon as a failure.

## GitHub Repository Policy

`main` is protected after initialization.

Required `main` policy:

- Pull request required before merge.
- Required approving reviews: 0 while the repository has a single maintainer.
- Required status checks:
  - `Repo checks`
  - `NATS smoke`
  - `Review gate`
- Require the branch to be up to date before merge.
- Require conversation resolution.
- Enforce rules for administrators.
- Disallow force pushes and branch deletion.

Merge strategy:

- Squash merge enabled.
- Merge commits disabled.
- Rebase merge disabled.
- Delete head branches after merge.

`k3d smoke` and OCI smoke checks are not required PR checks because they are
slower infrastructure checks and require Docker plus k3d on the runner. The k3d
smoke entrypoint uses the dedicated `deploy/k3d/` overlay so the generic public
base does not try to pull placeholder images.

`Review gate` is a metadata-only `pull_request_target` workflow. It must not
execute PR head code. It publishes the required `Review gate` commit status to
the PR head SHA. It enforces linked issue, milestone, label families, PR
template completion, PR size, sensitive-path labels, exact-head subagent review
comments, and label-after-review ordering.

## Go Tests

Expected coverage:

- `internal/config`: env parsing and validation.
- `internal/secrets`: secret lifecycle and verified open invariants.
- `internal/storage`: SQLite BLOB threshold and large-payload S3 routing behavior.
- `internal/events`: NATS payload validation and outbox publish behavior.
- `internal/worker`: idempotent job execution and retry decisions.

Integration tests should use temporary SQLite files and a real NATS instance
when testing worker delivery paths.

## Web Tests

Expected coverage:

- passphrase input and KDF parameter handling.
- Web Crypto encrypt/decrypt helpers.
- upload and verified open UI state.
- API client behavior with metadata lookup and proof-gated payload return.

Browser tests should prove:

1. text secret is created
2. share URL contains only the secret ID
3. recipient enters passphrase and first open decrypts
4. second open is blocked
5. expired secret is blocked
6. local file secret upload decrypts to a downloadable file

The focused recipient-page browser regression is `web/src/lib/components/OpenSecretPage.browser.mjs`.
It uses Playwright Chromium with mocked metadata/open responses and the shared
crypto vectors to exercise loading, retry after network/503/429 failures, both
access models, and terminal 404/410 responses. Run it locally:

```sh
pnpm --dir web exec playwright install chromium
pnpm --dir web dev --host 127.0.0.1
# In another terminal, with the dev server running:
pnpm --dir web test:browser
```

`FLICK_WEB_URL` can point the browser check at another running web instance.
This focused check is separate from the default Node unit suite; it does not
replace the complete create/open/file browser scenarios listed above.

The create-page upload regression additionally requires a live API with the
S3 backend, MinIO, and the default 1 MiB inline / 50 MiB maximum limits. Run it
against an isolated local stack (it creates and consumes dummy file secrets):

```sh
FLICK_WEB_URL=http://127.0.0.1:5173 pnpm --dir web test:storage-browser
```

The check round-trips an inline file and a 2 MiB encrypted file, compares the
downloaded bytes, observes real XHR upload events, holds the finalize request to
verify that 100 percent uploaded does not publish a link, and cancels a throttled
upload. Deterministic API-client tests cover unknown totals, transport failures,
late callbacks, and cancellation/failure around finalize.

`NativeShareButton.browser.mjs` runs through the create page with real browser
encryption and mocked API/Web Share responses. It checks both link models,
exact recipient URLs, unsupported browsers, cancellation, rejected shares,
copy/QR fallbacks, and narrow-screen keyboard/touch controls. `test:browser`
runs both component suites. These checks do not open a native OS share sheet;
verify the share sheet and selected target app on a real device separately.

`ManageSecretPage.browser.mjs` extends the mocked browser suite with both access
models, memory-only recipient handoff, separate copy/share/QR actions, reload,
invalid links, retryable errors, lockout, known expiry, and an open/cancel race.
`pnpm --dir web test:management` runs `ManageSecretPage.live.mjs` against a real
API with #200 and #201 implemented. The live suite creates synthetic Model A/B
text deliveries and verifies management followed by one-time open or cancellation.
Run the separate `test:storage-browser` suite for inline/S3 file round trips and
upload/finalize progress with the same management navigation. Mocked browser
tests do not replace the real API lifecycle checks.

## Contracts

Shared contracts live in `contracts/`.

- `openapi.yaml`: web to API.
- `contracts/events/*.schema.json`: NATS event payloads.

NATS payloads must contain IDs and small metadata only. They must not contain
ciphertext bodies, plaintext secrets, passphrases, or derived keys.

## Object Storage

PR CI must not require object storage credentials. S3 adapter behavior should be
tested with the MinIO integration test and fake clients in PR checks.

`scripts/ci/storage-integration.sh` runs inside the required `Repo checks` job
through `scripts/ci/all.sh`. It reuses the MinIO services in `compose.yaml` with
a unique Compose project and a dynamically allocated loopback port.
Because the official community registry images are unavailable, the script
builds `scripts/ci/Dockerfile.minio` from pinned official MinIO and mc source
commits with Go 1.25.11; the image override applies only to the test project.
The cold build is bounded by the `Repo checks` job's 30-minute timeout.
The script waits for readiness with a 60-second retry window, creates the test bucket, and runs
`go test -count=1 -timeout 2m -v -tags integration ./internal/storage/`.
The script overrides inherited S3 settings with the local test bucket and
credentials and removes its own containers, network, volumes, and image tag on exit.
Run the script directly to check presigned uploads, size enforcement, and
HEAD/GET/DELETE against MinIO without starting the application or NATS.

Real OCI dev bucket smoke tests (S3-compatibility mode) are manual or scheduled
and run only when the required secrets are present.

## Image Publish

`.github/workflows/publish-images.yml` publishes `flick-api`, `flick-worker`,
and `flick-web` images to Docker Hub. It is not a PR check and must not deploy
to OCI directly.

The workflow requires:

```text
DOCKERHUB_USERNAME
DOCKERHUB_NAMESPACE
DOCKERHUB_TOKEN
```

Production overlays should consume immutable `sha-*` tags or explicit `v*`
release tags, not `latest`.

The publish workflow validates the source and image tags before logging in to
Docker Hub:

- manual publishes must run from the repository default branch
- tag publishes must use a `v*` tag whose commit is already reachable from the
  repository default branch
- `DOCKERHUB_NAMESPACE` must be one lowercase Docker Hub namespace component
- manual custom tags must match `v<release>`, or `sha-<12-hex>` only when it is
  the checked-out commit SHA tag

## CLI Release

`.github/workflows/release-cli.yml` cross-compiles the `flick` command for
macOS, Linux, and Windows and attaches the archives, plus a `SHA256SUMS` file,
to a GitHub release. It needs no registry credentials; `GITHUB_TOKEN` with
`contents: write` is the only permission it uses.

It triggers on `cli/v*` tags, deliberately a different namespace from the `v*`
tags that publish images. Actions ref globs do not cross `/`, so a CLI release
never republishes `flick-api`, `flick-worker`, and `flick-web`, and a server
release never rebuilds the CLI.

The workflow applies the same trust checks as the image publish, because a
release asset is something users download and execute:

- the ref must be a `cli/v*` tag, and a manual dispatch must name a tag that
  already exists rather than a branch
- the tagged commit must already be reachable from the repository default
  branch, so binaries are only ever built from code that passed `review-gate`
- `scripts/ci/go.sh` runs before anything is built

A `cli/v*` tag is not a Go module version, so `go install ...@cli/v0.1.0` does
not resolve. Go users install with `@latest` or pin to the release commit SHA.
