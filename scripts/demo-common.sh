#!/usr/bin/env bash

set -euo pipefail

DEMO_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC2034 # Consumed by scripts that source this file.
DEMO_TF_DIR="$DEMO_REPO_ROOT/deploy/demo-gcp"
DEMO_VM_NAME="${DEMO_VM_NAME:-collab-public-demo-vm}"
GCP_REGION="${GCP_REGION:-us-central1}"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
DEMO_REMOTE_ROOT="${DEMO_REMOTE_ROOT:-/opt/collab-demo}"
DEMO_REMOTE_STATE="${DEMO_REMOTE_STATE:-/opt/collab-demo-state}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'Required command not found: %s\n' "$1" >&2
    return 1
  fi
}

require_project() {
  if [[ -z "${GCP_PROJECT_ID:-}" ]]; then
    printf 'Set GCP_PROJECT_ID to the existing Free Trial project ID.\n' >&2
    return 1
  fi
}

gcloud_ssh() {
  gcloud compute ssh "$DEMO_VM_NAME" \
    --project="$GCP_PROJECT_ID" \
    --zone="$GCP_ZONE" \
    --tunnel-through-iap \
    --quiet \
    "$@"
}

gcloud_scp() {
  gcloud compute scp \
    --project="$GCP_PROJECT_ID" \
    --zone="$GCP_ZONE" \
    --tunnel-through-iap \
    --quiet \
    "$@"
}
