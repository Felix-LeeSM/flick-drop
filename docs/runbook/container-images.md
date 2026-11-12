# Container Images

Flick builds three runtime images:

```text
<registry>/<namespace>/flick-api:<tag>
<registry>/<namespace>/flick-worker:<tag>
<registry>/<namespace>/flick-web:<tag>
```

Local CI uses `flick-local` as the image prefix and `ci` as the tag.

GitHub Actions publishes public Docker Hub images with
`.github/workflows/publish-images.yml`.

Published images are multi-arch manifests for `linux/amd64` and `linux/arm64`,
so the same tag runs on x86 nodes and on arm64 nodes such as OCI Ampere A1.
The Go and web build stages run on the build platform and cross-compile; only
the short runtime-stage `RUN` steps use emulation. To check both platforms
locally without pushing (the default `docker` driver cannot build several
platforms at once, so use a docker-container builder from
`docker buildx create --use` or enable the containerd image store):

```sh
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.api .
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.worker .
docker buildx build --platform linux/amd64,linux/arm64 -f web/Dockerfile web
```

## Build

Build all images:

```sh
scripts/ci/images.sh
```

Choose a registry namespace and tag without pushing:

```sh
FLICK_IMAGE_PREFIX=docker.io/<namespace> \
FLICK_IMAGE_TAG="$(git rev-parse --short HEAD)" \
scripts/ci/images.sh
```

Override the web API origin only when the deployment intentionally serves the
API from a separate public origin:

```sh
FLICK_WEB_PUBLIC_API_BASE_URL=https://api.example.com \
scripts/ci/images.sh
```

Individual build commands:

```sh
docker build -f Dockerfile.api -t flick-local/flick-api:dev .
docker build -f Dockerfile.worker -t flick-local/flick-worker:dev .
docker build -f web/Dockerfile -t flick-local/flick-web:dev web
```

## Web Build Arguments

The web image is static. Browser-visible `PUBLIC_` values are embedded at build
time; file size limits are not among them — the client fetches those at runtime
from `GET /api/config` (see `web/src/lib/api/config.ts`).

```sh
docker build \
  -f web/Dockerfile \
  --build-arg PUBLIC_FLICK_API_BASE_URL=/ \
  -t flick-local/flick-web:dev \
  web
```

Use `PUBLIC_FLICK_API_BASE_URL=/` when ingress serves web and API from the same
origin and routes `/api/*` to `flick-api`. Use a full public API origin when the
API is served separately.

The all-images script intentionally uses `FLICK_WEB_PUBLIC_API_BASE_URL` for
this override so local development `PUBLIC_` values do not leak into deployment
image builds by accident.

`web/Dockerfile` also accepts the link lifetime bounds that the browser uses
for client-side validation and the initial lifetime preset:

| Build argument | Default | Server value it mirrors |
| --- | --- | --- |
| `PUBLIC_FLICK_MIN_TTL_SECONDS` | `300` | `FLICK_MIN_TTL_SECONDS` |
| `PUBLIC_FLICK_MAX_TTL_SECONDS` | `604800` | `FLICK_MAX_TTL_SECONDS` |
| `PUBLIC_FLICK_DEFAULT_TTL_SECONDS` | `3600` | `FLICK_DEFAULT_TTL_SECONDS` |

`scripts/ci/ttl-drift.sh` keeps these defaults equal to the Go defaults in
`internal/config/defaults.go` (it also checks `.github/workflows/publish-images.yml`
and `contracts/openapi.yaml`). `scripts/ci/images.sh` does not pass them, so a
deployment that changes the server TTL bounds must pass the matching
`--build-arg` values to `docker build` itself; otherwise the browser offers or
rejects lifetimes that the API decides differently.

## Runtime Defaults

The API image defaults to:

```text
FLICK_API_ADDR=:8080
FLICK_API_DB_PATH=/data/api.db
```

The worker image defaults to:

```text
FLICK_WORKER_DB_PATH=/data/worker.db
```

Both Go images create `/data` for SQLite files. Kubernetes manifests should
mount the persistent volume there or override the database path explicitly.

The web image serves static assets with Nginx on port `8080` and exposes
`/healthz` for simple readiness checks.

The web image reads one runtime variable at container start, not at build:

```text
FLICK_CSP_CONNECT_SRC=
```

The nginx base image's envsubst entrypoint renders
`web/nginx-templates/csp.conf.template` into `/etc/nginx/snippets`, appending
`FLICK_CSP_CONNECT_SRC` to the CSP `connect-src 'self'`. The empty default is
correct while the large-object backend is disabled. A deployment with
`FLICK_STORAGE_LARGE_BACKEND=s3` sets it to the `FLICK_S3_ENDPOINT` origin (and
the R2 origin when `FLICK_R2_ENABLED=true`, space-separated) so the browser's
direct presigned uploads are not blocked. `deploy/base/web.yaml` wires it from
`ConfigMap/flick-config`.

## Boundaries

These image definitions do not push to a registry and do not contain real
domains, OCI credentials, kubeconfig, bucket names, internal tokens, database
files, or private deployment overlays.

## Publish

The publish workflow uses repository variables and secrets:

```text
DOCKERHUB_USERNAME
DOCKERHUB_NAMESPACE
DOCKERHUB_TOKEN
```

For the initial deployment, `DOCKERHUB_NAMESPACE` is `haradwaith`.

Run manually from GitHub Actions or push a version tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Manual publishes must run from the repository default branch. Tag publishes must
use a `v*` tag whose commit is already reachable from the default branch. Custom
manual tags are limited to `v<release>` or the checked-out commit's exact
`sha-<12-hex>` tag; `latest` and other mutable aliases are rejected. The Docker
Hub namespace must be one lowercase namespace component.

The workflow publishes:

```text
docker.io/haradwaith/flick-api:<tag>
docker.io/haradwaith/flick-worker:<tag>
docker.io/haradwaith/flick-web:<tag>
docker.io/haradwaith/flick-api:sha-<short-sha>
docker.io/haradwaith/flick-worker:sha-<short-sha>
docker.io/haradwaith/flick-web:sha-<short-sha>
```

Use the immutable `sha-*` tag in production overlays unless deliberately
rolling out a named release tag.
