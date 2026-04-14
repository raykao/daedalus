#!/bin/bash
set -euo pipefail

# validate-copilot-cli.sh - End-to-end validation of the proxy against
# a real GitHub Copilot CLI running in ACP mode inside Docker.
#
# Usage:
#   export GITHUB_TOKEN="ghp_..."
#   ./test/scripts/validate-copilot-cli.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/deploy/docker/docker-compose.copilot-cli.yml"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }

# ---------------------------------------------------------------------------
# Timing helpers (nanosecond precision)
# ---------------------------------------------------------------------------
now_ns() {
    # GNU date supports %N (nanoseconds); macOS BSD date does not.
    local ts
    ts=$(date +%s%N 2>/dev/null)
    if echo "$ts" | grep -q 'N'; then
        # BSD date: fall back to second-precision with zero-padded nanoseconds
        echo "$(date +%s)000000000"
    else
        echo "$ts"
    fi
}

# elapsed_sec <start_ns> <end_ns> - prints floating-point seconds
elapsed_sec() {
    local start="$1" end="$2"
    local diff=$(( end - start ))
    # Integer division + remainder to get seconds.nanoseconds
    local secs=$(( diff / 1000000000 ))
    local nanos=$(( diff % 1000000000 ))
    printf "%d.%03d" "$secs" "$(( nanos / 1000000 ))"
}

T_START=$(now_ns)

