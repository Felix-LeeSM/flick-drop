# Flick

Flick is a self-hosted, open-source service for sharing short-lived secrets
and files through one-time links.

**Live demo:** https://flick.dev-felix.work/

It is designed for people who want a small deployable alternative to sending
passwords, API keys, private notes, or temporary files through chat, email, or
long-lived cloud drives. A Flick secret is meant to be created, opened once,
and removed.

## What It Does

- Creates one-time links for encrypted text secrets and small encrypted files.
- Expires secrets automatically after a short TTL.
- Deletes consumed or expired data through an async worker.
- Removes a secret after five invalid passphrase attempts.
- Stores only ciphertext and metadata on the server side.
- Derives encryption keys in the browser from a user-entered passphrase.
- Keeps passphrases and derived keys outside HTTP requests and server logs.
- Supports local SQLite storage for small encrypted payloads.
- Plans S3-compatible object storage support for larger encrypted files.

Example links:

```text
https://drop.example.com/s/abc123
https://drop.example.com/s/xyz789
```

The link contains only a secret ID. The recipient must enter the passphrase in
the browser to decrypt the payload.

## Security Model

Flick is built around one rule:

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

The browser derives a key from the user-entered passphrase and encrypts the text
or file before upload. The API stores ciphertext, nonce, KDF salt/parameters,
size, content type, expiration metadata, storage location, and a hash of a
separate access proof. The passphrase and derived key never leave the browser.
Ciphertext is returned only after the API verifies the access proof and marks
the secret consumed in the same operation.

This does not make Flick a password manager or long-term vault. It is an
ephemeral delivery service: short-lived, one-time, and intentionally limited.

## Deployment Target

Flick is designed to be deployable by anyone with a small Kubernetes cluster
or OCI Always Free resources.

The intended production shape is:

- `flick-web`: SvelteKit frontend.
- `flick-api`: Go HTTP API.
- `flick-worker`: Go worker for cleanup and async jobs.
- `nats`: NATS JetStream broker.
- SQLite files on persistent volume for metadata, small ciphertext payloads,
  and worker state.
- S3-compatible object storage bucket (MinIO dev, OCI S3-compatibility prod)
  for larger encrypted files.

The service avoids a managed database requirement. That keeps the baseline small
enough for an OCI Free Tier-style deployment, using compute, block volume/PVC,
and Object Storage. OCI quotas and Always Free policies can change, so deployers
should verify current limits in their own tenancy before production use.

## Command-Line Client

`flick` creates and opens secrets from a terminal. It encrypts locally, exactly
as the browser does — the passphrase, the derived key, and the plaintext never
leave the machine it runs on.

### Install

Prebuilt binaries for macOS, Linux, and Windows are attached to every `cli/v*`
release, next to a `SHA256SUMS` file. A downloaded binary is something you are
about to run, so verify it before you do.

On macOS and Linux — set `version` to the latest
[release](https://github.com/Felix-LeeSM/flick-drop/releases):

```bash
version=v0.2.0
platform=darwin_arm64   # or darwin_amd64, linux_amd64, linux_arm64
base=https://github.com/Felix-LeeSM/flick-drop/releases/download/cli/$version

curl -fsSLO "$base/flick_${version}_${platform}.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
shasum -a 256 --ignore-missing -c SHA256SUMS   # sha256sum -c on Linux

tar -xzf "flick_${version}_${platform}.tar.gz"
sudo install -m 0755 flick /usr/local/bin/flick
flick version
```

On Windows, download `flick_<version>_windows_amd64.tar.gz` and `SHA256SUMS`
from the same release, check the archive with
`Get-FileHash flick_<version>_windows_amd64.tar.gz -Algorithm SHA256` against
its line in `SHA256SUMS`, then extract `flick.exe` somewhere on `PATH`.

Or build it from source with Go 1.25 or newer:

```bash
go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
```

A `cli/v*` tag is not a Go module version, so `go install` cannot be pinned to
one by name; pin it to the release commit instead, which each release names.

### Use

```bash
# Point the client at a deployment (or pass -url per command). The public
# instance below is the demo; a self-hosted deployment names its own origin.
export FLICK_URL=https://flick.dev-felix.work

# The key travels in the link fragment; anyone holding the link can open it once.
flick send "sk-live-example"
kubectl get secret db -o jsonpath='{.data.password}' | base64 -d | flick send

# Or require a passphrase, which is prompted for and sent through another channel.
flick send -passphrase -ttl 24h "the database password"
flick send -file ./credentials.json -ttl 30m

# Or say nothing and be asked for the secret, its lifetime, and whether it
# needs a passphrase, one at a time. Flags already given are not asked about.
flick send

# Opening consumes the secret. Text goes to stdout, files are saved by their
# decrypted name; a passphrase is prompted for only when the secret needs one.
flick open 'https://flick.dev-felix.work/s/abc123#key=...'
flick open https://flick.dev-felix.work/s/abc123 > recovered.txt
```

Flags may be written before or after the text or the link, in any order:
`flick send "db password" -ttl 24h` and `flick send -ttl 24h "db password"` are
the same command.

`FLICK_PASSPHRASE` replaces the prompt for scripted use. A passphrase is never
accepted as a flag, because command arguments are visible to every process on
the machine through `ps` and land in shell history.

## Repository Boundary

This repository is intended to be public.

It may contain:

- source code
- Dockerfiles
- generic Kubernetes manifests
- local development compose files
- example environment files
- documentation and runbooks

It must not contain:

- OCI credentials
- kubeconfig
- private keys
- admin tokens
- production domains, except the public demo instance this README names on
  purpose so the client has a working example to point at
- real bucket names
- SQLite databases
- PVC dumps
- backup archives

Production-specific configuration should live in a private ops repository or a
local private overlay.

## License

Flick is licensed under the [Apache License 2.0](LICENSE).

## Development Environment

Tool versions are pinned with `mise`.

```sh
mise install
mise trust
direnv allow
```

Local environment values live in `.env.local`, loaded by `.envrc`. Do not commit
real credentials. Use `.env.example` as the public contract.

Start the full local development stack:

```sh
mise run dev
```

## Checks

Run the local CI entrypoint:

```sh
mise run check
```

Run the NATS smoke test:

```sh
mise run smoke-nats
```

Build the local container images:

```sh
mise run images
```

Validate Kubernetes manifests:

```sh
mise run manifests
```

Run the optional local k3d deployment smoke:

```sh
mise run smoke-k3d
```

The scripts are scaffold-aware: Go and SvelteKit checks skip until their
respective code is initialized.

## Architecture Docs

- [Service topology](docs/architecture/service-topology.md)
- [Security model](docs/architecture/security-model.md)
- [Storage model](docs/architecture/storage-model.md)
- [Database schema](docs/architecture/database-schema.md)
- [Event contract](docs/architecture/event-contract.md)
- [Deployment target](docs/architecture/deployment-target.md)
- [Implementation choices](docs/architecture/implementation-choices.md)
- [Environment contract](docs/architecture/env-contract.md)
- [CI and testing](docs/architecture/ci-testing.md)
- [Agent workflow](docs/architecture/agent-workflow.md)
- [Container images](docs/runbook/container-images.md)
- [OCI Free Tier deployment](docs/runbook/oci-free-tier.md)
- [k3s base manifests](docs/runbook/k3s-base.md)
- [k3d smoke](docs/runbook/k3d-smoke.md)
- [Local development](docs/runbook/local-dev.md)
- [Roadmap](docs/ROADMAP.md)
