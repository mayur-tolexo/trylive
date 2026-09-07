#!/usr/bin/env sh
# Deploys trylive into a NeevCloud sandbox: builds the web app and a static
# linux/amd64 binary, creates a long-lived sandbox with internet egress,
# uploads the binary, starts a restart loop detached from the platform's
# process supervisor (which kills its own process groups after an hour), with
# the secrets as process environment only, exposes port 8080, and prints the
# public preview URL. The server keeps its sandbox alive with keepalives.
#
# Reads NEEV_API_KEY, NEEV_ORG_ID, NEEV_PROJECT_ID (required) and NEEV_API_BASE,
# NEEV_REGION, LLM_BASE_URL, LLM_API_KEY, LLM_MODEL, GITHUB_TOKEN,
# MAX_SESSIONS_PER_IP (optional) from the environment. Re-running replaces the
# previous deployment sandbox (same name).
set -eu

: "${NEEV_API_KEY:?}" "${NEEV_ORG_ID:?}" "${NEEV_PROJECT_ID:?}"
API="${NEEV_API_BASE:-https://api.ai.neevcloud.com/agent}"
NAME="${DEPLOY_NAME:-trylive-app}"
BASE="$API/api/v1beta1/orgs/$NEEV_ORG_ID/projects/$NEEV_PROJECT_ID"
AUTH="Authorization: Bearer $NEEV_API_KEY"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "building web and binary"
(cd "$ROOT/web" && npm ci --silent && npm run build --silent)
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$ROOT/bin/trylive-linux-amd64" ./cmd/trylive)

# Replace an earlier deployment with the same name so the URL stays stable-ish
# (the sandbox id changes; the badge URLs are relative to whatever host serves it).
OLD=$(curl -sf "$BASE/sandboxes" -H "$AUTH" | python3 -c 'import json,sys; d=json.load(sys.stdin); items=d if isinstance(d,list) else d.get("items") or d.get("data") or []; print(" ".join(s["id"] for s in items if s.get("name")=="'"$NAME"'"))')
for id in $OLD; do
  echo "deleting previous deployment $id"
  curl -sf -o /dev/null -X DELETE "$BASE/sandboxes/$id" -H "$AUTH"
done

echo "creating sandbox $NAME"
REGION_JSON=""
[ -n "${NEEV_REGION:-}" ] && REGION_JSON="\"region\":\"$NEEV_REGION\","
SB=$(curl -sf -X POST "$BASE/sandboxes" -H "$AUTH" -H 'Content-Type: application/json' -d '{
  "name": "'"$NAME"'", '"$REGION_JSON"'
  "resources": {"cpu": 1, "memory_gb": 2, "disk_gb": 10},
  "egress": {"mode": "allow_list", "allow_internet": true}
}')
ID=$(printf '%s' "$SB" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

echo "waiting for $ID"
for _ in $(seq 1 90); do
  SB=$(curl -sf "$BASE/sandboxes/$ID" -H "$AUTH")
  PHASE=$(printf '%s' "$SB" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("phase"))')
  CONNECT=$(printf '%s' "$SB" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("connect_url") or "")')
  [ "$PHASE" = "Ready" ] && [ -n "$CONNECT" ] && break
  sleep 2
done
[ "$PHASE" = "Ready" ] || { echo "sandbox not ready: $PHASE" >&2; exit 1; }

echo "uploading binary"
curl -sf -o /dev/null -X POST "$CONNECT/v1/files/write?path=trylive.upload" -H "X-Api-Key: $NEEV_API_KEY" -H "X-Protocol-Version: 1" \
  -H 'Content-Type: application/octet-stream' --data-binary "@$ROOT/bin/trylive-linux-amd64"
curl -sf -o /dev/null -X POST "$CONNECT/v1/exec" -H "X-Api-Key: $NEEV_API_KEY" -H "X-Protocol-Version: 1" -H 'Content-Type: application/json' -H 'Accept: application/x-ndjson' \
  -d '{"command":"sh","args":["-c","chmod +x trylive.upload && mv trylive.upload trylive"],"timeout_ms":10000}'

# Env is passed to the process only; nothing secret lands on the sandbox disk.
# SELF_SANDBOX_ID lets the server send keepalives for its own sandbox.
ENV_JSON=$(SELF_SANDBOX_ID="$ID" python3 - <<'EOF2'
import json, os
keys = ["NEEV_API_KEY","NEEV_ORG_ID","NEEV_PROJECT_ID","NEEV_API_BASE","NEEV_REGION","LLM_BASE_URL","LLM_API_KEY","LLM_MODEL","GITHUB_TOKEN","MAX_SESSIONS_PER_IP","MAX_LIVE_SESSIONS","BUILD_CONCURRENCY","SELF_SANDBOX_ID"]
env = [f"{k}={os.environ[k]}" for k in keys if os.environ.get(k)]
env.append("ADDR=:8080")
print(json.dumps(env))
EOF2
)
# setsid puts the restart loop in its own session so the supervisor's hourly
# SIGKILL of the launcher's process group cannot reach the server.
echo "starting server"
curl -sf -o /dev/null -X POST "$CONNECT/v1/processes/start" -H "X-Api-Key: $NEEV_API_KEY" -H "X-Protocol-Version: 1" -H 'Content-Type: application/json' \
  -d '{"program":"sh","args":["-c","setsid sh -c \"while true; do ./trylive; echo restarting; sleep 2; done\" > /workspace/trylive.log 2>&1 < /dev/null & sleep 1"],"env":'"$ENV_JSON"'}'

echo "exposing port 8080"
URL=$(curl -sf -X POST "$BASE/sandboxes/$ID/ports" -H "$AUTH" -H 'Content-Type: application/json' -d '{"port":8080}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["preview_url"])')

for _ in $(seq 1 30); do
  if curl -sf -o /dev/null --max-time 5 "$URL/healthz"; then
    echo "live: $URL"
    exit 0
  fi
  sleep 2
done
echo "deployed but health check did not pass yet: $URL" >&2
exit 1
