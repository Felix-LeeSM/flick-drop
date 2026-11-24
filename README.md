# Flick

**English** | [한국어](README.ko.md) | [日本語](README.ja.md)

Flick is a self-hosted, open-source service for sharing short-lived secrets and files through one-time links. Content is encrypted in the browser or CLI before upload, opened once, and removed from the server.

**Live demo:** https://flick.makana.cc/ · **User & developer guide:** https://flick.makana.cc/guides/

## Features

- **One-time encrypted links:** Share notes, files (multiple files or folders are zipped into one bundle), or structured templates (Login, Card, Identity, Custom) with configurable expiry (5 minutes to 7 days).
- **Optional passphrase:** Passphrase-free links carry the decryption key in the URL fragment (`#key=...`), which browsers never send to the server. Passphrase-protected links require both the link and passphrase, and are deleted from the server after 5 wrong attempts.
- **Live transfer:** Send text or files directly between two open devices using a 6-digit code and a 3-digit verification number. Ciphertext streams through API memory and is never written to the database or object storage.
- **Request a secret:** Generate a submission link to share with someone else and a private retrieval link to open their first submission once. See the [request-link contract](docs/architecture/request-links.md) for full semantics.
- **Account-free management link:** Check whether a delivery is still unopened or cancel it before it is read. The management link expires with the delivery and cannot decrypt content or recover a lost recipient link; Flick keeps no delivery history, so save the links you need.
- **Flexible storage & async cleanup:** Small encrypted payloads live inline in PostgreSQL; larger files go to S3-compatible storage (multiple named backends such as OCI and Cloudflare R2 can run in parallel with per-backend byte ceilings). Consumed and expired items are purged by an async worker.
- **Localized web UI:** Available in English, Korean, and Japanese.

## Security Model

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

