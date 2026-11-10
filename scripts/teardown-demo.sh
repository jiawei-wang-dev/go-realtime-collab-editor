#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=./scripts/demo-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/demo-common.sh"

require_project
for command_name in gcloud terraform; do
  require_command "$command_name"
done

printf 'Creating and verifying a local MySQL backup before destruction...\n'
backup_path="$("$DEMO_REPO_ROOT/scripts/backup-demo.sh")"
if [[ ! -s "$backup_path" || ! -s "${backup_path}.sha256" ]]; then
  printf 'Verified local backup was not produced; refusing destruction.\n' >&2
  exit 1
fi
printf 'Verified backup: %s\n' "$backup_path"

expected="DESTROY ${DEMO_VM_NAME} IN ${GCP_PROJECT_ID}"
printf 'Type exactly %q to destroy the VM, disk, VPC and firewall: ' "$expected"
IFS= read -r confirmation
if [[ "$confirmation" != "$expected" ]]; then
  printf 'Destruction cancelled; the verified backup remains at %s\n' "$backup_path"
  exit 1
fi

terraform -chdir="$DEMO_TF_DIR" destroy \
  -var="project_id=$GCP_PROJECT_ID" \
  -var="region=$GCP_REGION" \
  -var="zone=$GCP_ZONE"

if gcloud compute instances describe "$DEMO_VM_NAME" --project="$GCP_PROJECT_ID" --zone="$GCP_ZONE" >/dev/null 2>&1; then
  printf 'VM still exists after Terraform destroy; investigate before assuming billing stopped.\n' >&2
  exit 1
fi
printf 'Demo resources destroyed. Local backup retained: %s\n' "$backup_path"
