# Integration Testing

This document explains how to run the Docker Compose integration test for agent-forge. The test validates the complete end-to-end loop: publish an A2A task to NATS, the proxy dequeues it, drives the mock ACP agent, and the result flows back through NATS.

## Prerequisites

- Docker with Compose plugin (`docker compose version`)
- Go 1.24+
- `curl` (used by the run script for health checks)

## Running the Test

From the repository root:

```bash
./test/scripts/run-integration.sh
```

This script:
1. Builds all Docker images (`docker compose build`)
2. Starts the stack (`docker compose up -d`)
3. Waits for NATS to pass its health check
4. Runs `go test -tags integration ./test/integration/`
5. Tears down the stack and volumes (`docker compose down -v`)

### Options

```
--no-build     Skip image rebuild (use cached images)
--no-teardown  Leave the stack running after tests (useful for debugging)
```

### Running the Test Directly

If you already have the stack running:

```bash
go test -tags integration -v -count=1 -timeout 120s ./test/integration/
```

## What the Test Validates

`TestEndToEnd_CompletedTask` in `test/integration/compose_test.go`:

1. **JetStream streams**: Creates `AGENT_RESULTS` and `AGENT_STATUS` streams so the proxy can publish results and status updates.
2. **Task publish**: Publishes a `SendMessageRequest` to `agent.tasks.test-task-1`.
3. **Result assertion**: Waits up to 60 seconds for the proxy to process the task and publish an `a2a.Task` to `agent.results.test-task-1`. Asserts:
   - `task.Status.State == "completed"`
   - At least one artifact with non-empty text
   - `task.ID == "test-task-1"`
4. **Status progression**: Subscribes to `agent.status.test-task-1` and asserts that both a `"working"` and a `"completed"` status update were received.
5. **Latency**: Measures and logs the time from publish to result received.

### Build tag

The test uses `//go:build integration` so it is excluded from `go test ./...` and only runs when `-tags integration` is passed.

## Stack Architecture

```
[test] --> NATS (agent.tasks.test-task-1)
                        |
                     [proxy]
                        |
                  [mock-acp-server] (TCP :3000)
                        |
            [proxy publishes result]
                        |
           NATS (agent.results.test-task-1)
                        |
                     [test] asserts
```

## Expected Output

A passing run looks like:

```
--- PASS: TestEndToEnd_CompletedTask (3.21s)
    compose_test.go:135: task published: subject=agent.tasks.test-task-1
    compose_test.go:160: status update received: state=working
    compose_test.go:160: status update received: state=completed
    compose_test.go:172: end-to-end latency: 3.18s
    compose_test.go:220: PASS: task test-task-1 completed in 3.18s (status updates: [working completed])
```

## Expected Latency

| Component | Typical Range |
|-----------|---------------|
| NATS enqueue + dequeue | < 50ms |
| Proxy ACP round-trip (mock) | 500ms - 2s (mock streaming delay) |
| Total end-to-end | 1s - 5s |

Latency above 15 seconds usually indicates a startup race condition - check `docker compose logs proxy` for connection errors.

## Troubleshooting

**Proxy exits immediately**: Check `docker compose logs proxy`. The proxy requires NATS and mock-acp to be healthy before it starts. Increase `start_period` in `docker-compose.yml` if needed.

**Test times out waiting for result**: The mock ACP server may not be responding. Run `docker compose logs mock-acp` to check for errors.

**Stream conflict errors**: If a previous test run left streams with incompatible configurations, run `docker compose down -v` to remove NATS volumes and start clean.
