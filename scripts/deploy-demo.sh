#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=./scripts/demo-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/demo-common.sh"

require_project
for command_name in gcloud terraform tar; do
  require_command "$command_name"
done

if [[ "$(git -C "$DEMO_REPO_ROOT" branch --show-current)" != "main" ]]; then
  printf 'Refusing deployment from a non-main branch.\n' >&2
  exit 1
fi

printf '%s\n' 'Reviewed fixed-resource estimate (us-central1 list price):'
printf '  e2-medium:                 USD %.8f/hour\n' 0.03350571
printf '  in-use external IPv4:     USD %.8f/hour\n' 0.005
printf '  20 GiB pd-standard:       USD %.8f/hour\n' 0.00109590
printf '  fixed total:              USD %.8f/hour, USD %.8f/day\n' 0.03960161 0.95043864
printf '%s\n' '  Variable internet egress is not included. Eligible free-tier disk credits can reduce the actual bill.'
printf '%s\n' 'This script never enables or upgrades billing, but Terraform will create one billable VM when confirmed.'

expected="CREATE ONE DEMO VM IN ${GCP_PROJECT_ID}"
printf 'Type exactly %q to continue: ' "$expected"
IFS= read -r confirmation
if [[ "$confirmation" != "$expected" ]]; then
  printf 'Cancelled; no Terraform operation was run.\n'
  exit 1
fi

active_account="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' | head -n 1)"
if [[ -z "$active_account" ]]; then
  printf 'No active gcloud account.\n' >&2
  exit 1
fi
gcloud projects describe "$GCP_PROJECT_ID" --format='value(projectId)' >/dev/null

terraform -chdir="$DEMO_TF_DIR" init -upgrade=false
terraform -chdir="$DEMO_TF_DIR" validate
plan_file="$DEMO_TF_DIR/demo.tfplan"
terraform -chdir="$DEMO_TF_DIR" plan -out="$plan_file" -var="project_id=$GCP_PROJECT_ID" -var="region=$GCP_REGION" -var="zone=$GCP_ZONE"
terraform -chdir="$DEMO_TF_DIR" apply "$plan_file"
rm -f "$plan_file"

for _ in $(seq 1 40); do
  if gcloud_ssh --command='test -f /var/lib/collab-demo-bootstrap-ready'; then
    break
  fi
  sleep 10
done
gcloud_ssh --command='test -f /var/lib/collab-demo-bootstrap-ready'

archive="$(mktemp -t collab-demo-source.XXXXXX.tar.gz)"
trap 'rm -f "$archive"' EXIT
tar -C "$DEMO_REPO_ROOT" -czf "$archive" \
  --exclude='.git' \
  --exclude='.env' \
  --exclude='frontend/node_modules' \
  --exclude='frontend/dist' \
  --exclude='frontend/test-results' \
  --exclude='frontend/playwright-report' \
  --exclude='deploy/demo-gcp/.terraform' \
  --exclude='deploy/demo-gcp/*.tfstate*' \
  .
gcloud_scp "$archive" "$DEMO_VM_NAME:/tmp/collab-demo-source.tar.gz"
gcloud_ssh --command="sudo rm -rf '$DEMO_REMOTE_ROOT' && sudo install -d -m 0755 '$DEMO_REMOTE_ROOT' && sudo tar -xzf /tmp/collab-demo-source.tar.gz -C '$DEMO_REMOTE_ROOT' && sudo rm -f /tmp/collab-demo-source.tar.gz && sudo DEMO_REMOTE_ROOT='$DEMO_REMOTE_ROOT' DEMO_REMOTE_STATE='$DEMO_REMOTE_STATE' '$DEMO_REMOTE_ROOT/scripts/prepare-demo-host.sh'"

public_url="$(gcloud_ssh --command="sudo cat '$DEMO_REMOTE_STATE/public-url'" | tail -n 1)"
printf 'Public demo URL: %s\n' "$public_url"
"$DEMO_REPO_ROOT/scripts/verify-demo.sh" "$public_url"
