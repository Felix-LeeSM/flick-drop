#!/usr/bin/env bash
set -euo pipefail

if ! command -v k3d >/dev/null 2>&1; then
  echo "k3d-smoke: k3d not installed; skipped"
  exit 0
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "k3d-smoke: kubectl not installed; skipped"
  exit 0
fi

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "k3d-smoke: docker daemon unavailable; skipped"
  exit 0
fi

if ! command -v node >/dev/null 2>&1; then
  echo "k3d-smoke: node not installed; skipped"
  exit 0
fi

cluster="${FLICK_K3D_CLUSTER:-flick-ci}"
api_port="${FLICK_K3D_API_PORT:-18080}"
web_port="${FLICK_K3D_WEB_PORT:-18081}"
# One port-forward per API pod, so a call can pin a replica.
pod_a_port="${FLICK_K3D_POD_A_PORT:-18083}"
pod_b_port="${FLICK_K3D_POD_B_PORT:-18084}"
# The k3d load balancer is the only path that follows Service endpoints through a rollout.
ingress_port="${FLICK_K3D_INGRESS_PORT:-18085}"
ingress_host="flick.localhost"
tmp_dir="$(mktemp -d)"
port_forward_pids=()

cleanup() {
  for pid in "${port_forward_pids[@]}"; do
    kill "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  done
  rm -rf "$tmp_dir"
  k3d cluster delete "$cluster" >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_http() {
  local url="$1"
  local attempts="${2:-60}"

  for _ in $(seq 1 "$attempts"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done

  echo "k3d-smoke: timed out waiting for $url" >&2
  return 1
}

json_get() {
  local path="$1"
  node -e '
const path = process.argv[1].split(".");
let raw = "";
process.stdin.on("data", (chunk) => { raw += chunk; });
process.stdin.on("end", () => {
  const parsed = JSON.parse(raw);
  let value = parsed;
  for (const segment of path) {
    value = value?.[segment];
  }
  if (value === undefined || value === null) {
    process.exit(1);
  }
  process.stdout.write(String(value));
});
' "$path"
}

json_expect() {
  local path="$1"
  local expected="$2"
  node -e '
const path = process.argv[1].split(".");
const expected = process.argv[2];
let raw = "";
process.stdin.on("data", (chunk) => { raw += chunk; });
process.stdin.on("end", () => {
  const parsed = JSON.parse(raw);
  let value = parsed;
  for (const segment of path) {
    value = value?.[segment];
  }
  if (String(value) !== expected) {
    console.error("expected " + path.join(".") + "=" + expected + ", got " + value);
    process.exit(1);
  }
});
' "$path" "$expected"
}

start_port_forward() {
  local target="$1"
  local local_port="$2"
  local remote_port="$3"
  local log_file="$tmp_dir/${target//\//-}-port-forward.log"

  kubectl -n flick port-forward --address 127.0.0.1 "$target" "$local_port:$remote_port" >"$log_file" 2>&1 &
  port_forward_pids+=("$!")
}

# Public key only (identity discarded); the API parses it but never encrypts to it here.
recipient="age1pq1pksqxgktrkvek9erw0pn0nqxqmt4h9dmzkjstduqsaz7t7tmt8gzwmc3jqnu9q2zd2ts6es2v4wfnhqffl6mgc0yhyw3v6gfj2sgkude35y458r63gfjds4xyjcs6hsm0wm5dfnxry2xl26z55u6pnyz4aaxfzhzg5qe95th9xp4s5uq4n7gd3wql97ll33y5eyvad76tdxfzcgnag6eqpuuq8v2jg54zw74v0y32gwqwpdllc4hg8wc9q48qe4j6rpdcswy0jn5hg3ehdryczg47e37yjmc6vprm6dgte8fwrpce3n85xfvlzt550hqfy6w8zcg2q6m4anrtveern64drca28u8edtfjkvszkergmt2p9yj884yxfap7qgpxyw9xfsfwyp3490u9958753tfnm5zyl83p99z5gsn2vtvt986rmm0u5ek7k3cnekpwkcmhzu36j9v9key3ausn5nx3cnsw43cj3menmpjul0d9lszjyemqmrdqgqha88sq05jsztdxzkn26czzncjfszp2j8dtse5ugtwkdzakfzjdd2vshyrpxgjy9xnwqhetzc8krtqc0x3qkztxja69ukluz8egtuw6zzc70q2ws6gpcwla39n7j5d0cdpyu8vvwmvy4mtvggp4t4sf0a2yaxq4qh0cmrn863j9kvq0kzjpva6gq00392wh94ezyrrxz2kxfxeqptfpq2enanuclr852d2zpysnpwpk99z495jd5xxdzw83y0jz4pre2pun7yfvu4vc9ktq5avwevrg454ujtl2xukldz4yy25xavr7smzp4e208qpaj326y05vn3v4mykyr2e6qtey83vknj6zxr03srm73xt2cmf0gl42fc5vqq5sxrrf55rjwt350fx25qzjzvwuu3cn2vyafhv0cgj3sm3wmun6eycltuv8htrrf0xynrssy5sxq0cg7x38tjzpvwcaectd9s9f78x8envwfz3zsf5gc08qnksu2jdncvp62sg7xrgcsk7vdkh369euekvfnxcl99hfcp53r2545kfm79unme4kmp9r3p9dzlg2ukw9ts09p6rsxvpzhrm2f04xpm2778j905jw6dtfhmz2rwjzg4jhnxcfeq9dylqywfh3kzqyqr8kf2pr45vclhcvuv22z6n64xetq54h33hgdvwwg3ev3j53cjg9prrthkqs27yc3265d7t642pz5eru8thgvhxa9rzvx8g8xpksxgff3nq2kexy30vtr3kwev38ny2vrmxrlq2q32csphvk6mdctcat86ewrsh0afuup7sj65v46gkpxrhkw0g78wl0pg2zaul262pnxkjvycpjhakzhykqgph3ng3u664cccp0qyms892ca7kdtmehgne29l88rcedym84apdsftkk9fw3v2h8aehxxat7j4ympgckj8j4qstnd23swt94cduqamw5lvz2jjwua5z4qgfwjvglzfayu68cl7pyp8n9lhnuky2asrm4uk9k7e22lefjympks02xnngkdf9ny9psg0qgdjyqqt26umquncsc568q7fudq5645dmgd3rchxyccvxlp2kxcaey9lm29xxye6xvrlpyjhz9jn8vqm7m25cxerxvy6p3hq9jrapar3q46u2cv6k8x7z9edx5x7psu49f5mc2ku9x36hzcclrp2qzglcejxjv5m0tezktfwcf8rxpugfnct4n9zdj8j3glnqtyj9wuz5c3m6txk32mzcrs73xeu8yjujq7tkxrhe48ygthh57eexwg38pj3gznkc4epqddlq0pg6xkqghq82vcctynsydwnyc0d08yw60w2tvzc8d9qshluz8dnzxuyjpzvz7qgf3c85cggatyd8q6jfjxegwf5ajcpwml26q9429vvzmsr2d3g6xp3vycwnfvmkdh4d53l6ehr"

export FLICK_IMAGE_PREFIX=flick-local
export FLICK_IMAGE_TAG=ci
export FLICK_WEB_PUBLIC_API_BASE_URL=/
scripts/ci/images.sh

k3d cluster delete "$cluster" >/dev/null 2>&1 || true
# Two nodes so pod-to-pod stream hops cross the overlay network.
k3d cluster create "$cluster" --agents 1 --wait -p "$ingress_port:80@loadbalancer"
k3d image import \
  flick-local/flick-api:ci \
  flick-local/flick-worker:ci \
  flick-local/flick-web:ci \
  -c "$cluster"

# k3s installs the Traefik CRDs via helm-install-traefik-crd after `cluster create --wait` returns.
for crd in serverstransports.traefik.io middlewares.traefik.io; do
  for _ in $(seq 1 60); do
    kubectl get crd "$crd" >/dev/null 2>&1 && break
    sleep 2
  done
  kubectl wait --for=condition=established --timeout=60s "crd/$crd"
done

kubectl apply -k deploy/k3d
kubectl -n flick rollout status deploy/postgres --timeout=180s
kubectl -n flick rollout status deploy/valkey --timeout=120s
kubectl -n flick rollout status statefulset/nats --timeout=180s
kubectl -n flick rollout status deploy/flick-api --timeout=180s
kubectl -n flick rollout status deploy/flick-worker --timeout=120s
kubectl -n flick rollout status deploy/flick-web --timeout=120s
kubectl -n flick get pods -o wide

api_pods=()
while IFS= read -r pod; do
  [ -n "$pod" ] && api_pods+=("$pod")
done < <(kubectl -n flick get pods -l app.kubernetes.io/name=flick-api \
  --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
if [ "${#api_pods[@]}" -ne 2 ]; then
  echo "k3d-smoke: expected 2 running flick-api pods, got ${#api_pods[@]}" >&2
  exit 1
fi
pod_a="${api_pods[0]}"
pod_b="${api_pods[1]}"

start_port_forward service/flick-api "$api_port" 8080
start_port_forward service/flick-web "$web_port" 8080
start_port_forward "pod/$pod_a" "$pod_a_port" 8080
start_port_forward "pod/$pod_b" "$pod_b_port" 8080
pod_a_url="http://127.0.0.1:$pod_a_port"
pod_b_url="http://127.0.0.1:$pod_b_port"

wait_http "http://127.0.0.1:$api_port/healthz"
wait_http "http://127.0.0.1:$api_port/readyz"
wait_http "http://127.0.0.1:$web_port/healthz"
wait_http "$pod_a_url/readyz"
wait_http "$pod_b_url/readyz"

# The only CI run against the real nginx image.
FLICK_WEB_BASE_URL="http://127.0.0.1:$web_port" scripts/smoke/web-headers.sh

# The API checks only the age magic line and lengths, so a fixed age-shaped body suffices.
body="$tmp_dir/body.age"
opened="$tmp_dir/opened.age"
open_headers="$tmp_dir/open-headers.txt"
printf 'age-encryption.org/v1\nk3d-body' >"$body"
access_proof="$(printf 'k3d-proof' | base64)"
create_body="$(
  cat <<JSON
{
  "kind": "text",
  "ciphertext": "$(base64 <"$body" | tr -d '\n')",
  "access": {
    "kdf": {
      "algorithm": "PBKDF2-SHA-256",
      "salt": "$(printf 'k3d-access-salt' | base64)",
      "iterations": 600000,
      "key_length_bits": 256
    },
    "proof": "$access_proof"
  },
  "wrapped_identity": "$(printf 'age-encryption.org/v1\nk3d-wrapped' | base64 | tr -d '\n')",
  "size_bytes": $(wc -c <"$body" | tr -d ' '),
  "ttl_seconds": 600,
  "max_views": 1
}
JSON
)"

create_response="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -X POST \
    -d "$create_body" \
    "$pod_a_url/api/secrets"
)"
secret_id="$(printf '%s' "$create_response" | json_get id)"

metadata_response="$(curl -fsS "$pod_b_url/api/secrets/$secret_id")"
printf '%s' "$metadata_response" | json_expect kind text

curl -fsS \
  -H "Content-Type: application/json" \
  -X POST \
  -d "{\"access_proof\":\"$access_proof\"}" \
  -D "$open_headers" \
  -o "$opened" \
  "$pod_b_url/api/secrets/$secret_id/open"
cmp "$body" "$opened"
grep -qi '^flick-kind: text' "$open_headers"
grep -qi '^flick-wrapped-identity: ' "$open_headers"

request_response="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -X POST \
    -d "{\"public_key\":\"$recipient\",\"ttl_seconds\":600}" \
    "$pod_a_url/api/requests"
)"
request_id="$(printf '%s' "$request_response" | json_get id)"
submission_token="$(printf '%s' "$request_response" | json_get submission_token)"
curl -fsS -H "Authorization: Bearer $submission_token" "$pod_b_url/api/requests/$request_id" |
  json_expect can_submit true

# Sender on pod A, receiver on pod B: ciphertext must hop over the stream listener, room state in Valkey.
init_response="$(curl -fsS -X POST "$pod_a_url/api/pipe/init")"
code="$(printf '%s' "$init_response" | json_get code)"
sender_token="$(printf '%s' "$init_response" | json_get sender_token)"
join_response="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -H "X-Flick-Pipe-Code: $code" \
    -X POST \
    -d "{\"recipient\":\"$recipient\"}" \
    "$pod_b_url/api/pipe/join"
)"
connection_id="$(printf '%s' "$join_response" | json_get connection_id)"
curl -fsS -X POST \
  -H "X-Flick-Pipe-Code: $code" \
  -H "X-Flick-Pipe-Sender: $sender_token" \
  -H "X-Flick-Pipe-Connection: $connection_id" \
  "$pod_a_url/api/pipe/select" >/dev/null

pipe_payload="$tmp_dir/pipe-payload.bin"
pipe_received="$tmp_dir/pipe-received.bin"
pipe_headers="$tmp_dir/pipe-headers.txt"
head -c 2097152 /dev/urandom >"$pipe_payload"
curl -fsS --max-time 120 \
  -H "X-Flick-Pipe-Code: $code" \
  -H "X-Flick-Pipe-Connection: $connection_id" \
  -D "$pipe_headers" \
  -o "$pipe_received" \
  "$pod_b_url/api/pipe/receive" &
receive_pid=$!
send_response="$(
  curl -fsS --max-time 120 \
    -X POST \
    -H "X-Flick-Pipe-Code: $code" \
    -H "X-Flick-Pipe-Sender: $sender_token" \
    -H "Flick-Kind: file" \
    -H "Flick-Encrypted-Filename: azNkLXNtb2tl" \
    --data-binary "@$pipe_payload" \
    "$pod_a_url/api/pipe/send"
)"
wait "$receive_pid"
printf '%s' "$send_response" | json_expect status completed
cmp "$pipe_payload" "$pipe_received"
grep -qi '^flick-kind: file' "$pipe_headers"
grep -qi '^flick-encrypted-filename: azNkLXNtb2tl' "$pipe_headers"

