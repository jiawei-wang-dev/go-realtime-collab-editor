#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=./scripts/demo-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/demo-common.sh"

require_project
for command_name in gcloud gzip shasum; do
  require_command "$command_name"
done

backup_dir="${DEMO_BACKUP_DIR:-$HOME/collab-demo-backups}"
install -d -m 0700 "$backup_dir"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
base="$backup_dir/collab-demo-${timestamp}.sql"
tmp_file="${base}.tmp"
archive="${base}.gz"

cleanup() {
  rm -f "$tmp_file"
}
trap cleanup EXIT

remote_command="sudo docker compose --env-file '$DEMO_REMOTE_STATE/runtime.env' -f '$DEMO_REMOTE_ROOT/deploy/demo/docker-compose.demo.yml' exec -T mysql sh -c 'exec mysqldump --user=root --password=\"\$MYSQL_ROOT_PASSWORD\" --single-transaction --routines --events --triggers --set-gtid-purged=OFF \"\$MYSQL_DATABASE\"'"
gcloud_ssh --command="$remote_command" >"$tmp_file"

if [[ ! -s "$tmp_file" ]] || ! grep -q '^-- MySQL dump' "$tmp_file" || ! grep -q '^-- Dump completed on' "$tmp_file"; then
  printf 'Backup verification failed; Terraform state and VM were not changed.\n' >&2
  exit 1
fi

gzip -c "$tmp_file" >"$archive"
gzip -t "$archive"
shasum -a 256 "$archive" >"${archive}.sha256"
chmod 0600 "$archive" "${archive}.sha256"
printf '%s\n' "$archive"
