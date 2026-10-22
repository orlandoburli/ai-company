#!/usr/bin/env bash
# Smoke check against a running, seeded server. Exercises the endpoints a
# person touches right after `make dev`: health, sign-in, the dispatch queue,
# the tree and the product list — all against the tenant cmd/seed creates.
#
# Usage (defaults in parentheses):
#   AIC_URL=http://localhost:18081 AIC_EMAIL=owner@acme.dev \
#   AIC_PASSWORD=correct-horse-battery-staple ./scripts/smoke.sh
set -euo pipefail

AIC_URL="${AIC_URL:-http://localhost:8080}"
AIC_EMAIL="${AIC_EMAIL:-owner@acme.dev}"
AIC_PASSWORD="${AIC_PASSWORD:-correct-horse-battery-staple}"

fail() {
    echo "smoke: FAIL: $*" >&2
    exit 1
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT  # cookie jar lives here and dies with it

# --- healthz ------------------------------------------------------------------
code="$(curl -sS -o /dev/null -w '%{http_code}' "$AIC_URL/healthz")" \
    || fail "GET $AIC_URL/healthz could not be reached; is the server running?"
[ "$code" = "200" ] || fail "GET /healthz returned $code, want 200"
echo "ok   /healthz 200"

# --- login, session cookie into the temp jar ----------------------------------
python3 - "$AIC_EMAIL" "$AIC_PASSWORD" > "$tmp/login.json" <<'PY'
import json, sys
print(json.dumps({"email": sys.argv[1], "password": sys.argv[2]}))
PY

code="$(curl -sS -o /dev/null -w '%{http_code}' -c "$tmp/jar" \
    -H 'Content-Type: application/json' --data-binary @"$tmp/login.json" \
    "$AIC_URL/api/auth/login")" || fail "POST $AIC_URL/api/auth/login could not be reached"
[ "$code" = "200" ] || fail "POST /api/auth/login returned $code, want 200 (check AIC_EMAIL / AIC_PASSWORD)"

# The cookie jar must hold a real session value, not just a name.
grep -q $'\taic_session\t' "$tmp/jar" \
    || fail "login did not store a session cookie in the jar"
echo "ok   /api/auth/login 200, session cookie set"

# --- authenticated reads --------------------------------------------------------
get() { # name path output-file
    local name="$1" path="$2" out="$3"
    code="$(curl -sS -b "$tmp/jar" -o "$out" -w '%{http_code}' "$AIC_URL$path")" \
        || fail "GET $path could not be reached"
    [ "$code" = "200" ] || fail "GET $path returned $code, want 200"
}
get queue    /api/queue    "$tmp/queue.json"
get tree     /api/tree     "$tmp/tree.json"
get products /api/products "$tmp/products.json"

# --- JSON assertions (python3: no jq dependency) -------------------------------
python3 - "$tmp/queue.json" "$tmp/tree.json" "$tmp/products.json" <<'PY'
import json, sys

def load(path):
    with open(path) as f:
        return json.load(f)

def die(msg):
    print(f"smoke: FAIL: {msg}", file=sys.stderr)
    sys.exit(1)

queue = load(sys.argv[1]).get("queue")
if not isinstance(queue, list) or not queue:
    die("/api/queue returned no dispatchable items")
front = queue[0]
if not front.get("pinned"):
    die(f"/api/queue front entry is not pinned: item id {front.get('item', {}).get('id')}")
paused = [e.get("item", {}).get("id") for e in queue if e.get("paused")]
if paused:
    die(f"/api/queue contains paused item(s): {paused}")
print(f"ok   /api/queue pinned item first, no paused items ({len(queue)} entries)")

projects = load(sys.argv[2]).get("projects")
if not isinstance(projects, list) or not projects:
    die("/api/tree is empty")
print(f"ok   /api/tree non-empty ({len(projects)} root project(s))")

products = load(sys.argv[3]).get("products")
if not isinstance(products, list) or len(products) < 1:
    die("/api/products returned fewer than 1 product")
print(f"ok   /api/products {len(products)} product(s)")
PY
echo "smoke: OK — all checks passed (AIC_URL=$AIC_URL)"