# A non-flick-api pod reaches the public API port but not the stream listener, Valkey, or Postgres.
pod_a_ip="$(kubectl -n flick get pod "$pod_a" -o jsonpath='{.status.podIP}')"
kubectl -n flick run netpol-probe --rm -i --restart=Never --image=busybox:1.37 --command -- \
  sh -c "nc -z -w 3 $pod_a_ip 8080 \
    && ! nc -z -w 3 $pod_a_ip 8081 \
    && ! nc -z -w 3 valkey 6379 \
    && ! nc -z -w 3 postgres 5432"

# maxSurge 0 / maxUnavailable 1, preStop, and readiness keep one ready replica the whole time.
ingress_url="http://127.0.0.1:$ingress_port"
wait_http_host() {
  for _ in $(seq 1 60); do
    if curl -fsS -H "Host: $ingress_host" "$ingress_url/api/config" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "k3d-smoke: ingress never served /api/config" >&2
  return 1
}
wait_http_host
probe_log="$tmp_dir/rollout-probe.log"
(
  while :; do
    curl -sS -o /dev/null --max-time 5 -w '%{http_code}\n' -H "Host: $ingress_host" "$ingress_url/api/config" || echo "000"
    sleep 0.2
  done
) >"$probe_log" 2>/dev/null &
probe_pid=$!
kubectl -n flick rollout restart deploy/flick-api deploy/flick-web
kubectl -n flick rollout status deploy/flick-api --timeout=240s
kubectl -n flick rollout status deploy/flick-web --timeout=240s
kill "$probe_pid"
wait "$probe_pid" 2>/dev/null || true
probes="$(wc -l <"$probe_log" | tr -d ' ')"
failed="$(grep -vc '^200$' "$probe_log" || true)"
echo "k3d-smoke: rollout probes=$probes failed=$failed"
if [ "$failed" -ne 0 ] || [ "$probes" -lt 20 ]; then
  sort "$probe_log" | uniq -c >&2
  echo "k3d-smoke: requests failed during the rolling restart" >&2
  exit 1
fi

echo "k3d-smoke: ok"
