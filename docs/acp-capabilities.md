# ACP Agent Capabilities Matrix

This document captures known and unknown capabilities for agent CLIs that speak ACP
(Agent Client Protocol) — a JSON-RPC 2.0 / NDJSON protocol over TCP.

Columns marked ✅ are confirmed working. ❓ means unvalidated (needs a live CLI).
❌ means explicitly not supported.

## Protocol Methods

| Capability | `copilot --acp` | `claude --acp` | `codex --acp` | `gemini --acp` |
|---|:---:|:---:|:---:|:---:|
| `initialize` | ✅ | ✅ | ✅ | ✅ |
| `session/new` | ✅ | ✅ | ✅ | ✅ |
| `session/prompt` | ✅ | ✅ | ✅ | ✅ |
| `session/cancel` | ✅ | ✅ | ✅ | ✅ |
| `session/load` | ❓ | ❓ | ❓ | ❓ |
| `streaming` (assistant.message_delta) | ✅ | ✅ | ✅ | ✅ |
| `permissionRequest` (session/request_permission) | ✅ | ❓ | ❓ | ❓ |
| `mcpServers` in session/new | ✅ | ❓ | ❓ | ❓ |
| TCP mode (`--port N`) | ✅ (`--acp --port N`) | ❓ | ❓ | ❓ |

✅ = confirmed  ❓ = needs validation  ❌ = not supported

---

## Capability Details

### `initialize`
All agent CLIs are expected to implement capability negotiation. The server returns:
- `protocolVersion` — e.g. `"2025-01-01"`
- `capabilities` — object advertising what the server supports
- `serverInfo` — name and version

### `session/new`
Creates a new conversation session. The proxy passes:
- `workDir` — the agent's working directory in the pod
- `mcpServers[]` — optional list of MCP tool servers to configure

### `session/prompt`
Sends a user prompt. The server MAY:
1. Stream `assistant.message_delta` notifications before the final response
2. Emit `session/request_permission` notifications when tool use is needed
3. Return `artifacts[]` in the final result (created/modified files)

### `session/cancel`
Cancels an in-flight `session/prompt`. The response includes `"canceled": true`.
Expected to work mid-stream (before the final response is sent).

### `session/load`
Resumes a previously created session by replaying its history via `session/update`
notifications. Only available if `capabilities.loadSession == true` in `initialize`.

**Validation needed:** Whether `copilot --acp` actually persists sessions across
process restarts, or only within the same process lifetime.

### `streaming`
`assistant.message_delta` notifications are sent during prompt processing.
Each notification contains `{"sessionId": "...", "content": "<chunk>"}`.
The proxy accumulates chunks and forwards them to the NATS stream.

### `permissionRequest`
The server may emit `session/request_permission` notifications during prompt
processing when the agent wants to execute a tool (e.g. bash, file write).
The client (proxy) should respond with an approval or denial.

**Validation needed:** The exact approval mechanism — is it a new JSON-RPC request,
or a specific notification method?

### `mcpServers`
Allows the proxy to inject MCP tool servers into a session at creation time.
The agent CLI is expected to connect to these servers over stdio.

---

## Validation Plan

When real agent CLIs are available, use the ACP harness to validate each capability:

```bash
# Validate against a real copilot CLI instance
ACP_TARGET=localhost:3000 ./bin/acp-harness --scenarios happy-path,mcp-passthrough,session-load

# Run all scenarios
./test/scripts/validate-acp.sh
```

### Priority validation items

1. **`session/load`** — Does the CLI persist sessions? What triggers history replay?
2. **`permissionRequest` approval** — Exact protocol for granting/denying tool use
3. **`mcpServers` hot reload** — Can MCP servers be added after session creation?
4. **Concurrent sessions** — Does the CLI support multiple parallel sessions?
5. **Error codes** — Are error codes standardised across CLIs?

---

## Mock Server Reference

The mock ACP server (`test/mock-acp-server/`) implements the full protocol for
CI/CD testing without requiring real agent CLIs:

```bash
# Start with default settings
./bin/mock-acp-server --port 3000

# Start with permission requests enabled
./bin/mock-acp-server --port 3000 --send-permissions

# Test error handling
./bin/mock-acp-server --port 3000 --fail-on-prompt
```

All scenarios in the ACP harness pass against the mock server.
