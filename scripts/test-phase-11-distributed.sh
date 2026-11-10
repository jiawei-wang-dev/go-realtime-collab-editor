#!/bin/bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SUFFIX=$(openssl rand -hex 6)
TEMP_DIR=$(mktemp -d "/tmp/collab-phase111-${SUFFIX}.XXXXXX")
REPORT="/tmp/collab-phase111-${SUFFIX}.log"
NETWORK="collab-phase111-network-${SUFFIX}"
MYSQL_CONTAINER="collab-phase111-mysql-${SUFFIX}"
REDIS_CONTAINER="collab-phase111-redis-${SUFFIX}"
ETCD_CONTAINER="collab-phase111-etcd-${SUFFIX}"
PIDS=()
CONTAINERS=()

log() { printf '%s\n' "$*" | tee -a "$REPORT"; }
random_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}
stop_pid() {
  local pid=$1
  if [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in {1..50}; do kill -0 "$pid" 2>/dev/null || return 0; sleep 0.1; done
    kill -9 "$pid" 2>/dev/null || true
  fi
}
cleanup() {
  local status=$?
  set +e
  if [[ $status -ne 0 ]]; then
    for log_file in "$TEMP_DIR"/*.log; do
      [[ -f "$log_file" ]] || continue
      printf '\n===== %s (tail) =====\n' "$(basename "$log_file")" >> "$REPORT"
      tail -n 120 "$log_file" >> "$REPORT"
    done
  fi
  if [[ -f "$TEMP_DIR/restart-pids" ]]; then
    while IFS= read -r entry; do stop_pid "$entry"; done < "$TEMP_DIR/restart-pids"
  fi
  for pid in "${PIDS[@]:-}"; do stop_pid "$pid"; done
  for container in "${CONTAINERS[@]:-}"; do
    if [[ $(docker inspect -f '{{ index .Config.Labels "com.collab-editor.test-run" }}' "$container" 2>/dev/null) == "$SUFFIX" ]]; then
      docker rm -f -v "$container" >/dev/null 2>&1 || true
    fi
  done
  if [[ $(docker network inspect -f '{{ index .Labels "com.collab-editor.test-run" }}' "$NETWORK" 2>/dev/null) == "$SUFFIX" ]]; then
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
  fi
  chmod -R u+rwX "$TEMP_DIR" 2>/dev/null || true
  rm -rf "$TEMP_DIR"
  if [[ $status -eq 0 ]]; then log 'CLEANUP: PASS'; else log "CLEANUP: PASS (test exit $status)"; fi
  exit "$status"
}
trap cleanup EXIT INT TERM

wait_for() {
  local description=$1
  shift
  for _ in {1..120}; do
    if "$@" >/dev/null 2>&1; then log "$description: READY"; return 0; fi
    sleep 0.5
  done
  log "$description: FAILED"
  return 1
}

write_env() {
  local file=$1
  shift
  : > "$file"
  chmod 600 "$file"
  for item in "$@"; do printf '%q\n' "$item" >> "$file"; done
}

issue_certificate() {
  local ca_cert=$1 ca_key=$2 name=$3 extension=$4 usage=$5
  openssl ecparam -name prime256v1 -genkey -noout -out "$TEMP_DIR/$name-key.pem" >/dev/null 2>&1
  openssl req -new -sha256 -key "$TEMP_DIR/$name-key.pem" -out "$TEMP_DIR/$name.csr" -subj "/CN=$name" >/dev/null 2>&1
  openssl x509 -req -sha256 -days 10 -in "$TEMP_DIR/$name.csr" -CA "$ca_cert" -CAkey "$ca_key" -CAcreateserial \
    -out "$TEMP_DIR/$name.pem" -extfile <(printf 'basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=%s\nsubjectAltName=%s\n' "$usage" "$extension") >/dev/null 2>&1
  chmod 600 "$TEMP_DIR/$name-key.pem"
}

cd "$ROOT"
docker info >/dev/null
log "TEST RUN: $SUFFIX"
log "TEMP RESOURCES: $TEMP_DIR"

MYSQL_PORT=$(random_port)
REDIS_PORT=$(random_port)
ETCD_PORT=$(random_port)
REST_A=$(random_port); REST_B=$(random_port)
WT_A=$(random_port); WT_B=$(random_port)
QUIC_A=$(random_port); QUIC_B=$(random_port)
RPC_A=$(random_port); RPC_B=$(random_port)
ADMIN_A=$(random_port); ADMIN_B=$(random_port)
VITE_A=$(random_port); VITE_B=$(random_port)
MYSQL_ROOT_PASSWORD=$(openssl rand -hex 24)
MYSQL_PASSWORD=$(openssl rand -hex 24)
REDIS_PASSWORD=$(openssl rand -hex 24)
JWT_SECRET=$(openssl rand -hex 32)
REPLAY_KEY=$(openssl rand -base64 32 | tr -d '\n')

openssl ecparam -name prime256v1 -genkey -noout -out "$TEMP_DIR/etcd-ca-key.pem" >/dev/null 2>&1
openssl req -new -x509 -sha256 -days 10 -key "$TEMP_DIR/etcd-ca-key.pem" -out "$TEMP_DIR/etcd-ca.pem" -subj '/CN=phase111-etcd-ca' >/dev/null 2>&1
issue_certificate "$TEMP_DIR/etcd-ca.pem" "$TEMP_DIR/etcd-ca-key.pem" etcd-server 'DNS:localhost,IP:127.0.0.1' 'serverAuth,clientAuth'
issue_certificate "$TEMP_DIR/etcd-ca.pem" "$TEMP_DIR/etcd-ca-key.pem" etcd-client 'DNS:localhost,IP:127.0.0.1' 'clientAuth'
cp "$TEMP_DIR/etcd-ca.pem" "$TEMP_DIR/ca.pem"
cp "$TEMP_DIR/etcd-client.pem" "$TEMP_DIR/client.pem"
cp "$TEMP_DIR/etcd-client-key.pem" "$TEMP_DIR/client-key.pem"

openssl ecparam -name prime256v1 -genkey -noout -out "$TEMP_DIR/internal-ca-key.pem" >/dev/null 2>&1
openssl req -new -x509 -sha256 -days 10 -key "$TEMP_DIR/internal-ca-key.pem" -out "$TEMP_DIR/internal-ca.pem" -subj '/CN=phase111-internal-ca' >/dev/null 2>&1
issue_certificate "$TEMP_DIR/internal-ca.pem" "$TEMP_DIR/internal-ca-key.pem" node-a 'URI:spiffe://collab-editor/node/node-a/incarnation/process-a,DNS:localhost,IP:127.0.0.1' 'serverAuth,clientAuth'
issue_certificate "$TEMP_DIR/internal-ca.pem" "$TEMP_DIR/internal-ca-key.pem" node-b 'URI:spiffe://collab-editor/node/node-b/incarnation/process-b,DNS:localhost,IP:127.0.0.1' 'serverAuth,clientAuth'
issue_certificate "$TEMP_DIR/internal-ca.pem" "$TEMP_DIR/internal-ca-key.pem" node-a2 'URI:spiffe://collab-editor/node/node-a/incarnation/process-a2,DNS:localhost,IP:127.0.0.1' 'serverAuth,clientAuth'
issue_certificate "$TEMP_DIR/internal-ca.pem" "$TEMP_DIR/internal-ca-key.pem" node-b2 'URI:spiffe://collab-editor/node/node-b/incarnation/process-b2,DNS:localhost,IP:127.0.0.1' 'serverAuth,clientAuth'

openssl ecparam -name prime256v1 -genkey -noout -out "$TEMP_DIR/transport-ca-key.pem" >/dev/null 2>&1
openssl req -new -x509 -sha256 -days 10 -key "$TEMP_DIR/transport-ca-key.pem" -out "$TEMP_DIR/transport-ca.pem" -subj '/CN=phase111-transport-ca' >/dev/null 2>&1
issue_certificate "$TEMP_DIR/transport-ca.pem" "$TEMP_DIR/transport-ca-key.pem" transport 'DNS:localhost,IP:127.0.0.1' 'serverAuth'

OPENSSL3=/opt/homebrew/opt/openssl@3/bin/openssl
if [[ ! -x "$OPENSSL3" ]]; then
  log 'OpenSSL 3 is required for the browser-pinned WebTransport certificate'
  exit 1
fi
"$OPENSSL3" req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -sha256 -nodes \
  -keyout "$TEMP_DIR/webtransport-key.pem" -out "$TEMP_DIR/webtransport.pem" \
  -days 13 -subj '/CN=localhost' \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1' \
  -addext 'keyUsage=digitalSignature' \
  -addext 'extendedKeyUsage=serverAuth' >/dev/null 2>&1
chmod 600 "$TEMP_DIR"/*-key.pem
WT_HASH=$("$OPENSSL3" x509 -in "$TEMP_DIR/webtransport.pem" -outform DER | "$OPENSSL3" dgst -sha256 -binary | "$OPENSSL3" base64 -A)

docker network create --label "com.collab-editor.test-run=$SUFFIX" "$NETWORK" >/dev/null
docker run -d --name "$MYSQL_CONTAINER" --network "$NETWORK" --label "com.collab-editor.test-run=$SUFFIX" \
  -p "127.0.0.1:$MYSQL_PORT:3306" \
  -e "MYSQL_ROOT_PASSWORD=$MYSQL_ROOT_PASSWORD" -e MYSQL_DATABASE=collab_editor -e MYSQL_USER=collab -e "MYSQL_PASSWORD=$MYSQL_PASSWORD" \
  -v "$ROOT/migrations:/docker-entrypoint-initdb.d:ro" mysql:8.4.13 >/dev/null
CONTAINERS+=("$MYSQL_CONTAINER")
docker run -d --name "$REDIS_CONTAINER" --network "$NETWORK" --label "com.collab-editor.test-run=$SUFFIX" \
  -p "127.0.0.1:$REDIS_PORT:6379" redis:7.4.5-alpine redis-server --save '' --appendonly no --requirepass "$REDIS_PASSWORD" >/dev/null
CONTAINERS+=("$REDIS_CONTAINER")
docker run -d --name "$ETCD_CONTAINER" --network "$NETWORK" --label "com.collab-editor.test-run=$SUFFIX" \
  -p "127.0.0.1:$ETCD_PORT:2379" -v "$TEMP_DIR:/certs:ro" quay.io/coreos/etcd:v3.5.17 \
  /usr/local/bin/etcd --name phase111 --data-dir=/tmp/etcd-data \
  --listen-client-urls=https://0.0.0.0:2379 --advertise-client-urls=https://127.0.0.1:2379 \
  --cert-file=/certs/etcd-server.pem --key-file=/certs/etcd-server-key.pem --trusted-ca-file=/certs/etcd-ca.pem --client-cert-auth=true >/dev/null
CONTAINERS+=("$ETCD_CONTAINER")

wait_for MYSQL docker exec "$MYSQL_CONTAINER" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin ping -h 127.0.0.1 -u root --silent'
wait_for REDIS docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" ping
wait_for ETCD docker exec "$ETCD_CONTAINER" etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/certs/ca.pem --cert=/certs/client.pem --key=/certs/client-key.pem endpoint health

go build -o "$TEMP_DIR/collab-server" ./cmd/server

COMMON=(
  "APP_ENV=development" "STORAGE_DRIVER=mysql" "MYSQL_DSN=collab:${MYSQL_PASSWORD}@tcp(127.0.0.1:${MYSQL_PORT})/collab_editor?charset=utf8mb4&parseTime=true&loc=UTC"
  "AUTH_MODE=jwt" "JWT_SECRET=$JWT_SECRET" "JWT_ISSUER=phase111" "JWT_AUDIENCE=phase111" "JWT_TTL=15m" "REFRESH_TOKEN_TTL=1h" "REFRESH_REPLAY_TTL=10m" "REFRESH_REPLAY_ENCRYPTION_KEY=$REPLAY_KEY"
  "REDIS_ADDR=127.0.0.1:${REDIS_PORT}" "REDIS_PASSWORD=$REDIS_PASSWORD" "REDIS_DB=0"
  "QUIC_TLS_CERT_FILE=$TEMP_DIR/transport.pem" "QUIC_TLS_KEY_FILE=$TEMP_DIR/transport-key.pem" "QUIC_CA_FILE=$TEMP_DIR/transport-ca.pem"
  "WEBTRANSPORT_ENABLED=true" "WEBTRANSPORT_TLS_CERT_FILE=$TEMP_DIR/webtransport.pem" "WEBTRANSPORT_TLS_KEY_FILE=$TEMP_DIR/webtransport-key.pem"
  "CORS_ALLOWED_ORIGINS=http://127.0.0.1:${VITE_A},http://127.0.0.1:${VITE_B}"
  "CLUSTER_MODE=cluster" "ETCD_ENDPOINTS=https://127.0.0.1:${ETCD_PORT}" "ETCD_CA_FILE=$TEMP_DIR/etcd-ca.pem" "ETCD_CERT_FILE=$TEMP_DIR/etcd-client.pem" "ETCD_KEY_FILE=$TEMP_DIR/etcd-client-key.pem"
  "ETCD_LEASE_TTL=3s" "ETCD_DIAL_TIMEOUT=3s" "ETCD_REQUEST_TIMEOUT=3s" "INTERNAL_RPC_CA_FILE=$TEMP_DIR/internal-ca.pem" "INTERNAL_RPC_TIMEOUT=3s"
  "MYSQL_PING_TIMEOUT=5s" "MYSQL_QUERY_TIMEOUT=5s" "LOGIN_RATE_LIMIT=1000" "REFRESH_RATE_LIMIT=1000" "INTERNAL_RPC_RATE_LIMIT=1000"
)
write_env "$TEMP_DIR/node-a.env" "${COMMON[@]}" "PORT=$REST_A" "WEBTRANSPORT_ADDR=127.0.0.1:$WT_A" "QUIC_ADDR=127.0.0.1:$QUIC_A" "ADMIN_ADDR=127.0.0.1:$ADMIN_A" "NODE_ID=node-a" "NODE_INCARNATION=process-a" "NODE_QUIC_ENDPOINT=127.0.0.1:$QUIC_A" "NODE_HTTP_ENDPOINT=https://127.0.0.1:$RPC_A" "NODE_BROWSER_ENDPOINT=https://127.0.0.1:$WT_A" "INTERNAL_RPC_ADDR=127.0.0.1:$RPC_A" "INTERNAL_RPC_CERT_FILE=$TEMP_DIR/node-a.pem" "INTERNAL_RPC_KEY_FILE=$TEMP_DIR/node-a-key.pem"
write_env "$TEMP_DIR/node-b.env" "${COMMON[@]}" "PORT=$REST_B" "WEBTRANSPORT_ADDR=127.0.0.1:$WT_B" "QUIC_ADDR=127.0.0.1:$QUIC_B" "ADMIN_ADDR=127.0.0.1:$ADMIN_B" "NODE_ID=node-b" "NODE_INCARNATION=process-b" "NODE_QUIC_ENDPOINT=127.0.0.1:$QUIC_B" "NODE_HTTP_ENDPOINT=https://127.0.0.1:$RPC_B" "NODE_BROWSER_ENDPOINT=https://127.0.0.1:$WT_B" "INTERNAL_RPC_ADDR=127.0.0.1:$RPC_B" "INTERNAL_RPC_CERT_FILE=$TEMP_DIR/node-b.pem" "INTERNAL_RPC_KEY_FILE=$TEMP_DIR/node-b-key.pem"
write_env "$TEMP_DIR/node-a2.env" "${COMMON[@]}" "PORT=$REST_A" "WEBTRANSPORT_ADDR=127.0.0.1:$WT_A" "QUIC_ADDR=127.0.0.1:$QUIC_A" "ADMIN_ADDR=127.0.0.1:$ADMIN_A" "NODE_ID=node-a" "NODE_INCARNATION=process-a2" "NODE_QUIC_ENDPOINT=127.0.0.1:$QUIC_A" "NODE_HTTP_ENDPOINT=https://127.0.0.1:$RPC_A" "NODE_BROWSER_ENDPOINT=https://127.0.0.1:$WT_A" "INTERNAL_RPC_ADDR=127.0.0.1:$RPC_A" "INTERNAL_RPC_CERT_FILE=$TEMP_DIR/node-a2.pem" "INTERNAL_RPC_KEY_FILE=$TEMP_DIR/node-a2-key.pem"
write_env "$TEMP_DIR/node-b2.env" "${COMMON[@]}" "PORT=$REST_B" "WEBTRANSPORT_ADDR=127.0.0.1:$WT_B" "QUIC_ADDR=127.0.0.1:$QUIC_B" "ADMIN_ADDR=127.0.0.1:$ADMIN_B" "NODE_ID=node-b" "NODE_INCARNATION=process-b2" "NODE_QUIC_ENDPOINT=127.0.0.1:$QUIC_B" "NODE_HTTP_ENDPOINT=https://127.0.0.1:$RPC_B" "NODE_BROWSER_ENDPOINT=https://127.0.0.1:$WT_B" "INTERNAL_RPC_ADDR=127.0.0.1:$RPC_B" "INTERNAL_RPC_CERT_FILE=$TEMP_DIR/node-b2.pem" "INTERNAL_RPC_KEY_FILE=$TEMP_DIR/node-b2-key.pem"

start_server() {
  local env_file=$1 log_file=$2
  /bin/bash --noprofile --norc -c 'set -a; source "$1"; set +a; exec "$2"' _ "$env_file" "$TEMP_DIR/collab-server" >"$log_file" 2>&1 &
  PIDS+=("$!")
}
start_server "$TEMP_DIR/node-a.env" "$TEMP_DIR/node-a.log"
NODE_A_PID=$!
start_server "$TEMP_DIR/node-b.env" "$TEMP_DIR/node-b.log"
NODE_B_PID=$!
wait_for 'NODE A' curl -fsS "http://127.0.0.1:$ADMIN_A/readyz"
wait_for 'NODE B' curl -fsS "http://127.0.0.1:$ADMIN_B/readyz"

(cd frontend && VITE_BACKEND_URL="http://127.0.0.1:$REST_A" VITE_REALTIME_TRANSPORT=webtransport VITE_WEBTRANSPORT_URLS="https://127.0.0.1:$WT_A/api/webtransport,https://127.0.0.1:$WT_B/api/webtransport" VITE_WEBTRANSPORT_CERT_SHA256="$WT_HASH" npm run dev -- --host 127.0.0.1 --port "$VITE_A") >"$TEMP_DIR/vite-a.log" 2>&1 &
PIDS+=("$!")
(cd frontend && VITE_BACKEND_URL="http://127.0.0.1:$REST_B" VITE_REALTIME_TRANSPORT=webtransport VITE_WEBTRANSPORT_URLS="https://127.0.0.1:$WT_B/api/webtransport,https://127.0.0.1:$WT_A/api/webtransport" VITE_WEBTRANSPORT_CERT_SHA256="$WT_HASH" npm run dev -- --host 127.0.0.1 --port "$VITE_B") >"$TEMP_DIR/vite-b.log" 2>&1 &
PIDS+=("$!")
wait_for 'VITE A' curl -fsS "http://127.0.0.1:$VITE_A/auth"
wait_for 'VITE B' curl -fsS "http://127.0.0.1:$VITE_B/auth"

MEMBERS=$(docker exec "$ETCD_CONTAINER" etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/certs/ca.pem --cert=/certs/client.pem --key=/certs/client-key.pem get /collab/v1/nodes/ --prefix --keys-only | grep -c '/collab/v1/nodes/' || true)
[[ "$MEMBERS" -eq 2 ]] || { log "ETCD MEMBERSHIP: expected 2, got $MEMBERS"; exit 1; }
log 'ETCD MEMBERSHIP: 2'

export E2E_NODE_A_URL="http://127.0.0.1:$VITE_A" E2E_NODE_B_URL="http://127.0.0.1:$VITE_B"
export E2E_NODE_A_PID="$NODE_A_PID" E2E_NODE_B_PID="$NODE_B_PID"
export E2E_NODE_A_RESTART_ENV="$TEMP_DIR/node-a2.env" E2E_NODE_B_RESTART_ENV="$TEMP_DIR/node-b2.env"
export E2E_SERVER_BINARY="$TEMP_DIR/collab-server" E2E_PROJECT_ROOT="$ROOT" E2E_ETCD_CONTAINER="$ETCD_CONTAINER" E2E_RESTART_PID_FILE="$TEMP_DIR/restart-pids"
export E2E_BROWSER_CHANNEL=chrome

log 'REAL CHROME DISTRIBUTED WEBTRANSPORT: START'
(cd frontend && npx playwright test e2e/distributed-webtransport.spec.ts --reporter=line) 2>&1 | tee -a "$REPORT"
grep -q '"event":"owner_redirect"' "$TEMP_DIR/node-a.log" "$TEMP_DIR/node-b.log" || { log 'OWNER REDIRECT LOG: MISSING'; exit 1; }
log 'OWNER REDIRECT LOG: PASS'
log 'REAL CHROME DISTRIBUTED WEBTRANSPORT: PASS'
