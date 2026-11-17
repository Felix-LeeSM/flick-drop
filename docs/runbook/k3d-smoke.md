# k3d Smoke

`mise run smoke-k3d` exercises the Kubernetes manifests in a disposable local
k3d cluster.

## What It Verifies

The smoke script (`scripts/ci/k3d-smoke.sh`):

1. builds local `flick-local/*:ci` images
2. creates a disposable two-node `flick-ci` k3d cluster (one server, one
   agent) with the k3d load balancer forwarding the ingress port
3. imports the local web, API, and worker images into the cluster
4. applies `deploy/k3d`
5. waits for postgres, valkey, NATS, API (2 replicas), worker, and web
   (2 replicas) rollout readiness, then prints each pod's node
6. port-forwards the API and web services, plus each of the two API pods on
   its own port, to localhost
7. waits for API `/healthz` and `/readyz`, web `/healthz`, and `/readyz` on
   both API pods
8. runs `scripts/smoke/web-headers.sh` against the port-forwarded web service,
   which requires the CSP (with `frame-ancestors 'none'` and a non-wildcard
   `connect-src 'self'`), `X-Frame-Options: DENY`,
   `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and
   `Permissions-Policy` headers on the HTML response
9. creates one synthetic encrypted text secret on API pod A, then reads its
   metadata and opens it on API pod B, checking that the opened body matches
   and the `Flick-Kind` and `Flick-Wrapped-Identity` response headers are
   present (both replicas share PostgreSQL)
10. creates a request link on pod A and reads it with its submission token on
    pod B
11. runs a live transfer with the sender on pod A and the receiver on pod B:
    `init`, `join`, `select`, then a 2 MiB `send` and the matching `receive`.
    The bytes must hop pod to pod over the stream listener
    (`FLICK_INTERNAL_HTTP_PORT`) and arrive identical, with the `Flick-Kind`
    and `Flick-Encrypted-Filename` headers
12. starts a throwaway `busybox` pod that must reach API port `8080` but not
    the stream listener (`8081`), Valkey, or PostgreSQL, proving the
    NetworkPolicies in `deploy/base/api.yaml`, `deploy/base/valkey.yaml`, and
    `deploy/base/postgres.yaml` are enforced
13. restarts `flick-api` and `flick-web` with `kubectl rollout restart` while
    polling `/api/config` through the ingress every 200 ms, and fails if any
    poll is not `200` (`maxSurge: 0`, `maxUnavailable: 1`, the `preStop`
    sleep, and readiness keep one replica serving)

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
API service:     http://127.0.0.1:18080   FLICK_K3D_API_PORT
Web service:     http://127.0.0.1:18081   FLICK_K3D_WEB_PORT
API pod A:       http://127.0.0.1:18083   FLICK_K3D_POD_A_PORT
API pod B:       http://127.0.0.1:18084   FLICK_K3D_POD_B_PORT
Ingress (k3d LB): http://127.0.0.1:18085  FLICK_K3D_INGRESS_PORT (Host: flick.localhost)
```

Override them when needed:

```sh
FLICK_K3D_API_PORT=28080 FLICK_K3D_WEB_PORT=28081 mise run smoke-k3d
```

## Overlay

`deploy/k3d/` reuses the public base manifests, replaces the placeholder
secrets (internal token, Valkey key salt, database passwords) with disposable
smoke-only values, and replaces placeholder images with local smoke-test
images:

```text
flick-local/flick-api:ci
flick-local/flick-worker:ci
flick-local/flick-web:ci
```

Do not use this overlay for production. It is a local smoke target only.
