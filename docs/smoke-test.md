# End-to-End Smoke Test Guide

## Overview

The smoke test validates the full Daedalus pipeline end-to-end:

```
NATS queue (A2A SendMessageRequest)
  -> Proxy (queue consumer)
    -> ACP Client (TCP JSON-RPC 2.0)
      -> Copilot CLI (GitHub Copilot in ACP mode)
        -> Result back through NATS
```

This exercises every component in the stack with a real GitHub Copilot CLI,
not a mock. It verifies that prompts flow through the entire pipeline and
produce valid A2A Task results.

## Prerequisites

- **Docker and Docker Compose** (v2+ with `docker compose` syntax)
- **Go 1.25+** (for the Go smoke test)
- **`GITHUB_TOKEN`** with GitHub Copilot access (PAT or fine-grained token)
- **Available ports:**
  - `4222` - NATS client connections
  - `8222` - NATS monitoring HTTP
  - `3000` - Copilot CLI ACP listener

## Quick Start

### Using the validation script (bash)

The bash script is a standalone validator with timing instrumentation:

```bash
export GITHUB_TOKEN=ghp_your_token_here
./test/scripts/validate-copilot-cli.sh
```

This builds images, starts the stack, creates NATS streams, publishes a test
task, waits for the result, and prints a latency summary table.

### Using the Go smoke test

The Go test provides structured assertions, parallel-safe task IDs, and
detailed latency measurement:

```bash
export GITHUB_TOKEN=ghp_your_token_here
make test-smoke
```

Or directly with `go test`:

```bash
export GITHUB_TOKEN=ghp_your_token_here
go test ./test/integration/... -tags=smoke -v -count=1 -timeout=600s
```

### Using the Go integration test (mock ACP, no token needed)

For faster iteration without a real Copilot CLI:

```bash
make test-integration
```

This uses the mock ACP server and the standard `docker-compose.yml` stack.

## What Gets Tested

The smoke test validates each stage of the pipeline:

1. **Proxy connects to Copilot CLI via ACP** (TCP JSON-RPC 2.0 on port 3000)
2. **ACP capability negotiation** (`initialize` handshake)
3. **Session creation** (`session/new`)
4. **Prompt delivery and streaming response** (`session/prompt`)
5. **Result published to NATS** with correct A2A Task format
6. **Status transitions**: `working` -> `completed`
7. **Task ID propagation** through the full pipeline (publish -> status -> result)

### Go Test Cases

| Test | What It Validates |
|------|-------------------|
| `TestSmoke_EndToEnd` | Full round-trip: publish task, receive result, verify completed state and non-empty artifacts. Logs latency at each step. |
| `TestSmoke_StatusTransitions` | Correct state machine ordering: `working` precedes `completed`. Captures error messages on failure. |
| `TestSmoke_TaskIDPropagation` | Task ID flows correctly through NATS subjects, status updates, and result payload. |

## Expected Latencies

| Phase | Expected Range | Notes |
|-------|---------------|-------|
| Stack startup | 10-30s | Image pulls on first run |
| Service health | 15-45s | Copilot CLI ACP listener startup |
| Task round-trip | 10-60s | Depends on prompt complexity and model |
| Total wall time | 60-180s | First run slower due to image builds |

The validation script prints a detailed timing breakdown:

```
=== Latency Summary ===
Image build:        12.345s
Stack startup:      3.210s
Health ready:       28.456s
Stream creation:    1.234s
Task round-trip:    15.678s  (publish -> result)
Total wall time:    60.923s
```

The Go test logs per-step latency:

```
=== Smoke Test Latency ===
Publish -> First Status:  1234ms
First Status -> Complete: 14567ms
Total Round-Trip:         15801ms
Status Transitions: [working completed]
```

## Interpreting Results

### Success

- All tests pass
- Status transitions show: `working` -> `completed`
- Artifacts contain non-empty response text
- Latency summary shows reasonable times (see table above)

### Common Failures

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| "GITHUB_TOKEN must be set" | Missing or invalid token | Export a valid PAT with Copilot scope |
| Timeout waiting for health | Copilot CLI failed to start ACP listener | Check `docker compose logs copilot-cli` |
| Task state: failed | ACP protocol error or CLI crash | Check proxy logs: `docker compose logs proxy` |
| Connection refused on 4222 | NATS not started | Check `docker compose logs nats` |
| "no status updates received" | Proxy not publishing status | Verify proxy connects: `docker compose logs proxy` |
| Task ID mismatch | Proxy not propagating ID | Check proxy task handling logic |

## Architecture

```
+-------+     +-------------+     +---------+     +-------------+
| NATS  | --> |   Proxy     | --> |  ACP    | --> | Copilot CLI |
| Queue |     | (consumer)  |     | Client  |     | (ACP mode)  |
+-------+     +-------------+     +---------+     +-------------+
    ^                                                    |
    |               JSON-RPC 2.0 over TCP               |
    +----------------------------------------------------+
    |         Results + Status published to NATS          |
```

**NATS JetStream Subjects:**
- `agent.tasks.>` (AGENT_TASKS stream) - incoming task requests
- `agent.results.>` (AGENT_RESULTS stream) - completed task results
- `agent.status.>` (AGENT_STATUS stream) - status updates (working, completed, failed)

**Docker Compose Services:**
- `nats` - NATS server with JetStream enabled
- `nats-box` - Utility container for NATS CLI operations
- `copilot-cli` - GitHub Copilot CLI running in ACP mode
- `proxy` - Daedalus proxy bridging NATS to ACP

## Debugging

### View all container logs

```bash
docker compose -f deploy/docker/docker-compose.copilot-cli.yml logs
```

### View specific service logs

```bash
docker compose -f deploy/docker/docker-compose.copilot-cli.yml logs proxy --tail=50
docker compose -f deploy/docker/docker-compose.copilot-cli.yml logs copilot-cli --tail=50
docker compose -f deploy/docker/docker-compose.copilot-cli.yml logs nats --tail=50
```

### NATS monitoring

Open `http://localhost:8222` in a browser for the NATS monitoring dashboard.

Useful endpoints:
- `http://localhost:8222/jsz` - JetStream information
- `http://localhost:8222/connz` - Connection details
- `http://localhost:8222/subsz` - Subscription details

### Check NATS streams

```bash
docker compose -f deploy/docker/docker-compose.copilot-cli.yml exec nats-box \
  nats --server nats://nats:4222 stream ls
```

### Check stream contents

```bash
docker compose -f deploy/docker/docker-compose.copilot-cli.yml exec nats-box \
  nats --server nats://nats:4222 stream view AGENT_RESULTS
```

### Manual stack management

```bash
# Start stack
docker compose -f deploy/docker/docker-compose.copilot-cli.yml up -d

# Check service status
docker compose -f deploy/docker/docker-compose.copilot-cli.yml ps

# Tear down (including volumes)
docker compose -f deploy/docker/docker-compose.copilot-cli.yml down -v
```
