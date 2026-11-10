#!/usr/bin/env bash

set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  printf 'Run this script as root on the dedicated demo VM.\n' >&2
  exit 1
fi

repo_root="${DEMO_REMOTE_ROOT:-/opt/collab-demo}"
state_root="${DEMO_REMOTE_STATE:-/opt/collab-demo-state}"
cert_dir="$state_root/certs"
env_file="$state_root/runtime.env"
compose_file="$repo_root/deploy/demo/docker-compose.demo.yml"

require_command() {
  command -v "$1" >/dev/null 2>&1 || { printf 'Required command not found: %s\n' "$1" >&2; exit 1; }
}

for command_name in docker openssl awk grep install; do
  require_command "$command_name"
done

install -d -m 0750 "$state_root" "$cert_dir"

if [[ ! -f "$env_file" ]]; then
  umask 077
  mysql_root_password="$(openssl rand -hex 32)"
  mysql_password="$(openssl rand -hex 32)"
  redis_password="$(openssl rand -hex 32)"
  jwt_secret="$(openssl rand -hex 32)"
  replay_key="$(openssl rand -base64 32 | tr -d '\n')"
  runner_token="$(openssl rand -hex 32)"
  docker_gid="$(stat -c '%g' /var/run/docker.sock)"

  {
    printf 'MYSQL_ROOT_PASSWORD=%s\n' "$mysql_root_password"
    printf 'MYSQL_DATABASE=collab_editor\n'
    printf 'MYSQL_USER=collab\n'
    printf 'MYSQL_PASSWORD=%s\n' "$mysql_password"
    printf 'REDIS_PASSWORD=%s\n' "$redis_password"
    printf 'JWT_SECRET=%s\n' "$jwt_secret"
    printf 'REFRESH_REPLAY_ENCRYPTION_KEY=%s\n' "$replay_key"
    printf 'RUNNER_TOKEN=%s\n' "$runner_token"
    printf 'PUBLIC_ORIGIN=https://pending.invalid\n'
    printf 'DEMO_CERT_DIR=%s\n' "$cert_dir"
    printf 'DOCKER_GID=%s\n' "$docker_gid"
  } >"$env_file"
  chmod 0600 "$env_file"
fi

if [[ ! -s "$cert_dir/ca.crt" || ! -s "$cert_dir/server.crt" || ! -s "$cert_dir/server.key" ]]; then
  umask 077
  openssl genrsa -out "$cert_dir/ca.key" 3072
  openssl req -x509 -new -sha256 -days 30 \
    -key "$cert_dir/ca.key" \
    -subj '/CN=collab-demo-internal-ca' \
    -out "$cert_dir/ca.crt"
  openssl genrsa -out "$cert_dir/server.key" 3072
  openssl req -new -sha256 \
    -key "$cert_dir/server.key" \
    -subj '/CN=backend' \
    -out "$cert_dir/server.csr"
  cat >"$cert_dir/server.ext" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:backend,DNS:localhost,IP:127.0.0.1
EOF
  openssl x509 -req -sha256 -days 14 \
    -in "$cert_dir/server.csr" \
    -CA "$cert_dir/ca.crt" \
    -CAkey "$cert_dir/ca.key" \
    -CAcreateserial \
    -extfile "$cert_dir/server.ext" \
    -out "$cert_dir/server.crt"
  rm -f "$cert_dir/server.csr" "$cert_dir/server.ext" "$cert_dir/ca.srl"
  chown root:10001 "$cert_dir/server.key"
  chmod 0644 "$cert_dir/ca.crt" "$cert_dir/server.crt"
  chmod 0640 "$cert_dir/server.key"
  chmod 0600 "$cert_dir/ca.key"
fi

docker pull golang:1.26.10-bookworm >/dev/null
docker compose --env-file "$env_file" -f "$compose_file" config >/dev/null
docker compose --env-file "$env_file" -f "$compose_file" up -d --build

public_origin=""
for _ in $(seq 1 60); do
  public_origin="$(docker compose --env-file "$env_file" -f "$compose_file" logs --no-color cloudflared 2>&1 | grep -Eo 'https://[a-z0-9-]+\.trycloudflare\.com' | tail -n 1 || true)"
  [[ -n "$public_origin" ]] && break
  sleep 2
done

if [[ -z "$public_origin" ]]; then
  printf 'Cloudflare Quick Tunnel did not publish a URL.\n' >&2
  docker compose --env-file "$env_file" -f "$compose_file" logs --no-color cloudflared >&2
  exit 1
fi

tmp_env="$(mktemp "$state_root/runtime.env.XXXXXX")"
awk -v origin="$public_origin" '
  BEGIN { replaced=0 }
  /^PUBLIC_ORIGIN=/ { print "PUBLIC_ORIGIN=" origin; replaced=1; next }
  { print }
  END { if (!replaced) print "PUBLIC_ORIGIN=" origin }
' "$env_file" >"$tmp_env"
chmod 0600 "$tmp_env"
mv "$tmp_env" "$env_file"

docker compose --env-file "$env_file" -f "$compose_file" up -d --no-deps --force-recreate backend

for _ in $(seq 1 40); do
  if docker compose --env-file "$env_file" -f "$compose_file" exec -T nginx wget -qO- http://127.0.0.1/demo-healthz | grep -qx ok; then
    printf '%s\n' "$public_origin" >"$state_root/public-url"
    chmod 0644 "$state_root/public-url"
    printf 'Demo stack ready: %s\n' "$public_origin"
    exit 0
  fi
  sleep 2
done

printf 'Nginx did not become healthy after the backend origin update.\n' >&2
exit 1
