#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
public_url="${1:-}"
mode="${2:-full}"

if [[ ! "$public_url" =~ ^https://[a-z0-9-]+\.trycloudflare\.com$ ]]; then
  printf 'Usage: %s https://example.trycloudflare.com [full|http-only]\n' "$0" >&2
  exit 1
fi

frontend_status="$(curl -fsS -o /tmp/collab-demo-index.$$ -w '%{http_code}' "$public_url/")"
trap 'rm -f /tmp/collab-demo-index.$$ /tmp/collab-demo-api.$$' EXIT
[[ "$frontend_status" == "200" ]] || { printf 'Frontend returned HTTP %s\n' "$frontend_status" >&2; exit 1; }
grep -q '<div id="root"' /tmp/collab-demo-index.$$

api_status="$(curl -sS -o /tmp/collab-demo-api.$$ -w '%{http_code}' "$public_url/api/auth/me")"
[[ "$api_status" == "401" ]] || { printf 'Authenticated API probe returned HTTP %s, expected 401.\n' "$api_status" >&2; exit 1; }
printf 'HTTP frontend and API routing: PASS\n'

if [[ "$mode" == "http-only" ]]; then
  printf 'Browser WebSocket and Runner verification: NOT RUN (http-only requested)\n'
  exit 0
fi
if [[ "$mode" != "full" ]]; then
  printf 'Unknown verification mode: %s\n' "$mode" >&2
  exit 1
fi

if [[ ! -x "$repo_root/frontend/node_modules/.bin/playwright" ]]; then
  printf 'Playwright dependencies are missing. Run npm ci in frontend before full verification.\n' >&2
  exit 1
fi

(
  cd "$repo_root/frontend"
  E2E_BASE_URL="$public_url" npx playwright test e2e/execution.spec.ts
)
printf 'WebSocket collaboration and isolated Go execution: PASS\n'
