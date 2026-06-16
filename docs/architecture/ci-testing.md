# CI and Testing

BurnLink separates fast PR checks from slower infrastructure smoke tests.

## Layers

```text
PR checks
  shell/static structure
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

## Go Tests

Expected coverage:

- `internal/config`: env parsing and validation.
- `internal/secrets`: secret lifecycle and consume invariants.
- `internal/storage`: SQLite BLOB threshold and OCI routing behavior.
- `internal/events`: NATS payload validation and outbox publish behavior.
- `internal/worker`: idempotent job execution and retry decisions.

Integration tests should use temporary SQLite files and a real NATS instance
when testing worker delivery paths.

## Web Tests

Expected coverage:

- passphrase input and KDF parameter handling.
- Web Crypto encrypt/decrypt helpers.
- upload and consume UI state.
- API client behavior with ciphertext-only payloads.

Browser tests should prove:

1. text secret is created
2. share URL contains only the secret ID
3. recipient enters passphrase and first open decrypts
4. second open is blocked
5. expired secret is blocked

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
