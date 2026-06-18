# k3d Smoke

`mise run smoke-k3d` exercises the Kubernetes manifests in a disposable local
k3d cluster.

## What It Verifies

The smoke script:

1. builds local `flick-local/*:ci` images
2. creates a disposable `flick-ci` k3d cluster
3. imports the local web, API, and worker images into the cluster
4. applies `deploy/k3d`
5. waits for NATS, API, worker, and web rollout readiness
6. port-forwards API and web services to localhost
7. checks API and web health endpoints
8. creates, reads metadata for, and opens one synthetic encrypted text secret

The synthetic secret uses fake ciphertext and a fake access proof. No
passphrase, plaintext secret, derived key, OCI credential, bucket name, or
production domain is required.

## Run

```sh
mise run smoke-k3d
```

Required local tools:

- Docker daemon
- `k3d`
- `kubectl`
- `node`

`k3d`, `kubectl`, and `node` are pinned through mise:

```sh
mise install
```

If a required tool is missing, the script prints a skip message and exits
successfully. This keeps PR CI lightweight until k3d is explicitly provisioned
for a runner.

## Ports

The script uses local ports:

```text
API: http://127.0.0.1:18080
Web: http://127.0.0.1:18081
```

Override them when needed:

```sh
FLICK_K3D_API_PORT=28080 FLICK_K3D_WEB_PORT=28081 mise run smoke-k3d
```

## Overlay

`deploy/k3d/` reuses the public base manifests and replaces placeholder images
with local smoke-test images:

```text
flick-local/flick-api:ci
flick-local/flick-worker:ci
flick-local/flick-web:ci
```

Do not use this overlay for production. It is a local smoke target only.
