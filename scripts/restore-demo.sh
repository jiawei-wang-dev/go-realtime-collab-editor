#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=./scripts/demo-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/demo-common.sh"

require_project
for command_name in gcloud gzip shasum; do
  require_command "$command_name"
done

backup_file="${1:-}"
if [[ -z "$backup_file" || ! -f "$backup_file" ]]; then
  printf 'Usage: GCP_PROJECT_ID=... %s /absolute/path/backup.sql.gz\n' "$0" >&2
  exit 1
fi
if [[ -f "${backup_file}.sha256" ]]; then
  (cd "$(dirname "$backup_file")" && shasum -a 256 -c "$(basename "${backup_file}.sha256")")
fi
gzip -t "$backup_file"

expected="RESTORE $(basename "$backup_file")"
printf 'This replaces the demo database contents. Type exactly %q: ' "$expected"
IFS= read -r confirmation
[[ "$confirmation" == "$expected" ]] || { printf 'Restore cancelled.\n'; exit 1; }

remote_command="sudo docker compose --env-file '$DEMO_REMOTE_STATE/runtime.env' -f '$DEMO_REMOTE_ROOT/deploy/demo/docker-compose.demo.yml' exec -T mysql sh -c 'exec mysql --user=root --password=\"\$MYSQL_ROOT_PASSWORD\" \"\$MYSQL_DATABASE\"'"
gzip -dc "$backup_file" | gcloud_ssh --command="$remote_command"
printf 'Restore completed from %s\n' "$backup_file"
