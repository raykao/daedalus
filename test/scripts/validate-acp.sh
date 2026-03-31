#!/usr/bin/env bash
# validate-acp.sh — Start the mock ACP server, run the harness, report results.
# Exit code 0 if all scenarios pass, 1 if any fail.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
MODULE_ROOT="${SCRIPT_DIR}/../.."

# ── Resolve binaries ───────────────────────────────────────────────────────────
MOCK_BIN="${MODULE_ROOT}/bin/mock-acp-server"
HARNESS_BIN="${MODULE_ROOT}/bin/acp-harness"

build_binaries() {
  echo "Building mock ACP server..."
  (cd "${MODULE_ROOT}" && go build -o "${MOCK_BIN}" ./test/mock-acp-server/)
  echo "Building ACP harness..."
  (cd "${MODULE_ROOT}" && go build -o "${HARNESS_BIN}" ./test/acp-harness/)
}

if [[ ! -x "${MOCK_BIN}" ]] || [[ ! -x "${HARNESS_BIN}" ]]; then
  build_binaries
fi

# ── Pick a random free port ────────────────────────────────────────────────────
# Bind to port 0 and read what the OS assigned, then release it.
get_free_port() {
  python3 -c "
import socket
s = socket.socket()
s.bind(('', 0))
print(s.getsockname()[1])
s.close()
"
}

PORT=$(get_free_port)
TARGET="localhost:${PORT}"

# ── Start mock server ──────────────────────────────────────────────────────────
echo "Starting mock ACP server on port ${PORT}..."
"${MOCK_BIN}" --port "${PORT}" --streaming-delay 20ms &
MOCK_PID=$!
trap 'kill "${MOCK_PID}" 2>/dev/null; wait "${MOCK_PID}" 2>/dev/null; exit' INT TERM EXIT

# ── Wait for server to be ready (TCP connect check) ───────────────────────────
MAX_WAIT=10
WAITED=0
until nc -z localhost "${PORT}" 2>/dev/null; do
  if [[ ${WAITED} -ge ${MAX_WAIT} ]]; then
    echo "ERROR: mock server did not start within ${MAX_WAIT}s" >&2
    exit 1
  fi
  sleep 0.2
  WAITED=$((WAITED + 1))
done
echo "Mock server ready."

# ── Run harness ────────────────────────────────────────────────────────────────
HARNESS_ARGS=(--target "${TARGET}" --timeout 30s)
if [[ "${VERBOSE:-}" == "1" ]]; then
  HARNESS_ARGS+=(--verbose)
fi
if [[ -n "${SCENARIOS:-}" ]]; then
  HARNESS_ARGS+=(--scenarios "${SCENARIOS}")
fi

"${HARNESS_BIN}" "${HARNESS_ARGS[@]}"
EXIT_CODE=$?

# ── Cleanup ────────────────────────────────────────────────────────────────────
kill "${MOCK_PID}" 2>/dev/null
wait "${MOCK_PID}" 2>/dev/null || true
trap - INT TERM EXIT

exit ${EXIT_CODE}
