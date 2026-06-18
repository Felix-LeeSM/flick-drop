# OCI k3s GitHub Deploy Handoff

This handoff covers deployment work that cannot be completed from the local
repository because it requires real OCI instances, DNS, SSH, firewall rules, and
a private production overlay.

Target public domain placeholder:

```text
<production-domain>
```

The real domain is a private ops value. Keep it out of the public repository and
public GitHub issue/PR text.

Public image namespace:

```text
docker.io/haradwaith
```

## Current Public Repo State

Public repo work available after issue #63:

- generic k3s manifests in `deploy/base/`
- local k3d smoke overlay in `deploy/k3d/`
- Docker Hub image publish workflow in `.github/workflows/publish-images.yml`
- Docker Hub repository variables/secrets expected by the workflow:
  - `DOCKERHUB_USERNAME`
  - `DOCKERHUB_NAMESPACE`
  - `DOCKERHUB_TOKEN`

The public repo must not contain real OCI credentials, kubeconfig, SSH private
keys, internal tokens, production domains, private overlay values, database
files, PVC dumps, or backup archives.

## Target Topology

Use two OCI instances as one k3s cluster:

```text
flick-core
  role: k3s server
  runs: flick-api, nats, Traefik ingress
  owns: flick-api-data PVC, nats-data PVC
  public: 80/tcp, 443/tcp, operator SSH

flick-edge
  role: k3s agent
  runs: flick-worker, flick-web
  owns: flick-worker-data PVC
  public: operator SSH only
```

This avoids running separate service islands. Kubernetes service discovery keeps
`flick-worker` talking to `flick-api` through `http://flick-api:8080`, and the
public ingress terminates at the private ops `FLICK_DOMAIN` value.

## OCI Prerequisites

Create two instances in the same VCN. Prefer private node-to-node traffic for
k3s control-plane and pod networking.

DNS:

```text
<production-domain> A <flick-core-public-ip>
```

Do not point the domain at both instances with round-robin A records for the
initial deployment. The first deployment has one ingress entry point on
`flick-core`.

Minimum network rules:

```text
public internet -> flick-core:80/tcp
public internet -> flick-core:443/tcp
operator IP     -> flick-core:22/tcp
operator IP     -> flick-edge:22/tcp
flick-edge      -> flick-core:6443/tcp
node-to-node    -> all nodes:8472/udp
node-to-node    -> all nodes:10250/tcp
```

If the OCI network policy is hard to reason about during bootstrap, start with a
temporary NSG rule that allows all traffic inside the k3s node NSG, verify the
cluster, then narrow the rules.

## SSH Key Plan

Use a dedicated deployment key pair for operator access:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/flick-oci -C "flick-oci"
chmod 600 ~/.ssh/flick-oci
```

Register `~/.ssh/flick-oci.pub` on both OCI instances. Keep
`~/.ssh/flick-oci` local. Do not commit it and do not add it to GitHub until a
separate GitHub SSH deployment workflow is explicitly designed.

Local SSH config:

```sshconfig
Host flick-core
  HostName <flick-core-public-ip>
  User opc
  IdentityFile ~/.ssh/flick-oci

Host flick-edge
  HostName <flick-edge-public-ip>
  User opc
  IdentityFile ~/.ssh/flick-oci
```

## Private Ops Repo Shape

Create a private repository or local private directory such as:

```text
flick-drop-ops/
  scripts/
    00-preflight.sh
    10-install-server.sh
    20-install-agent.sh
    30-label-nodes.sh
    40-install-cert-manager.sh
    50-apply-production.sh
  overlays/
    production/
      kustomization.yaml
      patch-configmap.yaml
      patch-secret.yaml
      patch-ingress.yaml
      patch-placement.yaml
