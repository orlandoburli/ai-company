#!/usr/bin/env bash
# Smoke check for the M3 workflow engine against a running, seeded server.
#
# It starts a real run of a published template, waits for the checkpoint it
# parks on, decides it, and waits for the run to finish. The template defaults
# to "Review" (one CLI agent and one checkpoint) because it is the shortest
# path through the engine that still exercises a human decision.
#
# Usage (defaults in parentheses):
#   AIC_URL=http://localhost:18081 AIC_EMAIL=owner@acme.dev \
#   AIC_PASSWORD=correct-horse-battery-staple AIC_TEMPLATE=Review \
#   ./scripts/smoke-workflow.sh
set -euo pipefail

AIC_URL="${AIC_URL:-http://localhost:8080}"
AIC_EMAIL="${AIC_EMAIL:-owner@acme.dev}"
AIC_PASSWORD="${AIC_PASSWORD:-correct-horse-battery-staple}"
AIC_TEMPLATE="${AIC_TEMPLATE:-Review}"
AIC_TIMEOUT="${AIC_TIMEOUT:-240}"
# The state the run must end in. "Review" and "Bugfix" end completed; the
# bounded-cycle template ends failed on purpose (its budget runs out).
AIC_EXPECT_STATE="${AIC_EXPECT_STATE:-completed}"
# How many checkpoint decisions the template is expected to need. The
# bounded-cycle template has none: it ends on its iteration budget.
AIC_EXPECT_CHECKPOINTS="${AIC_EXPECT_CHECKPOINTS:-1}"

fail() {
    echo "smoke-workflow: FAIL: $*" >&2
    exit 1
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT  # cookie jar lives here and dies with it

# --- login, session cookie into the temp jar ----------------------------------
code="$(curl -sS -o /dev/null -w '%{http_code}' -c "$tmp/jar" \
    -H 'Content-Type: application/json' \
    --data-binary "{\"email\":\"$AIC_EMAIL\",\"password\":\"$AIC_PASSWORD\"}" \
    "$AIC_URL/api/auth/login")" || fail "POST $AIC_URL/api/auth/login could not be reached"
[ "$code" = "200" ] || fail "POST /api/auth/login returned $code, want 200 (check AIC_EMAIL / AIC_PASSWORD)"
echo "ok   /api/auth/login 200, session cookie set"

# --- the template --------------------------------------------------------------
code="$(curl -sS -b "$tmp/jar" -o "$tmp/templates.json" -w '%{http_code}' \
    "$AIC_URL/api/workflow-templates")" || fail "GET /api/workflow-templates could not be reached"
[ "$code" = "200" ] || fail "GET /api/workflow-templates returned $code, want 200"

template_id="$(python3 - "$tmp/templates.json" "$AIC_TEMPLATE" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
for tpl in data.get("templates", []):
    if tpl.get("name") == sys.argv[2]:
        print(tpl.get("id", ""))
        break
PY
)"
[ -n "$template_id" ] || fail "no published template named '$AIC_TEMPLATE' — run the seed first (make seed)"
echo "ok   template '$AIC_TEMPLATE' found ($template_id)"

# --- an item to run against ----------------------------------------------------
code="$(curl -sS -b "$tmp/jar" -o "$tmp/items.json" -w '%{http_code}' \
    "$AIC_URL/api/items?limit=1")" || fail "GET /api/items could not be reached"
[ "$code" = "200" ] || fail "GET /api/items returned $code, want 200"

item_id="$(python3 - "$tmp/items.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1])).get("items", [])
print(items[0]["id"] if items else "")
PY
)"
[ -n "$item_id" ] || fail "the tenant has no items — run the seed first (make seed)"
echo "ok   item $item_id"

# --- start the run -------------------------------------------------------------
code="$(curl -sS -b "$tmp/jar" -o "$tmp/run.json" -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    --data-binary "{\"template_id\":\"$template_id\"}" \
    "$AIC_URL/api/items/$item_id/runs")" || fail "POST /api/items/$item_id/runs could not be reached"
[ "$code" = "202" ] || fail "POST /api/items/$item_id/runs returned $code, want 202: $(cat "$tmp/run.json")"

run_id="$(python3 - "$tmp/run.json" <<'PY'
import json, sys
print(json.load(open(sys.argv[1])).get("run", {}).get("id", ""))
PY
)"
[ -n "$run_id" ] || fail "the run response carried no run id"
echo "ok   run $run_id started"

# --- drive it to the end --------------------------------------------------------
# The run parks on a checkpoint; the script decides it and keeps waiting. The
# agent itself takes real time, so the loop is generous but bounded.
deadline=$(( $(date +%s) + AIC_TIMEOUT ))
decided=0
final_state=""
while [ "$(date +%s)" -lt "$deadline" ]; do
    code="$(curl -sS -b "$tmp/jar" -o "$tmp/state.json" -w '%{http_code}' \
        "$AIC_URL/api/runs/$run_id")" || fail "GET /api/runs/$run_id could not be reached"
    [ "$code" = "200" ] || fail "GET /api/runs/$run_id returned $code, want 200"

    # The checkpoint is its own row: the decision endpoint takes the
    # checkpoint id, not the id of the step that parked.
    pending="$(python3 - "$tmp/state.json" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
for cp in data.get("checkpoints", []):
    if cp.get("state") == "pending":
        print(cp.get("id", ""))
        break
PY
)"
    # A graph can park on more than one checkpoint: approve each one as it
    # shows up, until the run reaches a terminal state.
    if [ -n "$pending" ]; then
        cp="$(curl -sS -b "$tmp/jar" -o "$tmp/decision.json" -w '%{http_code}' \
            -H 'Content-Type: application/json' \
            --data-binary '{"decision":"approve"}' \
            "$AIC_URL/api/checkpoints/$pending/decision")" || fail "POST /api/checkpoints/$pending/decision could not be reached"
        [ "$cp" = "200" ] || fail "POST /api/checkpoints/$pending/decision returned $cp, want 200: $(cat "$tmp/decision.json")"
        decided=$((decided + 1))
        echo "ok   checkpoint $pending approved"
    fi

    final_state="$(python3 - "$tmp/state.json" <<'PY'
import json, sys
print(json.load(open(sys.argv[1])).get("run", {}).get("state", ""))
PY
)"
    case "$final_state" in
        completed|failed|cancelled) break ;;
    esac
    sleep 2
done

python3 - "$tmp/state.json" "$final_state" "$decided" "$AIC_EXPECT_STATE" "$AIC_EXPECT_CHECKPOINTS" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
state = sys.argv[2]
decided = int(sys.argv[3])
want = sys.argv[4]
want_checkpoints = int(sys.argv[5])

def die(msg):
    print(f"smoke-workflow: FAIL: {msg}", file=sys.stderr)
    for step in data.get("steps", []):
        print(f"  step {step.get('node_id')} [{step.get('state')}]", file=sys.stderr)
    sys.exit(1)

if state != want:
    die(f"run ended in state '{state}', want '{want}'")
if decided < want_checkpoints:
    die(f"the run decided {decided} checkpoint(s), want {want_checkpoints}")
steps = data.get("steps", [])
if len(steps) < 2:
    die(f"the run has {len(steps)} step(s), want at least 2")
if want == "completed" and any(s.get("state") == "failed" for s in steps):
    die("the run completed but a step failed")
print(f"ok   run {data.get('run', {}).get('id')} completed with {len(steps)} steps and {decided} checkpoint decision(s)")
PY
echo "smoke-workflow: OK — all checks passed (AIC_URL=$AIC_URL)"
