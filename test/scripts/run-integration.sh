#!/usr/bin/env bash
# run-integration.sh - Build, start, test, and tear down the Docker Compose
# integration stack for daedalus.
#
# Usage:
#   ./test/scripts/run-integration.sh [--no-build] [--no-teardown]
#
# Flags:
#   --no-build     Skip image build step (use cached images)
#   --no-teardown  Leave stack running after tests (useful for debugging)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/docker/docker-compose.yml"
INTEGRATION_PKG="./test/integration/"

BUILD=true
TEARDOWN=true

for arg in "$@"; do
  case "$arg" in
    --no-build)    BUILD=false ;;
    --no-teardown) TEARDOWN=false ;;
    *) echo "Unknown flag: $arg" >&2; exit 1 ;;
  esac
done

cd "${REPO_ROOT}"

cleanup() {
  if [[ "${TEARDOWN}" == "true" ]]; then
    echo ""
    echo "--- Tearing down stack ---"
    docker compose -f "${COMPOSE_FILE}" down -v || true
  else
    echo ""
    echo "--- Stack left running (--no-teardown) ---"
    docker compose -f "${COMPOSE_FILE}" ps
  fi
}
trap cleanup EXIT

echo "=== daedalus integration test ==="
echo "Repo root:    ${REPO_ROOT}"
echo "Compose file: ${COMPOSE_FILE}"
echo ""

# Build images.
if [[ "${BUILD}" == "true" ]]; then
  echo "--- Building images ---"
  docker compose -f "${COMPOSE_FILE}" build
  echo ""
fi

# Start the stack.
echo "--- Starting stack ---"
docker compose -f "${COMPOSE_FILE}" up -d
echo ""

# Wait for NATS monitoring endpoint to be healthy.
echo "--- Waiting for NATS to be healthy ---"
NATS_READY=false
for i in $(seq 1 30); do
  if curl -sf http://localhost:8222/healthz > /dev/null 2>&1; then
    NATS_READY=true
    break
  fi
  printf "  attempt %d/30...\r" "$i"
  sleep 2
done
echo ""

if [[ "${NATS_READY}" == "false" ]]; then
  echo "ERROR: NATS monitoring endpoint not ready after 60s" >&2
  docker compose -f "${COMPOSE_FILE}" logs
  exit 1
fi
echo "NATS is healthy."
echo ""

# Show stack status.
echo "--- Stack status ---"
docker compose -f "${COMPOSE_FILE}" ps
echo ""

# Run integration tests.
echo "--- Running integration tests ---"
START_TS=$(date +%s)

go test \
  -tags integration \
  -v \
  -count=1 \
  -timeout 120s \
  "${INTEGRATION_PKG}"

END_TS=$(date +%s)
ELAPSED=$(( END_TS - START_TS ))
echo ""
echo "--- Integration tests completed in ${ELAPSED}s ---"