```

The ops repo may reference the public repo as a sibling checkout, Git submodule,
or CI checkout. Do not copy private overlay files into `flick-drop`.

## Script Contract

All scripts should start with:

```sh
#!/usr/bin/env bash
set -euo pipefail
```

Scripts should print the command target and refuse to continue when required
environment variables are missing.

Use these private env vars in the ops repo, not in the public repo:

```text
FLICK_DOMAIN=<production-domain>
FLICK_IMAGE_NAMESPACE=haradwaith
FLICK_IMAGE_TAG=sha-<short-sha>
FLICK_INTERNAL_TOKEN=<strong-random-token>
FLICK_CORE_PRIVATE_IP=<flick-core-private-ip>
FLICK_K3S_TOKEN=<server-node-token>
```

Keep `FLICK_STORAGE_LARGE_BACKEND=disabled` for the first deployment. OCI
Object Storage should be enabled only after the adapter and real bucket smoke
exist.

## 00-preflight.sh

Run locally before touching instances:

```sh
#!/usr/bin/env bash
set -euo pipefail

required=(FLICK_DOMAIN FLICK_IMAGE_NAMESPACE FLICK_IMAGE_TAG)
for name in "${required[@]}"; do
  if [ -z "${!name:-}" ]; then
    echo "missing $name" >&2
    exit 1
  fi
done

dig +short "$FLICK_DOMAIN"
ssh flick-core 'uname -a'
ssh flick-edge 'uname -a'
```

Expected:

- DNS resolves to `flick-core` public IP.
- SSH works to both nodes.
- Docker Hub images exist for the selected tag.

## 10-install-server.sh

Run on `flick-core` through SSH:

```sh
#!/usr/bin/env bash
set -euo pipefail

curl -sfL https://get.k3s.io | sh -s - server \
  --node-name flick-core \
  --write-kubeconfig-mode 600

sudo systemctl status k3s --no-pager
sudo cat /var/lib/rancher/k3s/server/node-token
```

Capture the node token into the private ops environment as `FLICK_K3S_TOKEN`.
Do not paste it into the public repo or GitHub comments.

Copy kubeconfig for local operator use only if needed:

```sh
ssh flick-core 'sudo cat /etc/rancher/k3s/k3s.yaml' > ./kubeconfig-flick
```

Then replace `127.0.0.1` in the private copy with the `flick-core` public IP or
a private tunnel target. Do not commit this file.

## 20-install-agent.sh

Run on `flick-edge` through SSH:

```sh
#!/usr/bin/env bash
set -euo pipefail

required=(FLICK_CORE_PRIVATE_IP FLICK_K3S_TOKEN)
for name in "${required[@]}"; do
  if [ -z "${!name:-}" ]; then
    echo "missing $name" >&2
    exit 1
  fi
done

curl -sfL https://get.k3s.io | \
  K3S_URL="https://$FLICK_CORE_PRIVATE_IP:6443" \
  K3S_TOKEN="$FLICK_K3S_TOKEN" \
  sh -s - agent --node-name flick-edge

sudo systemctl status k3s-agent --no-pager
```

Verify from `flick-core`:

```sh
sudo kubectl get nodes -o wide
```

## 30-label-nodes.sh

Run from a machine with kubeconfig access:

```sh
#!/usr/bin/env bash
set -euo pipefail

kubectl label node flick-core flick.dev/pool=core --overwrite
kubectl label node flick-edge flick.dev/pool=edge --overwrite
kubectl get nodes --show-labels
```

The private overlay should use:

```yaml
# flick-api and nats
nodeSelector:
  flick.dev/pool: core
```

```yaml
# flick-worker and flick-web
nodeSelector:
  flick.dev/pool: edge
```

Set these before the first PVC-backed deployment. With k3s local-path storage,
PVCs are tied to node-local disks and are not freely movable later.

## 40-install-cert-manager.sh

Install cert-manager only after DNS points at `flick-core` and ports `80` and
`443` are reachable.

Expected private script shape:

```sh
#!/usr/bin/env bash
set -euo pipefail

kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/<version>/cert-manager.yaml
kubectl -n cert-manager rollout status deploy/cert-manager --timeout=180s
kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
kubectl -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=180s
kubectl apply -f clusterissuer-letsencrypt-prod.yaml
```

Keep `clusterissuer-letsencrypt-prod.yaml` private because it contains the
operator email address.

## 50-apply-production.sh

Run from the private ops repo:

```sh
#!/usr/bin/env bash
set -euo pipefail

required=(FLICK_DOMAIN FLICK_IMAGE_NAMESPACE FLICK_IMAGE_TAG FLICK_INTERNAL_TOKEN)
for name in "${required[@]}"; do
  if [ -z "${!name:-}" ]; then
    echo "missing $name" >&2
    exit 1
  fi
done

rendered_manifest="$(mktemp)"
trap 'rm -f "$rendered_manifest"' EXIT
chmod 600 "$rendered_manifest"

kustomize build overlays/production >"$rendered_manifest"
kubectl apply --dry-run=client --validate=false -f "$rendered_manifest"

if grep -E 'replace-me-before-use|docker.io/example|flick.localhost' "$rendered_manifest"; then
  echo "rendered manifest still contains public placeholders" >&2
  exit 1
fi

kubectl apply -k overlays/production
kubectl -n flick rollout status statefulset/nats --timeout=180s
kubectl -n flick rollout status deploy/flick-api --timeout=180s
kubectl -n flick rollout status deploy/flick-worker --timeout=180s
kubectl -n flick rollout status deploy/flick-web --timeout=180s
```

Post-apply smoke:

```sh
curl -fsS "https://$FLICK_DOMAIN/api/healthz"
curl -fsS "https://$FLICK_DOMAIN/api/readyz"
curl -fsS "https://$FLICK_DOMAIN/healthz"
```

Then test through the browser:

- create one text secret
- open it once with the passphrase
- verify second open is blocked
- verify five wrong passphrase attempts consume the secret

## Production Overlay Requirements

The private overlay must patch:

```text
images:
  docker.io/example/flick-api    -> docker.io/haradwaith/flick-api:<tag>
  docker.io/example/flick-worker -> docker.io/haradwaith/flick-worker:<tag>
  docker.io/example/flick-web    -> docker.io/haradwaith/flick-web:<tag>

ConfigMap/flick-config:
  FLICK_PUBLIC_BASE_URL=https://<production-domain>
  FLICK_API_BASE_URL=https://<production-domain>
  FLICK_INTERNAL_API_BASE_URL=http://flick-api:8080
  FLICK_STORAGE_LARGE_BACKEND=disabled

Secret/flick-secrets:
  FLICK_INTERNAL_TOKEN=<strong-random-token>

Ingress/flick:
  host=<production-domain>
  tls.secretName=flick-tls
  cert-manager.io/cluster-issuer=letsencrypt-prod

Placement:
  flick-api -> flick.dev/pool=core
  nats -> flick.dev/pool=core
  flick-worker -> flick.dev/pool=edge
  flick-web -> flick.dev/pool=edge
```

## GitHub Deployment Later

Do not add GitHub SSH deployment until the manual run above succeeds.

When ready, add deployment secrets:

```text
OCI_SSH_HOST=<flick-core-public-ip-or-admin-domain>
OCI_SSH_USER=opc
OCI_SSH_PRIVATE_KEY=<deploy-only-private-key>
```

The GitHub deployment workflow should SSH only to `flick-core`, pull or checkout
the private ops repo, render the private overlay on the server, run the same
placeholder checks, apply manifests, and wait for rollout.

Do not put kubeconfig in GitHub secrets for the first deployment. Keeping
kubeconfig on `flick-core` avoids exposing the Kubernetes API server publicly.

## Rollback

Rollback should use image tags first:

```sh
kubectl -n flick rollout undo deploy/flick-api
kubectl -n flick rollout undo deploy/flick-worker
kubectl -n flick rollout undo deploy/flick-web
```

Do not delete PVCs, buckets, or SQLite files during normal rollback.
