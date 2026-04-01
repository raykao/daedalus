# Agent Forge Runtime Contract v1

## Overview

The Agent Forge Runtime Contract defines the interface between the **platform layer** (proxy sidecar) and the **user layer** (agent runtime container). Any container that implements this contract can be deployed as an agent worker in Agent Forge, regardless of language, framework, or AI provider.

This contract enables pluggable runtimes: swap the agent container without changing the platform. The proxy sidecar handles NATS consumption, queue management, health monitoring, and trace propagation. The agent runtime only needs to be an A2A-compliant HTTP server.

## Architecture Context

Agent Forge uses a two-container pod model:

### Platform Layer (Proxy Sidecar)

Managed by Agent Forge. The proxy:

- Consumes tasks from NATS JetStream queue (`agent.tasks.<name>`)
- Forwards each task as an A2A `SendMessageRequest` to the agent runtime via `localhost:$A2A_PORT`
- Publishes the resulting `Task` back to NATS
- Monitors agent health via `GET /health`
- Propagates W3C Traceparent headers for distributed tracing
- Handles SIGTERM graceful shutdown coordination

### User Layer (Agent Runtime)

Provided by the user. Any container that:

- Listens on `$A2A_PORT` (default `8080`)
- Serves the three required HTTP endpoints (below)
- Returns valid A2A protocol responses

## A2A Server Interface

### Required Endpoints

| Endpoint | Method | Content-Type | Description |
|----------|--------|--------------|-------------|
| `/.well-known/agent-card.json` | GET | `application/json` | AgentCard describing capabilities |
| `/` | POST | `application/json` | Accept `SendMessageRequest`, return `Task` |
| `/health` | GET | `application/json` | Liveness/readiness probe |

### POST / — Task Execution

The primary endpoint. The proxy forwards one task at a time.

**Request:** A2A `SendMessageRequest` conforming to `send-message-request.schema.json`.

```json
{
  "message": {
    "messageId": "msg-abc123",
    "role": "user",
    "parts": [{ "text": "Implement feature X" }],
    "taskId": "task-001"
  }
}
```

**Response:** A2A `Task` conforming to `task.schema.json`.

| Field | Requirement |
|-------|-------------|
| `id` | Must match the request's `message.taskId` if present, otherwise `message.messageId` |
| `status.state` | One of: `completed`, `failed`, `input-required` |
| `artifacts[]` | Required for `completed` tasks — the agent's output |
| `status.message` | Required for `failed` tasks — error description |

**Response codes:**

| Code | Meaning |
|------|---------|
| 200 | Task processed (check `status.state` for outcome) |
| 400 | Malformed request body (invalid JSON) |
| 415 | Unsupported Content-Type (must be `application/json`) |
| 503 | Server shutting down or not ready |

### GET /.well-known/agent-card.json — Discovery

Returns an `AgentCard` conforming to `agent-card.schema.json`.

Requirements:
- Must include at least one skill with `id`, `name`, `description`, and `tags`
- Must be available before the first task is processed
- Should be cacheable (the proxy reads it once at startup)

```json
{
  "name": "my-agent",
  "description": "Does useful work",
  "version": "1.0.0",
  "defaultInputModes": ["text"],
  "defaultOutputModes": ["text"],
  "skills": [{
    "id": "code-review",
    "name": "Code Review",
    "description": "Reviews pull requests",
    "tags": ["code", "review"]
  }],
  "capabilities": {
    "streaming": false
  }
}
```

### GET /health — Health Check

The proxy polls this endpoint to determine readiness.

**Response when ready:**
```json
{"status": "ok"}
```
HTTP 200.

**Response when not ready:**
HTTP 503 (any body).

The agent must become healthy within `STARTUP_TIMEOUT` (default 60s). If the health check does not return 200 within the timeout, the proxy marks the pod as failed.

### Optional: Streaming (SSE)

If the agent supports streaming, `POST /` with `Accept: text/event-stream` returns Server-Sent Events:

- `TaskStatusUpdateEvent` — state transitions (`working` → `completed`)
- `TaskArtifactUpdateEvent` — streaming artifact parts

If supported, set `capabilities.streaming: true` in the AgentCard.