- **Local encryption:** The browser and CLI encrypt content with [age](https://age-encryption.org/) using a fresh post-quantum hybrid key (`ML-KEM-768` + `X25519`) per delivery. Large files are encrypted in 64 KiB chunks, and nothing is shown until the final chunk verifies.
- **Key handling:** Without a passphrase, the key travels only in the URL fragment (`#key=AGE-SECRET-KEY-PQ-1…`), staying out of HTTP requests and access logs. With a passphrase, the key is wrapped using age's `scrypt` mode (work factor 18) and a separate access proof (`PBKDF2-SHA-256`, 600,000 iterations) is derived; the server stores only a hash of that proof. The built-in generator produces 80-bit passphrases.
- **Live transfer:** The receiving browser creates an ephemeral key pair and sends only the public key. The sender encrypts directly to it while the server relays ciphertext in memory.
- **Honest limit of web encryption:** Browser users must trust the JavaScript served by the instance. This design protects against database, log, and backup leaks, not against a malicious operator. Verifying and using the command-line client removes that trust in served code.

Flick is an ephemeral handoff tool, not a password manager or long-term vault. See [security-model.md](docs/architecture/security-model.md) and [age-format-v1.md](contracts/age-format-v1.md) for the full threat model and wire format.

## Command-Line Client

`flick` sends and opens deliveries from a terminal using the same local encryption as the browser.

### Install

Prebuilt binaries for macOS, Linux, and Windows are attached to each `cli/v*` [release](https://github.com/Felix-LeeSM/flick-drop/releases) alongside a `SHA256SUMS` file. Verify the archive before running it.

**macOS and Linux:**

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

**Windows:** Download `flick_<version>_windows_amd64.tar.gz` and `SHA256SUMS`, verify with `Get-FileHash flick_<version>_windows_amd64.tar.gz -Algorithm SHA256`, and place `flick.exe` on `PATH`.

**From source (Go 1.25+):**

```bash
go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
```

> `cli/v*` tags are not Go module versions; pin to the release commit listed on the release page if needed.

### Use

```bash
# Point at a Flick instance (demo shown below; replace with your own origin when self-hosting)
export FLICK_URL=https://flick.makana.cc

# Passphrase-free link (key travels in #key=...)
flick send "sk-live-example"
kubectl get secret db -o jsonpath='{.data.password}' | base64 -d | flick send

# Passphrase-protected or file delivery
flick send -passphrase -ttl 24h "the database password"
flick send -file ./credentials.json -ttl 30m

# Structured credential (the web's login/card/identity/custom forms), as JSON
flick send -credential login.json

# Interactive prompt
flick send

# Open and consume: the payload lands in a 0600 file and only its path is printed
flick open 'https://flick.makana.cc/s/abc123#key=...'
# To print the payload itself (byte-exact text, label: value credential lines):
flick open -reveal https://flick.makana.cc/s/abc123 > recovered.txt

# Request a secret from someone once: prints a submission link to share and a
# private retrieval link to keep (the retrieval link holds the only key)
flick request -ttl 24h -note "the staging password, please"
flick submit 'https://flick.makana.cc/r/abc#submit=...&fp=...' "here it is"
flick receive - < retrieval-link.txt

# Live transfer to a nearby device: nothing is stored, the receiver confirms
# with a 3-digit number read out loud (agents: -confirm auto or -confirm <nnn>)
flick live "wifi password"
flick join 305817
```

Flags can appear before or after positional arguments. A passphrase is never accepted as a flag, because command arguments show up in `ps` and shell history; for scripted use, set `FLICK_PASSPHRASE` in place of the prompt.

Opened values never go to stdout unless `-reveal` is passed, so decrypted secrets stay out of terminal scrollback and agent or CI logs; `flick open` reports the saved file path instead, and `flick open -json` emits structure without secret-field values unless paired with `-reveal`.

## Self-Hosting

Flick is designed to run on a small Kubernetes cluster or OCI Always Free resources without requiring a managed database. OCI quotas and Always Free policies change, so check the current limits in your own tenancy.

- **Core components:** `flick-web` (nginx serving the built SvelteKit app), `flick-api` (Go HTTP API), `flick-worker` (Go cleanup worker), `nats` (NATS JetStream), and PostgreSQL 17 (`flick_api` and `flick_worker` databases).
- **Optional components:** Valkey (required for live transfer) and S3-compatible object storage (without it, file uploads are limited to the 1 MiB inline database limit).
- **Container images:** Published as `docker.io/haradwaith/flick-api`, `flick-worker`, and `flick-web`, tagged by version or commit (no `latest` tag).
- **Manifests & configuration:** Kubernetes manifests live in [`deploy/base`](deploy/base). Follow the [k3s runbook](docs/runbook/k3s-base.md) or [OCI Free Tier runbook](docs/runbook/oci-free-tier.md), and see [`.env.example`](.env.example) and [env-contract.md](docs/architecture/env-contract.md) for all settings.

## Repository Boundary

This repository is public. It holds source code, Dockerfiles, generic Kubernetes manifests, example environment files, and documentation. Credentials, kubeconfig, private keys, admin tokens, production domains (other than the demo instance above), real bucket names, and database, PVC, or backup dumps belong in a private ops repository or a local overlay.

## Local Development

Tool versions are pinned with `mise`:

```sh
mise install
mise trust
direnv allow
```

Put local settings in `.env.local` (loaded by `.envrc`; never commit real credentials) based on `.env.example`, then start the stack:

```sh
mise run dev
```

Common checks:

```sh
mise run check       # Local CI entrypoint
mise run smoke-nats  # NATS smoke test
mise run images      # Build local container images
mise run manifests   # Validate Kubernetes manifests
mise run smoke-k3d   # Optional local k3d deployment smoke test
```

> Any check whose local tool (such as Docker, k3d, kubectl, or Node) is missing prints `skipped` instead of failing—check the output before treating a run as a full pass.

## Documentation

**Architecture**
- [Service topology](docs/architecture/service-topology.md) · [Security model](docs/architecture/security-model.md) · [Storage model](docs/architecture/storage-model.md) · [Database schema](docs/architecture/database-schema.md)
- [Event contract](docs/architecture/event-contract.md) · [Request-link contract](docs/architecture/request-links.md) · [Environment contract](docs/architecture/env-contract.md)
- [Deployment target](docs/architecture/deployment-target.md) · [Implementation choices](docs/architecture/implementation-choices.md) · [CI and testing](docs/architecture/ci-testing.md) · [Agent workflow](docs/architecture/agent-workflow.md)

**Runbooks & Roadmap**
- [Local development](docs/runbook/local-dev.md) · [Container images](docs/runbook/container-images.md) · [k3s base manifests](docs/runbook/k3s-base.md) · [k3d smoke](docs/runbook/k3d-smoke.md) · [OCI Free Tier deployment](docs/runbook/oci-free-tier.md) · [Roadmap](docs/ROADMAP.md)

## License

Flick is licensed under the [Apache License 2.0](LICENSE).
