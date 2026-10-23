#!/usr/bin/env bash
# Register credentials from another project's .env into the OS keychain under
# the service the AI Company resolves its secrets from.
#
# The AI Company never stores a secret: a runner, an integration or a tenant
# references a secret by name ("ai-company/github:orlandoburli") and the
# server resolves it through the login keychain at the moment it needs it
# (internal/integration/credentials.go). This script is the way to put a
# credential under such a name without the value ever entering the database,
# a log line or this repository.
#
# Usage:
#   scripts/register-credentials.sh <source.env> [service]
#
#   <source.env>  the file to read (e.g. ../project-erp/.env)
#   [service]     the keychain service (default: ai-company)
#
# The mapping below is the one the API (Apiary) project in ../project-erp uses:
# five GitHub tokens — one per agent persona — plus the Slack bot, the Plane
# key and the Hetzner/Cloudflare tokens. An account that already exists is
# left alone unless --force is given, so a live integration is never broken
# by a re-run.
#
# Secrets are read from the file and passed to the keychain on stdin; they are
# never passed as arguments and never printed.
set -euo pipefail

src="${1:-}"
service="${2:-ai-company}"
force=0
[ "${3:-}" = "--force" ] && force=1

[ -n "$src" ] || { echo "usage: $0 <source.env> [service] [--force]" >&2; exit 2; }
[ -f "$src" ] || { echo "register-credentials: no such file: $src" >&2; exit 2; }

# variable in the source env -> keychain account under the service
mapping="
GITHUB_TOKEN=github:apiary-default
GITHUB_TOKEN_STAFF=github:apiary-staff
GITHUB_TOKEN_ENGINEER=github:apiary-engineer
GITHUB_TOKEN_REVIEWER=github:apiary-reviewer
GITHUB_TOKEN_QA=github:apiary-qa
SLACK_BOT_TOKEN=slack:bot
SLACK_CHANNEL_ID=slack:channel
SLACK_CHANNEL_ID_INTEGRATION=slack:channel:integration
SLACK_OPERATOR_USER=slack:operator
PLANE_API_KEY=plane:api
HCLOUD_TOKEN=hcloud:token
CLOUDFLARE_API_TOKEN=cloudflare:api-token
CLOUDFLARE_ZONE_ID=cloudflare:zone-id
CLOUDFLARE_ACCOUNT_ID=cloudflare:account-id
"

# The identity behind each GitHub token, for the keychain comment.
declare -a notes=(
  "GITHUB_TOKEN=the account's general-purpose token"
  "GITHUB_TOKEN_STAFF=orlandodeveloper02 (the staff persona)"
  "GITHUB_TOKEN_ENGINEER=orlandoburli (the engineer persona)"
  "GITHUB_TOKEN_REVIEWER=orlandodeveloper01 (the reviewer persona)"
  "GITHUB_TOKEN_QA=orlandoburliqa01 (the QA persona)"
)

note_for() {
  local var="$1" entry
  for entry in "${notes[@]}"; do
    [ "${entry%%=*}" = "$var" ] && { echo "${entry#*=}"; return 0; }
  done
  echo "taken from $src"
}

registered=0
skipped=0
missing=0

while IFS= read -r line; do
  [ -n "$line" ] || continue
  var="${line%%=*}"
  account="${line#*=}"
  case "$var" in \#*|'') continue ;; esac

  value="$(grep -E "^${var}=" "$src" | head -1 | cut -d= -f2- || true)"
  value="$(printf '%s' "$value" | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")"
  if [ -z "$value" ]; then
    echo "  skip  $var — not in $src"
    missing=$((missing + 1))
    continue
  fi

  if [ "$force" -eq 0 ] && security find-generic-password -s "$service" -a "$account" >/dev/null 2>&1; then
    echo "  keep  $service/$account — already registered"
    skipped=$((skipped + 1))
    continue
  fi

  # -w as the last option prompts for the password twice; the value is piped
  # in (twice) so the secret never appears in the process list.
  if printf '%s\n%s' "$value" "$value" | security add-generic-password -U -a "$account" -s "$service" -j "$(note_for "$var")" -l "$service/$account" -w >/dev/null 2>&1; then
    echo "  ok    $service/$account  ($var)"
    registered=$((registered + 1))
  else
    echo "  FAIL  $service/$account  ($var)" >&2
  fi
done <<< "$mapping"

echo
echo "registered $registered, kept $skipped, missing $missing — service '$service'"
echo "reference one as \"$service/<account>\" (the tenant's integration, or a runner's credential_ref)"