cleanup() {
    info "Tearing down stack..."
    docker compose -f "$COMPOSE_FILE" down -v 2>/dev/null || true
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Pre-checks
# ---------------------------------------------------------------------------

if [ -z "${GITHUB_TOKEN:-}" ]; then
    error "GITHUB_TOKEN environment variable is required"
    exit 1
fi

if ! command -v docker &>/dev/null; then
    error "docker is required"
    exit 1
fi

if ! command -v nats &>/dev/null; then
    warn "nats CLI not found on host - will use docker exec for NATS operations"
    USE_DOCKER_NATS=true
else
    USE_DOCKER_NATS=false
fi

info "=== Phase 4.1: Copilot CLI ACP Validation ==="
info ""

# ---------------------------------------------------------------------------
# Step 1: Build images
# ---------------------------------------------------------------------------

info "Step 1: Building images..."
docker compose -f "$COMPOSE_FILE" build --quiet
T_BUILD=$(now_ns)

# ---------------------------------------------------------------------------
# Step 2: Start the stack
# ---------------------------------------------------------------------------

info "Step 2: Starting stack..."
docker compose -f "$COMPOSE_FILE" up -d
T_STACK_UP=$(now_ns)

# ---------------------------------------------------------------------------
# Step 3: Wait for all services to be healthy
# ---------------------------------------------------------------------------

info "Step 3: Waiting for services to be healthy..."
TIMEOUT=120
ELAPSED=0
while [ $ELAPSED -lt $TIMEOUT ]; do
    HEALTHY=$(docker compose -f "$COMPOSE_FILE" ps --format json 2>/dev/null | \
        python3 -c "
import sys, json
services = [json.loads(line) for line in sys.stdin if line.strip()]
healthy = sum(1 for s in services if s.get('Health','') == 'healthy' or s.get('State','') == 'running')
print(healthy)
" 2>/dev/null || echo "0")

    if [ "$HEALTHY" -ge 3 ]; then
        info "All services healthy after ${ELAPSED}s"
        T_HEALTHY=$(now_ns)
        break
    fi
    sleep 2
    ELAPSED=$((ELAPSED + 2))
done

if [ $ELAPSED -ge $TIMEOUT ]; then
    error "Services did not become healthy within ${TIMEOUT}s"
    docker compose -f "$COMPOSE_FILE" ps
    docker compose -f "$COMPOSE_FILE" logs copilot-cli --tail=50
    exit 1
fi

# If we timed out the loop but didn't break, T_HEALTHY may not be set.
T_HEALTHY=${T_HEALTHY:-$(now_ns)}

# ---------------------------------------------------------------------------
# Step 4: Create NATS JetStream streams
# ---------------------------------------------------------------------------

info "Step 4: Creating NATS streams..."

nats_exec() {
    docker compose -f "$COMPOSE_FILE" exec -T nats-box \
        nats --server nats://nats:4222 "$@"
}

add_stream() {
    local name="$1"
    local subjects="$2"
    nats_exec stream add "$name" \
        --subjects="$subjects" \
        --retention=limits --max-msgs=-1 --max-bytes=-1 \
        --max-age=1h --storage=memory --replicas=1 --discard=old \
        --no-allow-rollup --dupe-window=2m 2>/dev/null \
    || info "Stream $name may already exist"
}

add_stream AGENT_TASKS   "agent.tasks.>"
add_stream AGENT_RESULTS "agent.results.>"
add_stream AGENT_STATUS  "agent.status.>"
T_STREAMS=$(now_ns)

# ---------------------------------------------------------------------------
# Step 5: Publish a test task
# ---------------------------------------------------------------------------

TASK_ID="validation-$(date +%s)"
info "Step 5: Publishing test task: $TASK_ID"

TASK_JSON=$(cat <<EOF
{
  "message": {
    "messageId": "${TASK_ID}",
    "taskId": "${TASK_ID}",
    "role": "user",
    "parts": [
      {"text": "Create a file called hello.txt with the contents 'Hello from Daedalus validation'. Do not create any other files."}
    ]
  }
}
EOF
)

if [ "$USE_DOCKER_NATS" = true ]; then
    echo "$TASK_JSON" | nats_exec pub "agent.tasks.${TASK_ID}" --stdin
else
    echo "$TASK_JSON" | nats pub "agent.tasks.${TASK_ID}" --stdin \
        --server nats://localhost:4222
fi
T_PUBLISH=$(now_ns)

# ---------------------------------------------------------------------------
# Step 6: Wait for result
# ---------------------------------------------------------------------------

RESULT_TIMEOUT=120
info "Step 6: Waiting for result (timeout: ${RESULT_TIMEOUT}s)..."
if [ "$USE_DOCKER_NATS" = true ]; then
    RESULT=$(nats_exec sub "agent.results.${TASK_ID}" \
        --stream=AGENT_RESULTS --count=1 --timeout="${RESULT_TIMEOUT}s" --raw 2>/dev/null || true)
else
    RESULT=$(nats sub "agent.results.${TASK_ID}" \
        --server nats://localhost:4222 \
        --stream=AGENT_RESULTS --count=1 --timeout="${RESULT_TIMEOUT}s" --raw 2>/dev/null || true)
fi

# ---------------------------------------------------------------------------
# Step 7: Validate result
# ---------------------------------------------------------------------------

info "Step 7: Validating result..."

if [ -z "$RESULT" ]; then
    error "No result received within ${RESULT_TIMEOUT}s"
    info "Checking proxy logs..."
    docker compose -f "$COMPOSE_FILE" logs proxy --tail=30
    info "Checking copilot-cli logs..."
    docker compose -f "$COMPOSE_FILE" logs copilot-cli --tail=30
    exit 1
fi

T_RESULT=$(now_ns)

# Parse result JSON
STATE=$(echo "$RESULT" | python3 -c \
    "import sys,json; print(json.load(sys.stdin).get('status',{}).get('state','unknown'))" \
    2>/dev/null || echo "parse_error")

# ---------------------------------------------------------------------------
# Latency Summary
# ---------------------------------------------------------------------------

print_latency_summary() {
    info ""
    info "=== Latency Summary ==="
    info "Image build:        $(elapsed_sec "$T_START" "$T_BUILD")s"
    info "Stack startup:      $(elapsed_sec "$T_BUILD" "$T_STACK_UP")s"
    info "Health ready:       $(elapsed_sec "$T_STACK_UP" "$T_HEALTHY")s"
    info "Stream creation:    $(elapsed_sec "$T_HEALTHY" "$T_STREAMS")s"
    info "Task round-trip:    $(elapsed_sec "$T_PUBLISH" "$T_RESULT")s  (publish -> result)"
    info "Total wall time:    $(elapsed_sec "$T_START" "$T_RESULT")s"
    info ""
}

if [ "$STATE" = "completed" ]; then
    info ""
    info "=========================================="
    info "  VALIDATION PASSED"
    info "  Task $TASK_ID completed successfully"
    info "  Copilot CLI ACP mode is working!"
    info "=========================================="
    info ""
    info "Result:"
    echo "$RESULT" | python3 -m json.tool 2>/dev/null || echo "$RESULT"
    print_latency_summary
    exit 0
elif [ "$STATE" = "failed" ]; then
    warn "Task completed with FAILED state"
    echo "$RESULT" | python3 -m json.tool 2>/dev/null || echo "$RESULT"
    print_latency_summary
    exit 1
else
    error "Unexpected task state: $STATE"
    echo "$RESULT" | python3 -m json.tool 2>/dev/null || echo "$RESULT"
    print_latency_summary
    exit 1
fi
