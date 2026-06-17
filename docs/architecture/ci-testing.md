# CI and Testing

BurnLink separates fast PR checks from slower infrastructure smoke tests.

## Layers

```text
PR checks
  shell
  repo structure
  env contract
  contracts
  Go checks
  web checks
  NATS compose smoke

manual/nightly checks
  k3d deploy smoke
  OCI dev bucket smoke when credentials are configured
```

## Local Entry Points

```sh
mise run check
mise run smoke-nats
mise run smoke-k3d
```

The scripts are scaffold-aware. Missing Go or web components produce an explicit
skip message until those parts are initialized.

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
scheduled or manually triggered infrastructure checks.

`Review gate` is a metadata-only `pull_request_target` workflow. It must not
execute PR head code. It publishes the required `Review gate` commit status to
the PR head SHA. It enforces linked issue, milestone, label families, PR
template completion, PR size, sensitive-path labels, exact-head subagent review
comments, and label-after-review ordering.

## Go Tests

Expected coverage:

- `internal/config`: env parsing and validation.
- `internal/secrets`: secret lifecycle and verified open invariants.
- `internal/storage`: SQLite BLOB threshold and OCI routing behavior.
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

## Contracts

Shared contracts live in `contracts/`.

- `openapi.yaml`: web to API.
- `contracts/events/*.schema.json`: NATS event payloads.

NATS payloads must contain IDs and small metadata only. They must not contain
ciphertext bodies, plaintext secrets, passphrases, or derived keys.

## OCI

PR CI must not require OCI credentials. OCI adapter behavior should be tested
with fake clients in PR checks.

Real OCI dev bucket smoke tests are manual or scheduled and run only when the
required secrets are present.