Streaming is optional. The proxy falls back to synchronous request/response when streaming is not advertised.

## Container Interface

### Ports

- Listen on `$A2A_PORT` (default: `8080`)
- The proxy connects to `localhost:$A2A_PORT` within the same pod network namespace

### Environment Variables

**Required:**

| Variable | Default | Description |
|----------|---------|-------------|
| `A2A_PORT` | `8080` | Port for the A2A HTTP server |
| `WORK_DIR` | `/workspace` | Writable workspace directory |

**Optional:**

| Variable | Default | Description |
|----------|---------|-------------|
| `GITHUB_TOKEN` | *(none)* | GitHub authentication token |
| `TERMINATION_GRACE_PERIOD` | `30s` | Time allowed for in-flight requests during shutdown |
| `STARTUP_TIMEOUT` | `60s` | Maximum time for health check to succeed |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | *(none)* | OpenTelemetry collector endpoint |

### Volume Mounts

| Path | Access | Description |
|------|--------|-------------|
| `/workspace` (or `$WORK_DIR`) | Read-write | Workspace for agent operations (cloning repos, writing output) |

### Signal Handling (SIGTERM)

When the pod is terminated, the proxy sends SIGTERM to the agent container. The agent must:

1. **Stop accepting new requests** — return 503 for any new `POST /` requests
2. **Complete in-flight requests** — finish current work within `TERMINATION_GRACE_PERIOD`
3. **Cancel remaining work** — if the grace period expires, cancel any still-running operations
4. **Exit cleanly** — exit with code 0

The proxy coordinates shutdown: it stops pulling from NATS first, then waits for the agent to finish.

## Conformance Levels

### Level 1: Minimal (required)

All runtimes must implement Level 1 to be deployable.

- [ ] `GET /health` returns 200 with `{"status": "ok"}`
- [ ] `GET /.well-known/agent-card.json` returns a valid AgentCard
- [ ] `POST /` accepts `SendMessageRequest` and returns a valid `Task`
- [ ] Task `id` matches request's `taskId` or `messageId`
- [ ] Completed tasks include at least one artifact
- [ ] Failed tasks include `status.message`

### Level 2: Production (recommended)

For production workloads, Level 2 adds operational requirements.

- [ ] All Level 1 requirements
- [ ] SIGTERM graceful shutdown within `TERMINATION_GRACE_PERIOD`
- [ ] Structured JSON logging to stdout
- [ ] OpenTelemetry trace context propagation (W3C `Traceparent` header)
- [ ] `POST /` returns 503 during shutdown

### Level 3: Full (optional)

For advanced use cases with real-time output.

- [ ] All Level 2 requirements
- [ ] SSE streaming support (`Accept: text/event-stream`)
- [ ] `capabilities.streaming: true` in AgentCard
- [ ] Context compaction reporting via metadata
- [ ] Session resume support (maintain state across tasks with same `contextId`)

## Contract Manifest

Agent runtimes may ship a `runtime-contract.json` manifest declaring their conformance level and capabilities. See `runtime-contract.schema.json` for the schema.

```json
{
  "contractVersion": "v1",
  "runtime": {
    "name": "my-agent",
    "version": "1.0.0",
    "description": "My custom agent runtime"
  },
  "server": {
    "port": 8080,
    "healthPath": "/health",
    "agentCardPath": "/.well-known/agent-card.json",
    "taskPath": "/"
  },
  "capabilities": {
    "streaming": false,
    "traceContext": true,
    "gracefulShutdown": true,
    "contextCompaction": false
  },
  "conformanceLevel": 2
}
```

## Reference Implementations

| Runtime | Level | Notes |
|---------|-------|-------|
| copilot-bridge (via proxy) | Level 2 | Default runtime, ACP-backed |
| reference-server (test fixture) | Level 1 | Minimal Go server for conformance testing |

## Versioning

This is **v1** of the runtime contract.

- **Major version bump** (v2): breaking changes to required endpoints, request/response formats, or container interface
- **Minor version bump** (v1.1): new optional headers, endpoints, or capabilities
- **Contract version** is declared in the manifest's `contractVersion` field

The proxy sidecar advertises the contract versions it supports. Agent runtimes declare the version they implement. The proxy rejects runtimes with incompatible major versions.
