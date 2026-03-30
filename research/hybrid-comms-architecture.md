# Hybrid Communication Architecture: A2A Protocol, Message Queues, and Kubernetes Operator Design

## Summary

This paper addresses the communication architecture for the Dark Software Factory - specifically, how the orchestrator bridge dispatches work to ephemeral worker agents and how those agents report results. It evaluates three communication models: **pure message queue** (NATS JetStream), **pure A2A protocol** (Google's Agent2Agent), and a **hybrid** that uses A2A's data model as structured message envelopes transported via queue. The hybrid is recommended.

The paper also resolves two architectural gaps left open by prior research:

1. **The runtime harness problem**: `.agent.md` files are inert configuration - they need a runtime to execute. We evaluate three harness options (bare CLI, minimal bridge, full bridge) and recommend **minimal bridge instances** as headless queue consumers, each configured for a single specialization.

2. **Kubernetes Operator design**: A custom operator managing `AgentRuntime`, `MCPServer`, `AgentCard`, and `TaskPipeline` CRDs can declaratively describe the factory, bridging the gap between the existing container research (which describes *what* runs) and production operations (which describes *how it's managed*).

The resulting architecture is a **multi-bridge team model** - each bridge instance is a specialized team reading from a dedicated queue subject, with one gateway bridge connected to Mattermost for human interaction and all workers running headless.

This builds directly on four prior research papers:
- [Dark Software Factory Architecture](dark-software-factory-architecture.md) - vision, manufacturing analogy, phased roadmap
- [Containerizing Agent Runtimes](containerizing-agent-runtimes.md) - three runtime models, hybrid recommendation
- [Inter-Agent Task Handoff](inter-agent-task-handoff.md) - `delegate_task` proposal, A2A pattern analysis
- [Message Broker Selection](message-broker-selection.md) - NATS JetStream recommendation

---

## Context & Motivation

The [containerizing agent runtimes](containerizing-agent-runtimes.md) research established that the **hybrid model** - copilot-bridge as a persistent orchestrator with Copilot CLI containers as disposable workers - is the recommended architecture. The [message broker research](message-broker-selection.md) selected NATS JetStream as the dispatch layer. The [inter-agent handoff research](inter-agent-task-handoff.md) designed the `delegate_task` / `check_task` tools.

But several questions remain unresolved:

1. **Queue vs A2A**: The task handoff research identified Google's A2A protocol as a reference but deferred adoption ("NOT implementing full A2A protocol in v1"). Now that A2A has reached v1.0.0 with Go, Python, JS, Java, and .NET SDKs, should it replace the queue, complement it, or be ignored?

2. **Message schema**: Queues need structured payloads. A2A defines a rich data model (Task, Message, Part, Artifact, AgentCard). Rolling a custom schema duplicates effort. Can A2A's data model be used as the queue message format?

3. **Agent discovery**: The existing architecture hardcodes routing ("devops queue", "frontend queue"). A2A's AgentCard provides capability-based discovery. How does this work in a queue-based architecture?

4. **Runtime harness**: The containerizing research identified three runtime models but didn't resolve how `.agent.md` files - which are just markdown with YAML frontmatter - actually execute inside containers. What's the minimal runtime?

5. **Kubernetes Operator**: The existing research describes Pod specs and KEDA ScaledJobs as individual resources. A Kubernetes Operator with CRDs would provide declarative, reconciliation-loop-based lifecycle management. What CRDs are needed?

6. **Mattermost connectivity**: In a multi-bridge world, how many bridges connect to chat? Does every worker need a Mattermost connection, or just the orchestrator?

---

## Knowns

### A2A Protocol (v1.0.0)

- A2A is an open protocol (Linux Foundation, Apache 2.0) for inter-agent communication. It uses **JSON-RPC 2.0 over HTTP(S)** with SSE for streaming and webhooks for push notifications.
- Core operations: `message/send`, `message/stream`, `tasks/get`, `tasks/list`, `tasks/cancel`.
- Data model: **Task** (lifecycle state machine), **Message** (user/agent role, contains Parts), **Part** (text, file reference, or structured data), **Artifact** (named output from task), **AgentCard** (capability advertisement for discovery).
- Task states: `submitted`, `working`, `input-required`, `completed`, `failed`, `canceled`, `rejected`.
- SDKs exist in Go, Python, JS, Java, and .NET.
- A2A is designed for **opaque agents** - they collaborate without sharing internal state, memory, or tools.
- A2A complements MCP: MCP provides tool access (agent-to-tool), A2A provides agent collaboration (agent-to-agent).
- AgentCards are published at `/.well-known/agent.json` and describe skills, supported input/output modes, authentication requirements, and connection endpoints.
- The spec is defined via Protocol Buffers (`spec/a2a.proto`) with JSON-RPC, gRPC, and HTTP/REST bindings.

### copilot-bridge Architecture

- copilot-bridge is a Node.js application wrapping the Copilot CLI with chat platform adapters (Mattermost/Slack), session management, MCP server orchestration, hooks, scheduling, skills, and inter-agent communication.
- It reads agent definitions from `AGENTS.md` and `.github/agents/*.agent.md`.
- Current inter-agent communication is via `ask_agent` (synchronous, 300s max timeout).
- The bridge connects to Mattermost via WebSocket (`@mattermost/client` library) with bot token auth.
- Configuration lives in `config.json`; state in SQLite (`state.db`).

### NATS JetStream (from broker research)

- Recommended self-hosted OSS broker for task dispatch.
- `ack_wait` provides message lock semantics (10 min per agent task).
- `In-Progress` ack extends the lease during long-running tasks.
- `max_deliver` + dead-letter subjects for DLQ.
- Native KEDA ScaledJob support.
- Lightweight: 100-300 MB/node, single Go binary.

### Kubernetes Primitives

- KEDA **ScaledJob** creates one-shot Kubernetes Jobs per queue message - correct primitive for ephemeral workers.
- KEDA **ScaledObject** scales long-running Deployments/StatefulSets - correct primitive for the orchestrator bridge.
- **Custom Resource Definitions (CRDs)** + controllers provide the operator pattern: declare desired state, controller reconciles.
- KEDA operator itself is ~30-50 MiB per pod.

---

## Unknowns

1. **A2A over non-HTTP transport**: A2A v1.0.0 specifies JSON-RPC over HTTP(S) and gRPC. Can the data model be used over queue transport without violating the spec? The spec doesn't prohibit it - it's a data model + operations + bindings layered architecture - but no SDK implements a queue binding.

2. **AgentCard without HTTP**: A2A discovery assumes `/.well-known/agent.json` served over HTTP. In a queue-based architecture, workers may not expose HTTP endpoints (headless pods with no ingress). Alternative discovery mechanisms (Kubernetes CRD, config map, in-queue registration) need design.

3. **Multi-bridge session isolation**: If multiple copilot-bridge instances run concurrently, each with its own `state.db`, how are sessions isolated? Can two bridge instances safely operate on the same repo concurrently? Git branch naming conventions provide some isolation, but SQLite state is per-instance.

4. **Bridge headless mode**: copilot-bridge today always connects to a chat platform. Can it run in "headless" mode - reading tasks from a queue, executing, and publishing results - without a Mattermost/Slack connection? This likely requires upstream changes or a queue adapter.

5. **Operator reconciliation complexity**: How complex is a Kubernetes operator that manages KEDA ScaledJobs, NATS subjects, MCP server deployments, and bridge instances? What's the blast radius of a reconciliation bug?

6. **Cost of concurrent bridge instances**: Each bridge instance runs the Copilot CLI, which requires a Copilot subscription. Rate limits, token costs, and concurrent session limits under a single GitHub org are unmeasured at scale (10+ concurrent agents).

7. **Session-store.db portability across CLI versions**: The Copilot CLI's internal SQLite schema (`sessions`, `turns`, `checkpoints`, `session_files`, `session_refs`, `search_index`) may change between CLI versions. Restoring an archive created by CLI v0.0.421 into a container running v0.0.425 may fail if migrations are not backward-compatible.

8. **Events.jsonl replay fidelity**: When restoring a session, does `client.resumeSession()` re-read `events.jsonl`, or does it rely solely on `session-store.db`? If the event stream is only used for observability (not session state), archiving it is optional. If it's required for resume, it's critical.

---

## Gaps

1. **No A2A server mode for copilot-bridge** - The bridge runs as a chat platform client (Mattermost/Slack), not as an HTTP server. To use the proxy pattern, the bridge needs to expose an A2A-compliant HTTP endpoint (or the proxy needs to call its SDK/RPC interface directly). This may require a thin HTTP wrapper or upstream contribution.

2. **No headless bridge mode** - copilot-bridge assumes a chat platform connection. In the proxy pattern, the bridge still needs to run without Mattermost/Slack - it just receives work via A2A HTTP on localhost instead of from a queue directly. A minimal "A2A adapter" (instead of `MattermostAdapter`) may still be needed upstream.

3. **No Kubernetes Operator** - CRD schemas, controller logic, and reconciliation loops for the Dark Factory don't exist. The closest analog is KEDA itself (which manages ScaledJobs), but it doesn't manage bridge instances, MCP servers, or AgentCards.

4. **No AgentCard registry** - A2A AgentCards need a discovery mechanism that works without HTTP endpoints. A Kubernetes-native registry (CRDs or ConfigMaps) needs design.

5. **No multi-bridge deployment pattern** - How to deploy N specialized bridge instances, each with distinct `AGENTS.md`, agent files, and queue subscriptions, is undocumented.

6. **No queue message schema** - The `delegate_task` proposal in the handoff research defines an internal schema. It hasn't been aligned with A2A's data model.

7. **No session externalization mechanism** - CLI session state (`session-store.db`, `events.jsonl`, checkpoints) is local to each container's filesystem. No archive/restore pipeline exists for persisting sessions beyond container lifecycle or resurrecting workers with previous context.

8. **No session archive index** - Even if sessions are archived as blobs, there's no queryable index to find sessions by repo, agent, task, or content without restoring the full archive.

---

## Analysis

### 1. Three Communication Models

#### Option A: Pure Message Queue

The orchestrator bridge publishes JSON task payloads to NATS subjects. Worker bridges (or CLI containers) consume, execute, and publish results to a response subject.

```
Orchestrator ──publish──► NATS subject ──consume──► Worker
Worker ──publish──► NATS results ──consume──► Orchestrator
```

**Strengths**: Full decoupling, buffering, scale-to-zero (KEDA watches queue depth), replay/retry via JetStream, back-pressure, dead letter handling.

**Weaknesses**: No standardized message schema - must roll a custom envelope. No discovery mechanism - routing is hardcoded. No interop with agents outside the factory. No streaming for real-time progress.

#### Option B: Pure A2A Protocol

The orchestrator bridge acts as an A2A Client, making `message/send` HTTP calls to worker bridges that each expose an A2A Server endpoint (`/.well-known/agent.json`).

```
Orchestrator ──HTTP POST──► Worker A2A Server
Worker ──SSE stream / webhook──► Orchestrator
```

**Strengths**: Standardized data model, AgentCard discovery, streaming via SSE, task lifecycle states, interop with any A2A-compliant agent (including non-Copilot agents), rich content exchange (text, files, structured data).

**Weaknesses**: HTTP coupling means the server must be running when called - incompatible with scale-to-zero. No buffering - if the worker is down, the request fails. Synchronous dispatch (even with async push notifications, the initial `message/send` is an HTTP call). No replay without adding a persistence layer.

#### Option C: Hybrid - A2A Data Model on Queue Transport

Use A2A's canonical data model (Task, Message, Part, Artifact) as the **message envelope format**. Deliver via NATS JetStream. Use AgentCards via a Kubernetes CRD registry instead of HTTP discovery.

```
Orchestrator ──publish A2A envelope──► NATS subject ──consume──► Worker
Worker ──publish A2A TaskStatusUpdate──► NATS results ──consume──► Orchestrator
AgentCard registry (K8s CRD) ──discovery──► Orchestrator
```

**Strengths**: Standardized schema (no custom envelope to maintain). Decoupled transport (queue semantics - buffering, retry, scale-to-zero). Discovery via CRD (Kubernetes-native). Future interop - if A2A gains a queue binding, we're aligned. SSE still available for real-time streaming on the orchestrator-to-chat leg. Can expose A2A HTTP endpoints on the orchestrator for external agents while using queue internally.

**Weaknesses**: Custom queue binding layer needed (serialize/deserialize A2A protobuf/JSON to NATS messages). AgentCard registry is non-standard (not `/.well-known/agent.json` over HTTP). Slightly higher complexity than pure queue. **A2A is not designed for queue transport** - the spec defines HTTP and gRPC bindings only. Using A2A's data model without its transport deviates from the spec and forfeits protocol-level interop (SDKs, tooling, compliance testing all assume HTTP/gRPC).

#### Option D: Queue-to-A2A Proxy (Sidecar Adapter Pattern)

Keep A2A on its **native HTTP/gRPC transport**. Keep the queue for **decoupling, buffering, and scale-to-zero**. Bridge the two with a lightweight proxy sidecar that reads from the queue, makes standard A2A HTTP calls to the bridge (localhost), and writes results back to the queue.

```
┌─────────────────────────────────────────────────────────────────┐
│  Worker Pod                                                      │
│                                                                   │
│  ┌──────────────────┐    HTTP (localhost)   ┌──────────────────┐ │
│  │  Queue-to-A2A    │ ──── POST /a2a ─────► │  copilot-bridge  │ │
│  │  Proxy Sidecar   │ ◄─── SSE stream ───── │  (A2A Server)    │ │
│  │                  │                        │                  │ │
│  │  - NATS consumer │                        │  - AGENTS.md     │ │
│  │  - A2A HTTP client│                        │  - .agent.md     │ │
│  │  - Result publisher│                       │  - MCP client    │ │
│  │  - Health check  │                        │  - Copilot CLI   │ │
│  └────────┬─────────┘                        └──────────────────┘ │
│           │                                                       │
└───────────┼───────────────────────────────────────────────────────┘
            │
            │ NATS JetStream
            │
┌───────────┼───────────────────────────────────────────────────────┐
│           │                                                       │
│  ┌────────▼─────────┐         ┌──────────────────┐               │
│  │ agent.tasks.*    │         │ agent.results     │               │
│  │ (consume)        │         │ (publish)         │               │
│  └──────────────────┘         └──────────────────┘               │
│           ▲                            ▲                          │
│           │                            │                          │
│  ┌────────┴─────────┐                  │                          │
│  │ Orchestrator     │──────────────────┘                          │
│  │ (A2A Client +    │  (also consumes results)                    │
│  │  Queue Publisher) │                                            │
│  └──────────────────┘                                             │
└───────────────────────────────────────────────────────────────────┘
```

**The proxy lifecycle for a single task:**

```
1. Proxy dequeues message from NATS (A2A SendMessageRequest JSON)
2. Proxy POSTs to http://localhost:PORT/a2a  (standard A2A message/send)
3. Bridge returns Task object + opens SSE stream
4. Proxy reads SSE events:
   - TaskStatusUpdateEvent (state: "working") → publish to agent.status
   - TaskArtifactUpdateEvent → buffer
   - Final TaskStatusUpdateEvent (state: "completed") → publish to agent.results
5. Proxy acks the NATS message
6. If bridge returns error or times out → proxy nacks → NATS redelivers
```

**Strengths**:
- **A2A stays on native transport** - HTTP/gRPC as the spec intends. SDKs, compliance tools, and the full protocol (streaming, push notifications, AgentCards) all work as designed.
- **Queue provides decoupling** - buffering, scale-to-zero, replay, back-pressure, dead letter. KEDA watches queue depth, not HTTP endpoints.
- **No custom A2A binding** - eliminates Gap #1 ("No A2A queue binding exists"). The proxy is a standard NATS consumer + standard A2A HTTP client.
- **No upstream bridge changes** - the bridge runs as a normal A2A server. No `QueueAdapter` needed. The proxy is the only new component.
- **AgentCards work natively** - bridge serves `/.well-known/agent.json` on localhost. Proxy or orchestrator can read it. No CRD registry needed (though one can still be added for cluster-wide discovery).
- **Proxy is generic** - works with any A2A-compliant server, not just copilot-bridge. Swap the bridge for a LangGraph agent, ADK agent, or BeeAI agent and the proxy doesn't change.
- **SSE streaming preserved** - proxy consumes SSE from bridge, publishes incremental status to the queue. Orchestrator gets real-time progress updates.
- **Clean separation of concerns** - proxy handles queue I/O and A2A transport; bridge handles agent execution. Each is independently testable.

**Weaknesses**:
- Extra process per pod (sidecar). Adds ~20-50MB memory overhead for a Go binary.
- Localhost HTTP adds ~1ms latency per request. Negligible for 5-15 min tasks.
- Two processes to health-check instead of one.

**The proxy itself is very thin** (~200-300 lines of Go):

```go
// Simplified proxy loop
func main() {
    natsConn := connectNATS(os.Getenv("NATS_URL"))
    sub := natsConn.QueueSubscribe(os.Getenv("QUEUE_SUBJECT"), "workers")
    a2aClient := newA2AClient("http://localhost:" + os.Getenv("BRIDGE_PORT"))

    for msg := range sub.Chan() {
        // 1. Deserialize A2A SendMessageRequest from queue
        var req a2a.SendMessageRequest
        json.Unmarshal(msg.Data, &req)

        // 2. POST to bridge's A2A endpoint
        resp, err := a2aClient.SendMessage(ctx, &req)
        if err != nil {
            msg.Nak()  // redelivery
            continue
        }

        // 3. If streaming, consume SSE and forward status
        if resp.IsTask() {
            for event := range a2aClient.StreamEvents(resp.Task.ID) {
                publishStatus(natsConn, "agent.status", event)
            }
        }

        // 4. Publish final result and ack
        publishResult(natsConn, "agent.results", resp)
        msg.Ack()
    }
}
```

### Trade-off Matrix

| Dimension | Queue Only | A2A Only | Hybrid (C) | **Proxy (D)** |
|-----------|-----------|----------|------------|---------------|
| **Discovery** | ❌ None | ✅ AgentCards HTTP | 🟡 CRD (non-standard) | ✅ AgentCards HTTP (native) |
| **Task lifecycle** | 🟡 Custom schema | ✅ Standardized | ✅ A2A model on queue | ✅ A2A model on HTTP (native) |
| **Decoupling** | ✅ Full | ❌ HTTP sync | ✅ Queue | ✅ Queue (proxy bridges) |
| **Scale-to-zero** | ✅ KEDA | ❌ Server must be up | ✅ KEDA | ✅ KEDA (proxy + bridge in same pod) |
| **Buffering / replay** | ✅ JetStream | ❌ Lost if down | ✅ Queue | ✅ Queue |
| **Streaming** | 🟡 Custom | ✅ SSE native | 🟡 SSE on gateway only | ✅ SSE native (proxy forwards) |
| **External interop** | ❌ Internal | ✅ Any A2A agent | 🟡 A2A data only | ✅ Full A2A protocol |
| **Schema maintenance** | 🟡 Roll your own | ✅ A2A spec | ✅ Reuse A2A model | ✅ A2A spec (native) |
| **Upstream changes** | 🟡 QueueAdapter | ❌ None | 🟡 QueueAdapter | ❌ None |
| **Spec compliance** | N/A | ✅ Full | 🟡 Data model only | ✅ Full |
| **Complexity** | Low | Medium | Medium | Medium (but well-separated) |
| **New code** | QueueAdapter in bridge | None | Queue binding library | ~300 line Go proxy |

### Recommendation: Option D (Queue-to-A2A Proxy)

Option D is strictly better than Option C. Both use A2A's data model and queue decoupling, but Option D keeps A2A on its native HTTP transport instead of inventing a custom queue binding. This eliminates Gap #1 (no A2A queue binding), eliminates the need for upstream `QueueAdapter` changes to copilot-bridge, preserves full A2A spec compliance (AgentCards, SSE streaming, SDK compatibility), and makes the bridge interchangeable with any A2A-compliant server.

The proxy is a generic, reusable component - it works with any A2A server behind any message queue. It's the [ambassador pattern](https://learn.microsoft.com/en-us/azure/architecture/patterns/ambassador) applied to agent communication: the proxy handles the impedance mismatch between queue-based dispatch and HTTP-based agent execution, so neither side needs to know about the other's transport.

---

### 2. Queue Message Envelope Design

The queue message uses A2A's `SendMessageRequest` as the envelope, extended with factory-specific metadata:

```json
{
  "jsonrpc": "2.0",
  "id": "task-uuid-here",
  "method": "message/send",
  "params": {
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "Implement JWT auth middleware for the Go API service"
        }
      ]
    },
    "configuration": {
      "agent": "coder",
      "model": "claude-sonnet-4",
      "autopilot": true,
      "acceptedOutputModes": ["text"]
    },
    "metadata": {
      "repo": "org/api-service",
      "branch": "feature/jwt-auth",
      "baseRef": "main",
      "workDir": "/src/middleware",
      "tools": {
        "grant": ["bash", "git", "gh", "mcp-github"],
        "deny": ["schedule", "ask_user"]
      },
      "context": {
        "specRef": "specs/jwt-auth/spec.md",
        "taskId": "dark-factory-042",
        "epicIssue": 12
      },
      "timeout": 600,
      "priority": "high"
    }
  }
}
```

Result messages use A2A's `Task` response with `TaskStatusUpdateEvent`:

```json
{
  "jsonrpc": "2.0",
  "result": {
    "type": "task",
    "id": "task-uuid-here",
    "contextId": "chain-uuid",
    "status": {
      "state": "completed",
      "timestamp": "2026-03-30T01:15:00Z",
      "message": {
        "role": "agent",
        "parts": [
          {
            "type": "text",
            "text": "JWT auth middleware implemented. PR #42 opened."
          }
        ]
      }
    },
    "artifacts": [
      {
        "name": "pull-request",
        "parts": [
          {
            "type": "data",
            "data": {
              "number": 42,
              "url": "https://github.com/org/api-service/pull/42",
              "branch": "feature/jwt-auth"
            }
          }
        ]
      }
    ]
  }
}
```

**Why this works**: The envelope is valid A2A JSON-RPC. If we later add HTTP transport, the same payload works over HTTP without transformation. The `metadata` field is A2A's extensibility mechanism - factory-specific data (repo, branch, tools) lives there.

---

### 3. AgentCard Discovery in an Ephemeral Factory

#### What AgentCards Are

An AgentCard is a JSON metadata document - a "digital business card" for an agent. It describes:

| Field | Purpose | Example |
|-------|---------|---------|
| `name` | Human-readable name | "Go Code Implementation Agent" |
| `description` | What the agent does | "Implements Go code from specifications" |
| `url` | Service endpoint | `http://localhost:8080/a2a` |
| `skills` | Capability list with IDs, tags, examples | `[{id: "go-impl", tags: ["go", "testing"]}]` |
| `capabilities` | Protocol features supported | `{streaming: true, pushNotifications: false}` |
| `authentication` | Auth requirements | `{schemes: ["Bearer"]}` |
| `provider` | Organization info | `{organization: "dark-factory"}` |

In standard A2A, an agent serves its own card at `/.well-known/agent-card.json`. A client fetches it to learn what the agent can do before sending work.

#### The Ephemeral Agent Problem

Standard A2A discovery assumes agents are long-running HTTP servers. In the Dark Factory:

- Workers scale to zero when idle (KEDA ScaledJob)
- Workers don't exist until a task arrives on the queue
- An agent that doesn't exist can't serve its own AgentCard
- The orchestrator needs to know agent capabilities *before* dispatching work

This means discovery must be **decoupled from agent lifecycle**. The A2A spec anticipates this with [Strategy 2: Curated Registries](https://a2a-protocol.org/latest/topics/agent-discovery/#2-curated-registries-catalog-based-discovery) - an intermediary service maintains a collection of AgentCards that clients query.

#### Factory Discovery Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                     AGENTCARD DISCOVERY FLOW                      │
│                                                                   │
│  ┌──────────────────┐                                             │
│  │ Agent Definitions │  (Helm values / ConfigMaps / CRDs)         │
│  │                  │                                             │
│  │  coder-go:       │                                             │
│  │    card: {...}   │                                             │
│  │    queue: agent. │                                             │
│  │     tasks.coder  │                                             │
│  │                  │                                             │
│  │  docs-writer:    │                                             │
│  │    card: {...}   │                                             │
│  │    queue: agent. │                                             │
│  │     tasks.docs   │                                             │
│  └────────┬─────────┘                                             │
│           │                                                       │
│           │  on deploy / update                                    │
│           ▼                                                       │
│  ┌──────────────────┐     NATS: agent.registry.updates            │
│  │ AgentCard        │ ──────────────────────────────────►         │
│  │ Registry         │     (publish on add/remove/update)          │
│  │                  │                                             │
│  │ Maps:            │                                             │
│  │  AgentCard ↔     │     ┌──────────────────┐                   │
│  │  queue subject   │◄────│ Orchestrator     │                   │
│  │                  │     │ queries registry │                   │
│  └──────────────────┘     │ before dispatch  │                   │
│                           └──────────────────┘                   │
│                                                                   │
│  Meanwhile, inside the worker pod (when running):                 │
│                                                                   │
│  ┌──────────────────────────────────────────────────────┐         │
│  │ Worker Pod                                            │         │
│  │                                                        │         │
│  │  Bridge serves AgentCard on localhost:                  │         │
│  │  GET http://localhost:8080/.well-known/agent-card.json  │         │
│  │                                                        │         │
│  │  Proxy reads it on startup to confirm capabilities     │         │
│  │  (optional - proxy already knows from registry)       │         │
│  └──────────────────────────────────────────────────────┘         │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

#### Dynamic Agent Registration

When a new agent type is deployed (e.g., a `security-scanner` agent is added to the Helm chart), the registry publishes an update to a NATS subject. All interested consumers (orchestrators, dashboards, monitoring) receive the update:

```
NATS subject: agent.registry.events

Message types:
  agent.registered    - new agent type available
  agent.deregistered  - agent type removed
  agent.updated       - agent card changed (new skills, model change)

Payload:
{
  "event": "agent.registered",
  "agentCard": {
    "name": "Security Scanner",
    "description": "Scans code for vulnerabilities",
    "skills": [
      { "id": "sast", "name": "Static Analysis", "tags": ["security"] }
    ],
    "capabilities": { "streaming": true }
  },
  "queue": {
    "subject": "agent.tasks.security",
    "resultSubject": "agent.results"
  },
  "timestamp": "2026-03-30T04:00:00Z"
}
```

The orchestrator subscribes to `agent.registry.events` and maintains an in-memory map of AgentCard -> queue subject. When a task arrives, it queries this map:

```
1. User: "@copilot scan the auth module for vulnerabilities"
2. Orchestrator classifies task: needs "security" skill
3. Orchestrator queries registry: which AgentCards have skill tag "security"?
4. Registry returns: security-scanner (queue: agent.tasks.security)
5. Orchestrator publishes A2A SendMessageRequest to agent.tasks.security
6. KEDA sees queue depth > 0, scales security-scanner pod from 0 to 1
7. Proxy in pod dequeues message, POSTs to bridge on localhost
8. Bridge serves its own AgentCard at /.well-known/agent-card.json (standard A2A)
```

#### Registry Implementation Options (Phased)

| Phase | Implementation | Complexity | When |
|-------|---------------|-----------|------|
| **v1** | Static config file or ConfigMap listing agent cards + queue subjects. Orchestrator reads on startup. | Low | Now |
| **v2** | NATS-backed registry with `agent.registry.events` subject. Orchestrator subscribes for dynamic updates. | Medium | When agent types change frequently |
| **v3** | Dedicated registry service (OSS) or K8s Operator with AgentCard CRD. | High | When factory has 10+ agent types or needs cross-cluster discovery |

#### Existing OSS Registry Projects

The A2A spec intentionally does **not** prescribe a registry API. From the spec: "The current A2A specification does not prescribe a standard API for curated registries." Several OSS projects fill this gap:

| Project | Type | Language | Key Features | Fit for Dark Factory |
|---------|------|----------|-------------|---------------------|
| [allenday/a2a-registry](https://github.com/allenday/a2a-registry) | Dedicated A2A registry | Python (FastAPI) | Register/search agents by skill, JSON-RPC + REST + GraphQL, `pip install a2a-registry` | Good v2 candidate - lightweight, purpose-built |
| [IBM ContextForge](https://github.com/IBM/mcp-context-forge) | Gateway + registry | Python | MCP + A2A federation, agent routing, OTel observability, K8s-ready | Interesting superset - does proxy + registry + gateway in one. Heavy. |
| [Alibaba Nacos](https://github.com/alibaba/nacos) | Service discovery platform | Java | Recently added A2A AgentCard + MCP registry. 32K stars. Battle-tested. | Too heavy for our use case - full microservice platform |
| [awslabs/a2a-agent-registry-on-aws](https://github.com/awslabs/a2a-agent-registry-on-aws) | AWS-native registry | Python | Lambda + S3 Vectors + Bedrock for semantic skill matching | AWS-locked but interesting for semantic search over AgentCards |

#### Who Registers Agents? (Not the Agents Themselves)

In our architecture, ephemeral workers don't register themselves - they may not exist when registration happens. Registration is triggered by the **deployment process**, not the agent lifecycle:

```
┌──────────────────────────────────────────────────────────────────┐
│                   REGISTRATION FLOW                               │
│                                                                   │
│  1. Developer adds new agent type                                 │
│     (Helm values, ConfigMap, or AgentCard CRD)                    │
│                                                                   │
│  2. Deploy pipeline (CI/CD or helm upgrade) triggers              │
│     registration:                                                 │
│                                                                   │
│     ┌────────────────────┐                                        │
│     │ Helm post-install  │                                        │
│     │ hook / CI step     │                                        │
│     │                    │                                        │
│     │ Reads AgentCard    │                                        │
│     │ from values.yaml   │                                        │
│     │ or ConfigMap       │                                        │
│     └────────┬───────────┘                                        │
│              │                                                    │
│              ▼                                                    │
│     ┌────────────────────┐     ┌──────────────────┐              │
│     │ Publish to NATS    │────►│ agent.registry.   │              │
│     │ agent.registered   │     │ events subject    │              │
│     └────────────────────┘     └────────┬─────────┘              │
│                                          │                        │
│              ┌───────────────────────────┤                        │
│              │                           │                        │
│              ▼                           ▼                        │
│     ┌────────────────────┐     ┌──────────────────┐              │
│     │ Orchestrator       │     │ Monitoring /     │              │
│     │ updates in-memory  │     │ Dashboard        │              │
│     │ AgentCard → queue  │     │ shows new agent  │              │
│     │ routing map        │     │                  │              │
│     └────────────────────┘     └──────────────────┘              │
│                                                                   │
│  3. Worker pods don't exist yet. KEDA ScaledJob has min: 0.       │
│     The queue subject (agent.tasks.coder-rust) is created in      │
│     JetStream as part of the Helm deploy.                         │
│                                                                   │
│  4. When orchestrator routes a task to this agent type,           │
│     KEDA sees queue depth > 0 and scales the pod.                 │
│     Proxy + bridge start. Bridge serves its own AgentCard         │
│     on localhost (standard A2A, for runtime validation).          │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

#### v1 Implementation (Static Config)

For v1, the orchestrator just needs a JSON file:

```json
{
  "agents": [
    {
      "card": {
        "name": "Go Coder",
        "skills": [{ "id": "go-impl", "tags": ["go", "implementation"] }],
        "capabilities": { "streaming": true }
      },
      "queue": "agent.tasks.coder-go"
    },
    {
      "card": {
        "name": "Documentation Writer",
        "skills": [{ "id": "docs", "tags": ["docs", "markdown"] }],
        "capabilities": { "streaming": true }
      },
      "queue": "agent.tasks.docs"
    }
  ]
}
```

This is enough to route tasks. Adding a new agent means editing this file and restarting the orchestrator.

#### v2 Implementation (NATS-Backed Dynamic Registry)

When agent types start changing at runtime (e.g., a new specialization is deployed while the factory is running), the static config becomes a bottleneck. v2 adds a NATS-backed event channel:

**Registration (Helm post-install hook or CI step):**

```bash
#!/bin/bash
# register-agent.sh - called by Helm post-install hook
AGENT_CARD_JSON=$(cat /config/agent-card.json)
QUEUE_SUBJECT="${QUEUE_SUBJECT:?}"

nats pub agent.registry.events "{
  \"event\": \"agent.registered\",
  \"agentCard\": $AGENT_CARD_JSON,
  \"queue\": {
    \"subject\": \"$QUEUE_SUBJECT\",
    \"resultSubject\": \"agent.results\"
  },
  \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
}"
```

**Deregistration (Helm pre-delete hook):**

```bash
#!/bin/bash
# deregister-agent.sh - called by Helm pre-delete hook
nats pub agent.registry.events "{
  \"event\": \"agent.deregistered\",
  \"agentName\": \"$AGENT_NAME\",
  \"queue\": { \"subject\": \"$QUEUE_SUBJECT\" },
  \"timestamp\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"
}"
```

**Orchestrator subscribes on startup:**

```go
// Orchestrator loads static config first, then subscribes for live updates
registry := loadStaticConfig("agent-registry.json")

natsConn.Subscribe("agent.registry.events", func(msg *nats.Msg) {
    var event RegistryEvent
    json.Unmarshal(msg.Data, &event)

    switch event.Event {
    case "agent.registered":
        registry.Add(event.AgentCard, event.Queue.Subject)
        log.Info("Agent registered", "name", event.AgentCard.Name)
    case "agent.deregistered":
        registry.Remove(event.AgentName)
        log.Info("Agent deregistered", "name", event.AgentName)
    case "agent.updated":
        registry.Update(event.AgentCard, event.Queue.Subject)
        log.Info("Agent updated", "name", event.AgentCard.Name)
    }
})
```

**NATS JetStream for durability:** Use a JetStream stream (not raw NATS pub/sub) for the registry events so that if the orchestrator restarts, it can replay the event history to rebuild its routing map:

```bash
# Create durable stream for registry events
nats stream add AGENT_REGISTRY \
  --subjects "agent.registry.events" \
  --retention limits \
  --max-msgs-per-subject 100 \
  --storage file
```

#### v3: Dedicated Registry Service or K8s Operator

At scale (10+ agent types, cross-cluster discovery), consider either:

- **allenday/a2a-registry** as a standalone service (FastAPI, searchable by skill, already has A2A protocol support)
- **K8s Operator** with `AgentCard` CRD where the controller watches CRD changes and publishes to NATS
- **IBM ContextForge** if you also need MCP federation and gateway capabilities in one service

---

### 4. The Runtime Harness Problem

`.agent.md` files are inert configuration - YAML frontmatter (model, tools, description) plus a system prompt in markdown. They cannot execute independently. They require:

| Need | What Provides It |
|------|-----------------|
| LLM API connection | Copilot subscription (GITHUB_TOKEN) |
| Tool runtime | bash, git, gh CLI, MCP client |
| Session management | Conversation state, hooks, skills |
| I/O transport | Queue consumer, HTTP server, or chat WebSocket |
| Agent file interpretation | Code that reads frontmatter + applies system prompt |

Three runtime harness options exist:

#### Harness A: Copilot CLI (Thinnest)

```
copilot -p "system prompt contents + task prompt" --yolo --allow-all-tools
```

The CLI doesn't read `.agent.md` files directly. The system prompt and tool grants are injected via command-line flags. This works but loses the agent abstraction - every invocation must reconstruct the persona from scratch.

- **Image**: ~300MB (Node.js + CLI + Git)
- **Startup**: 3-5s
- **Agent support**: None - prompt injection only
- **MCP**: Sidecar or embedded
- **State**: None

#### Harness B: Minimal copilot-bridge (Recommended)

A copilot-bridge instance configured for a single specialization. It reads `AGENTS.md` + agent files, has full MCP and tool support, but operates headless - reading from a queue subject instead of Mattermost.

```
copilot-bridge --headless --queue nats://nats:4222 --subject agent.tasks.coder
```

(Hypothetical - requires upstream `QueueAdapter` alongside `MattermostAdapter`/`SlackAdapter`)

- **Image**: ~500MB (Node.js + bridge + CLI + Git)
- **Startup**: 10-15s
- **Agent support**: Full - reads `.agent.md`, applies frontmatter
- **MCP**: Full orchestration (stdio + SSE)
- **State**: SQLite per-instance (ephemeral for workers)
- **Hooks/skills**: Available but typically unused for workers

#### Harness C: Full copilot-bridge

A complete bridge instance with chat connection, scheduling, hooks, skills, and multiple agent personas. This is the orchestrator, not a worker.

- **Image**: ~500MB
- **Startup**: 10-15s
- **Agent support**: Full multi-agent (AGENTS.md with N agents)
- **MCP**: Full orchestration
- **State**: Persistent SQLite
- **Chat**: Connected to Mattermost/Slack

#### Runtime Harness Decision Matrix

| Dimension | CLI (A) | Minimal Bridge (B) | Full Bridge (C) |
|-----------|---------|--------------------|-----------------| 
| Image size | ~300MB | ~500MB | ~500MB |
| Startup | 3-5s | 10-15s | 10-15s |
| `.agent.md` support | ❌ | ✅ | ✅ |
| MCP support | Sidecar only | Full | Full |
| Queue consumer | Custom entrypoint | Bridge adapter | Bridge adapter |
| Scaling pattern | KEDA ScaledJob | KEDA ScaledJob | StatefulSet + HPA |
| Use case | Leaf tasks | **Specialized workers** | **Orchestrator** |

**Recommendation**: Use Harness B (minimal bridge) for workers and Harness C (full bridge) for the orchestrator. The CLI-only harness (A) is viable for simple leaf tasks but loses agent file support and MCP orchestration. The cost difference between A and B (~200MB image size, ~7s startup) is minimal compared to the capability gain.

---

### 5. Multi-Bridge Team Architecture

Each bridge instance is a specialized "team" with its own agent personas, reading from a dedicated queue subject:

```
┌─────────────────────────────────────────────────────────────────────┐
│                      DARK FACTORY - MULTI-BRIDGE TOPOLOGY           │
│                                                                     │
│  ┌─────────────────┐                                                │
│  │ Mattermost      │◄──WebSocket──► Orchestrator Bridge             │
│  │ #dark-factory   │                 ├─ AGENTS.md: orchestrator      │
│  └─────────────────┘                 ├─ Skills: classify, fan-out    │
│                                      ├─ AgentCard registry           │
│  ┌─────────────────┐                 ├─ Publishes: agent.tasks.*     │
│  │ Mattermost      │◄──WebSocket──► │ Consumes: agent.results       │
│  │ #factory-logs   │     (optional) └─────────────┬─────────────────│
│  └─────────────────┘                              │                  │
│                                             NATS JetStream           │
│                                                   │                  │
│         ┌─────────────────────────────────────────┼──────────┐       │
│         │                    │                    │          │       │
│         ▼                    ▼                    ▼          ▼       │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌───────────┐  │
│  │ Coder Team  │  │ Docs Team   │  │ Research    │  │ Infra     │  │
│  │ Bridge      │  │ Bridge      │  │ Team Bridge │  │ Team      │  │
│  │             │  │             │  │             │  │ Bridge    │  │
│  │ AGENTS.md:  │  │ AGENTS.md:  │  │ AGENTS.md:  │  │ AGENTS.md:│  │
│  │  coder      │  │  writer     │  │  researcher │  │  terraform│  │
│  │  reviewer   │  │  editor     │  │             │  │  k8s-admin│  │
│  │  tester     │  │             │  │             │  │           │  │
│  │             │  │             │  │             │  │           │  │
│  │ Queue:      │  │ Queue:      │  │ Queue:      │  │ Queue:    │  │
│  │ agent.tasks │  │ agent.tasks │  │ agent.tasks │  │ agent.    │  │
│  │ .coder      │  │ .docs       │  │ .research   │  │ tasks.    │  │
│  │             │  │             │  │             │  │ infra     │  │
│  │ Chat: NONE  │  │ Chat: NONE  │  │ Chat: NONE  │  │ Chat: NONE│  │
│  │ (headless)  │  │ (headless)  │  │ (headless)  │  │ (headless)│  │
│  └─────────────┘  └─────────────┘  └─────────────┘  └───────────┘  │
│                                                                     │
│  Worker bridges are KEDA ScaledJobs - scale 0↔N per queue subject   │
└─────────────────────────────────────────────────────────────────────┘
```

Each worker bridge receives a structured A2A envelope from the queue that specifies:
- Which **agent persona** to activate (`configuration.agent`)
- Which **model** to use (`configuration.model`)
- What **tools** to grant/deny (`metadata.tools`)
- Which **repo/branch** to operate on (`metadata.repo`, `metadata.branch`)
- The **task prompt** (`message.parts[].text`)
- **Timeout** and **priority** (`metadata.timeout`, `metadata.priority`)

The bridge reads the envelope, activates the specified agent persona (from its `AGENTS.md`), and executes the task. The result is published back to `agent.results` using A2A's Task response format.

---

### 6. Kubernetes Operator Design

A custom Kubernetes Operator manages the factory lifecycle declaratively:

#### CRD: AgentRuntime

Defines a worker bridge specialization and its scaling parameters.

```yaml
apiVersion: darkfactory.io/v1alpha1
kind: AgentRuntime
metadata:
  name: coder-go
  namespace: dark-factory
spec:
  image: ghcr.io/dark-factory/bridge-worker:latest

  replicas:
    min: 0          # Scale-to-zero when idle
    max: 10         # Cap concurrent instances

  agents:
    primary: "coder"
    files:
      - configMap: coder-agent-files   # .agent.md, prompts
    model: claude-sonnet-4
    autopilot: true

  queue:
    subject: "agent.tasks.coder-go"
    resultSubject: "agent.results"
    ackWait: 600s

  mcpServers:
    - name: github-mcp
    - name: filesystem-mcp

  agentCard:
    skills:
      - id: "go-implementation"
        name: "Go Code Implementation"
        description: "Implements Go code from specs and tasks"
        tags: ["go", "implementation", "testing"]

  resources:
    requests: { cpu: 500m, memory: 512Mi }
    limits: { cpu: "2", memory: 2Gi }
```

**What the operator does on reconciliation:**
1. Creates/updates a KEDA `ScaledJob` watching `spec.queue.subject`
2. Creates a Kubernetes `Job` template with the bridge image, agent ConfigMap mounts, env vars for queue connection and auth
3. Registers the `agentCard` in the AgentCard registry (another CRD or ConfigMap)
4. Validates MCP server references exist

#### CRD: MCPServer

Defines a shared MCP server deployment.

```yaml
apiVersion: darkfactory.io/v1alpha1
kind: MCPServer
metadata:
  name: github-mcp
  namespace: dark-factory
spec:
  image: ghcr.io/github/github-mcp-server:latest
  transport: sse        # SSE or stdio
  port: 8080
  replicas: 2
  auth:
    secretRef: github-app-token
  tools:
    - create_issue
    - search_code
    - create_pull_request
```

**What the operator does**: Creates a `Deployment` + `Service` for the MCP server. Worker bridges reference it via the service DNS name.

#### CRD: AgentCard

A Kubernetes-native representation of an A2A AgentCard, used for discovery without requiring HTTP endpoints.

```yaml
apiVersion: darkfactory.io/v1alpha1
kind: AgentCard
metadata:
  name: coder-go
  namespace: dark-factory
spec:
  name: "Go Code Implementation Agent"
  description: "Implements Go code from specifications and tasks"
  version: "1.0.0"
  skills:
    - id: "go-implementation"
      name: "Go Code Implementation"
      description: "Writes production Go code with tests"
      tags: ["go", "implementation", "testing"]
      examples:
        - "Implement JWT auth middleware"
        - "Write unit tests for the user service"
    - id: "go-review"
      name: "Go Code Review"
      description: "Reviews Go code for correctness and style"
      tags: ["go", "review"]
  queue:
    subject: "agent.tasks.coder-go"
  capabilities:
    streaming: false
    pushNotifications: false
    stateTransitionHistory: true
```

**What the operator does**: Maintains an in-memory registry of all AgentCards. The orchestrator bridge queries this registry to discover which agents can handle a given task. In future, can also serve these via HTTP at `/.well-known/agent.json` for external A2A clients.

#### CRD: TaskPipeline

Defines multi-step workflow routing rules (fan-out, dependency chains, merge strategies).

```yaml
apiVersion: darkfactory.io/v1alpha1
kind: TaskPipeline
metadata:
  name: full-feature-pipeline
  namespace: dark-factory
spec:
  steps:
    - name: implement
      agentRef: coder-go
      dependsOn: []
    - name: test
      agentRef: tester
      dependsOn: [implement]
    - name: docs
      agentRef: docs-writer
      dependsOn: [implement]
    - name: review
      agentRef: reviewer
      dependsOn: [implement, test]
  mergeStrategy: all-must-pass
  timeout: 3600s
```

**What the operator does**: Injects the pipeline definition into the orchestrator bridge's configuration. The orchestrator uses it to determine fan-out/fan-in patterns when dispatching multi-step tasks.

---

### 7. Mattermost Connectivity in a Multi-Bridge World

Two connectivity models exist. The **gateway model** is recommended for production:

#### Gateway Model (Recommended)

Only the orchestrator bridge connects to Mattermost. Workers are headless.

| Component | Mattermost | Queue | HTTP |
|-----------|------------|-------|------|
| Orchestrator Bridge | ✅ WebSocket (bot token) | ✅ Publisher + consumer | Optional: A2A server for external agents |
| Worker Bridges | ❌ None | ✅ Consumer + publisher | ❌ None |
| Log Observer (optional) | ✅ WebSocket (separate channel) | ✅ Consumer (read-only) | ❌ None |

**Benefits**: Single bot in chat (clean UX), workers have no network exposure beyond queue, clear separation of concerns.

#### Full Mesh Model (Dev/Debug)

Every bridge has its own Mattermost channel. Useful for watching agent "thoughts" in real-time during development.

| Component | Mattermost | Queue | Use Case |
|-----------|------------|-------|----------|
| Orchestrator | ✅ `#dark-factory` | ✅ | Human interaction |
| Coder Worker | ✅ `#factory-coder` | ✅ | Watch code generation live |
| Docs Worker | ✅ `#factory-docs` | ✅ | Watch doc generation live |

**Drawbacks**: More bot tokens, more channels, more noise. Not suitable for production.

---

### 8. Task Routing and Lifecycle

#### Routing Decision Tree

```
Incoming request
  │
  ├─ Classify complexity
  │   ├─ Simple (single-file, known pattern) → Handle inline in orchestrator
  │   └─ Complex (multi-file, multi-step) → Dispatch via queue
  │
  ├─ Discover agents (query AgentCard registry)
  │   └─ Match required skills to available AgentCards
  │
  ├─ Check dependencies
  │   ├─ Independent tasks → Fan-out: publish all to respective queues
  │   └─ Dependent tasks → Sequential: publish first, await result, then next
  │
  └─ Await results
      ├─ Single task → Consume result, reply to user
      └─ Fan-out → Merge all results, reply to user
```

#### Task Lifecycle States (A2A-aligned)

```
                    ┌──────────────────────────┐
                    │                          │
 ┌──────────┐  ┌───▼──────┐  ┌───────────┐  ┌─┴──────────┐
 │ submitted │─►│ working  │─►│ completed │  │  failed    │
 └──────────┘  └───┬──────┘  └───────────┘  └────────────┘
                   │                            ▲
                   │  ┌────────────────┐        │
                   └──► input-required ├────────┘ (timeout)
                      └────────────────┘
                         ▲
                         │ (human-in-the-loop,
                         │  e.g., approval needed)
                         │
                    ┌────┴──────┐
                    │ canceled  │ (user or orchestrator cancels)
                    └───────────┘
```

These map directly to A2A's `TaskState` enum, so no custom state machine is needed.

---

### 9. Session State Externalization and Resurrection

A critical requirement for the factory is **stateless worker containers that can be resurrected** to continue or modify previous work. This requires externalizing session state to a persistent, distributed store - but the current architecture splits state across two independently managed layers with different externalization profiles.

#### Two-Layer State Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                      SESSION STATE TOPOLOGY                          │
│                                                                      │
│  Layer 1: copilot-bridge state.db (Bridge-Owned)                     │
│  ───────────────────────────────────────────────                      │
│  Path:   ~/.copilot-bridge/state.db                                  │
│  Engine: better-sqlite3 (Node.js, WAL mode)                         │
│  Owner:  copilot-bridge (src/state/store.ts)                         │
│  Access: ~60 exported functions via getDb() singleton                │
│                                                                      │
│  Tables:                                                             │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │
│  │ channel_sessions │  │ channel_prefs    │  │ permission_rules │   │
│  │ channel → session│  │ model, agent,    │  │ scope, tool,     │   │
│  │ mapping          │  │ verbose, mode    │  │ allow/deny       │   │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘   │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │
│  │ workspace_overrides│ │ scheduled_tasks │  │ agent_calls      │   │
│  │ bot → workdir    │  │ cron, prompts   │  │ inter-agent log  │   │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘   │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │
│  │ dynamic_channels │  │ scheduled_task_  │  │ settings         │   │
│  │                  │  │ history          │  │ key-value store  │   │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘   │
│                                                                      │
│  Externalization: POSSIBLE - replace better-sqlite3 with pg client   │
│  Effort: Moderate - clean module boundary, ~60 functions             │
│                                                                      │
│  ────────────────────────────────────────────────────────────────     │
│                                                                      │
│  Layer 2: Copilot CLI session-store.db + events (CLI-Owned)          │
│  ──────────────────────────────────────────────────────               │
│  Paths:  ~/.copilot/session-store.db                                 │
│          ~/.copilot/session-state/[UUID]/events.jsonl                 │
│          ~/.copilot/session-state/[UUID]/workspace.yaml               │
│          ~/.copilot/session-state/[UUID]/checkpoints/                 │
│  Engine: SQLite (internal to Copilot CLI binary)                     │
│  Owner:  Copilot CLI (closed source binary)                          │
│  Access: SDK API only - client.resumeSession(), client.listSessions()│
│                                                                      │
│  Tables (session-store.db):                                          │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │
│  │ sessions         │  │ turns            │  │ checkpoints      │   │
│  │ id, repo, branch │  │ session_id,      │  │ title, overview, │   │
│  │ summary, dates   │  │ user_message,    │  │ work_done,       │   │
│  │                  │  │ assistant_resp.  │  │ next_steps       │   │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘   │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │
│  │ session_files    │  │ session_refs     │  │ search_index     │   │
│  │ files touched    │  │ issues, PRs      │  │ FTS5 full-text   │   │
│  │ per session      │  │ cross-refs       │  │ across all turns │   │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘   │
│                                                                      │
│  Externalization: NOT POSSIBLE directly - CLI owns SQLite            │
│  Workaround: Snapshot/restore pattern (see below)                    │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

#### The Externalization Problem

The two layers have fundamentally different ownership models:

| Dimension | Bridge `state.db` | CLI `session-store.db` + `events.jsonl` |
|-----------|-------------------|----------------------------------------|
| **Code owner** | copilot-bridge (open source, Node.js) | Copilot CLI binary (closed source) |
| **Storage engine** | `better-sqlite3` via `store.ts` | Internal SQLite, filesystem |
| **API access** | Direct SQL - full CRUD | SDK methods only: `createSession`, `resumeSession`, `listSessions`, `deleteSession` |
| **Swap to Postgres** | ✅ Replace `better-sqlite3` imports, update queries | ❌ Cannot modify CLI storage layer |
| **Data volume** | Small (~108 KB) | Large (sessions: 1.6 MB DB + 67 MB event logs) |
| **Resurrection value** | Low - operational config, easily rebuilt | **High** - conversation history, checkpoints, file context |

The irony: the data we most need to externalize (conversation history, checkpoints, the "memory" that enables session resurrection) is the data we have the least control over.

#### Today's Workarounds: Hooks and Event Streams

Before building a full snapshot/restore pipeline, the existing copilot-bridge **hooks system** and the CLI's **JSONL event stream** provide immediate workarounds for session state capture. These work today with no upstream changes.

**Available Hooks**

copilot-bridge supports 6 hook types, each executing shell commands with JSON piped to stdin:

| Hook | Fires When | Input Contains | Use for Session State |
|------|-----------|----------------|----------------------|
| `sessionStart` | New session created or resumed | `{ sessionId, channelId }` | Register session in external index |
| `sessionEnd` | Session destroyed (`/new`, timeout) | `{ sessionId, channelId }` | Archive session state to external store |
| `preToolUse` | Before every tool call | `{ toolName, toolInput, sessionId }` | Log tool decisions (audit trail) |
| `postToolUse` | After every tool call | `{ toolName, toolOutput, sessionId }` | Capture tool results incrementally |
| `userPromptSubmitted` | User sends a message | `{ prompt, sessionId }` | Log conversation turns in real-time |
| `errorOccurred` | Error during session | `{ error, sessionId }` | Capture failure state for post-mortem |

Hooks are defined in `hooks.json` and discovered from three layers (lowest → highest priority):
1. Plugin hooks: `~/.copilot/installed-plugins/.../hooks.json`
2. User hooks: `~/.copilot/hooks.json`
3. Workspace hooks: `<workspace>/.github/hooks/hooks.json`

This workspace already uses `sessionStart` and `sessionEnd` hooks for Beads task memory:

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [
      { "type": "command", "bash": "./session-start.sh", "cwd": ".", "timeoutSec": 15 }
    ],
    "sessionEnd": [
      { "type": "command", "bash": "./session-end.sh", "cwd": ".", "timeoutSec": 30 }
    ]
  }
}
```

**Workaround 1: `sessionEnd` hook archives session state**

Extend the existing `session-end.sh` to copy CLI session data to an external store:

```bash
#!/usr/bin/env bash
set -euo pipefail
input=$(cat)
SESSION_ID=$(echo "$input" | jq -r '.sessionId')

# Existing: Beads backup
bd backup export-git >/dev/null 2>&1 || true

# NEW: Archive CLI session state
SESSION_DIR="$HOME/.copilot/session-state/$SESSION_ID"
if [ -d "$SESSION_DIR" ]; then
  # Option A: Copy to shared volume / NFS
  cp -r "$SESSION_DIR" "/shared/session-archive/$SESSION_ID/"
  cp "$HOME/.copilot/session-store.db" "/shared/session-archive/$SESSION_ID/session-store.db"

  # Option B: Upload to blob storage
  # tar czf /tmp/session.tar.gz -C "$HOME/.copilot/session-state" "$SESSION_ID/"
  # az storage blob upload -f /tmp/session.tar.gz -c sessions -n "$SESSION_ID/state.tar.gz"
fi

echo '{}'
```

**Workaround 2: `postToolUse` hook streams tool results to external store**

For real-time capture (not just end-of-session), a `postToolUse` hook can stream every tool interaction to an external system as it happens:

```json
{
  "version": 1,
  "hooks": {
    "postToolUse": [
      { "type": "command", "bash": "./capture-tool-use.sh", "cwd": ".", "timeoutSec": 5 }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# capture-tool-use.sh - append tool use to external JSONL log
set -euo pipefail
input=$(cat)
SESSION_ID=$(echo "$input" | jq -r '.sessionId // empty')
TOOL_NAME=$(echo "$input" | jq -r '.toolName // empty')
TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Append to external JSONL (shared volume, S3, or Postgres)
echo "{\"session_id\":\"$SESSION_ID\",\"tool\":\"$TOOL_NAME\",\"timestamp\":\"$TIMESTAMP\",\"event\":$input}" \
  >> "/shared/session-logs/$SESSION_ID.jsonl"

echo '{}'
```

**Workaround 3: Direct JSONL event stream as primary archive**

The CLI writes `~/.copilot/session-state/<UUID>/events.jsonl` as a comprehensive, append-only event stream. Every session event - `session.start`, user messages, assistant responses, tool calls, tool results, mode changes - is captured as a JSONL line with linked `id`/`parentId` fields.

This file is the **most complete record** of a session. Rather than building custom capture via hooks, the simplest archive strategy is:

1. Let the session run normally (CLI writes `events.jsonl` as always)
2. On `sessionEnd` hook: copy `events.jsonl` + `workspace.yaml` + `session-store.db` to external store
3. On resurrection: restore these files, call `client.resumeSession()`

The events.jsonl format:
```jsonl
{"type":"session.start","data":{"sessionId":"529750d1-...","version":1,"producer":"copilot-agent","copilotVersion":"0.0.421","startTime":"2026-03-05T02:11:54.553Z","context":{"cwd":"/path","gitRoot":"/path","branch":"main","repository":"org/repo"}},"id":"uuid","timestamp":"...","parentId":null}
{"type":"session.mode_change","data":{...},"id":"uuid","timestamp":"...","parentId":"prev-uuid"}
{"type":"user.message","data":{"text":"implement JWT auth"},"id":"uuid","timestamp":"...","parentId":"prev-uuid"}
{"type":"assistant.message","data":{"text":"I'll create..."},"id":"uuid","timestamp":"...","parentId":"prev-uuid"}
{"type":"tool.call","data":{"toolName":"bash","input":{...}},"id":"uuid","timestamp":"...","parentId":"prev-uuid"}
```

**Hook Limitations**

| Limitation | Impact | Mitigation |
|-----------|--------|------------|
| `sessionEnd` has a `timeoutSec` (default 30s) | Large sessions may not finish archiving in time | Use async upload (background `&` process) or increase timeout |
| Hooks execute shell commands - no native SDK integration | Can't call Postgres client directly from hook | Use `psql`, `curl`, or a lightweight CLI tool |
| `postToolUse` fires on every tool call | High-frequency archiving adds latency per tool call | Only enable for sessions that need real-time capture |
| Hooks receive JSON via stdin, return JSON via stdout | Structured but adds serialization overhead | Negligible for session state use case |
| Workspace hooks require `allowWorkspaceHooks: true` in bridge config | Security gate - disabled by default | Set in bridge config for factory workers |

#### Session ID: CLI-Generated, Not Pre-Determinable

The session ID is generated by the **Copilot CLI binary**, not by the bridge. The bridge calls `client.createSession(opts)` and receives a `CopilotSession` object with a `sessionId` field - a UUID v4 generated internally by the CLI:

```typescript
// bridge.ts - session creation flow
const session = await this.client.createSession({
  clientName: 'copilot-bridge',
  model: opts.model,
  workingDirectory: opts.workingDirectory,
  // ... other options
});
// session.sessionId is CLI-generated - bridge has no input on this value
this.sessions.set(session.sessionId, session);
```

The `createSession` API does **not** accept a `sessionId` parameter. The UUID is generated server-side (inside the CLI process) and returned to the caller. This means:

1. **You cannot pre-generate a session ID** before starting the bridge
2. **You cannot pre-stage session state** in `~/.copilot/session-state/<UUID>/` before the session exists
3. **The external archive key must be assigned after session creation**, not before

**Is pre-staging needed?** No - and here's why. The resurrection flow is:

```
┌──────────────────────────────────────────────────────────────────────┐
│                  RESURRECTION FLOW                                   │
│                                                                      │
│  1. Orchestrator dispatches task with metadata.resume.sessionId       │
│     (this is the ORIGINAL session's ID, from the archive)            │
│                                                                      │
│  2. Worker init container / entrypoint prologue:                     │
│     a. Download archived session-store.db → ~/.copilot/              │
│     b. Download archived session-state/<original-uuid>/ → ~/.copilot/│
│     (The CLI now sees the original session in its local SQLite)      │
│                                                                      │
│  3. Bridge starts, calls client.resumeSession(originalSessionId)     │
│     (CLI finds the session in restored session-store.db → success)   │
│                                                                      │
│  4. Bridge continues with full conversation history                  │
│                                                                      │
│  Result: The restored session ID IS the original session ID.         │
│  No new ID is generated. No pre-staging needed.                      │
└──────────────────────────────────────────────────────────────────────┘
```

The key insight: `resumeSession(id)` does not create a new session - it reconnects to an existing one. As long as the CLI's local SQLite contains a session with that ID, resume succeeds. The archive/restore flow places the data before the CLI starts, so the CLI finds it naturally.

**External archive keying strategy:**

| Key | Format | When Set | Use |
|-----|--------|----------|-----|
| Session ID | UUID v4 (CLI-generated) | After `createSession()` returns | Primary archive key - immutable, globally unique |
| Task ID | UUID or factory-assigned | Before session starts (from queue envelope) | Correlates session to factory task - lookup key |
| Composite | `{task_id}/{session_id}` | After first turn | Archive path in blob storage |

The `sessionStart` hook receives `{ sessionId, channelId }` - this is the moment to register the session in the external Postgres index:

```bash
#!/usr/bin/env bash
# session-start.sh - register session in external archive index
set -euo pipefail
input=$(cat)
SESSION_ID=$(echo "$input" | jq -r '.sessionId')
TASK_ID="${TASK_ID:-unassigned}"  # From queue envelope env var

psql "$DATABASE_URL" -c "
  INSERT INTO session_archive (session_id, task_id, status, created_at)
  VALUES ('$SESSION_ID', '$TASK_ID', 'active', now())
  ON CONFLICT (session_id) DO UPDATE SET status = 'active', task_id = '$TASK_ID'
"

echo '{}'
```

#### Recommended Pattern: Snapshot/Restore with External Archive

Since we can't swap the CLI's internal SQLite for Postgres, we use a **snapshot/restore pattern** - archiving session state after task completion and hydrating it before resurrection.

```
┌──────────────────────────────────────────────────────────────────────┐
│                  SNAPSHOT / RESTORE LIFECYCLE                         │
│                                                                      │
│  ┌─────────┐     ┌─────────────┐     ┌─────────────┐               │
│  │ Worker  │────►│ Execute     │────►│ Snapshot     │               │
│  │ starts  │     │ task        │     │ session      │               │
│  └─────────┘     └─────────────┘     └──────┬──────┘               │
│       │                                       │                      │
│       │  Optional: restore                    │  Archive to          │
│       │  previous session                     │  external store      │
│       │                                       ▼                      │
│  ┌────┴──────────┐                   ┌─────────────────┐            │
│  │ Hydrate       │◄─────────────────│ External Store   │            │
│  │ ~/.copilot/   │   on resurrection │ (Postgres/S3)   │            │
│  │ from archive  │                   │                  │            │
│  └───────────────┘                   │ Tables:          │            │
│                                      │ • session_archive│            │
│                                      │ • turn_archive   │            │
│                                      │ • event_blobs    │            │
│                                      │ • checkpoint_    │            │
│                                      │   archive        │            │
│                                      └─────────────────┘            │
└──────────────────────────────────────────────────────────────────────┘
```

**Phase 1: Post-Execution Snapshot** (sidecar or entrypoint epilogue)

After a worker bridge completes its task:

1. Copy `~/.copilot/session-store.db` - contains session metadata, turns, checkpoints, file refs, search index
2. Tar `~/.copilot/session-state/<session-uuid>/` - contains `events.jsonl`, `workspace.yaml`, checkpoint files
3. Upload both to a session archive store keyed by `(task_id, session_id)`

```bash
#!/bin/bash
# post-task-snapshot.sh - runs as entrypoint epilogue or sidecar
SESSION_ID="${COPILOT_SESSION_ID:?}"
TASK_ID="${TASK_ID:?}"
ARCHIVE_BUCKET="${SESSION_ARCHIVE_BUCKET:?}"

# Snapshot session-store.db (SQLite safe copy)
cp ~/.copilot/session-store.db /tmp/session-store-snapshot.db

# Tar the session event stream and artifacts
tar czf /tmp/session-state.tar.gz \
  -C ~/.copilot/session-state "${SESSION_ID}/"

# Upload to external store
# Option A: S3/Azure Blob
az storage blob upload -f /tmp/session-store-snapshot.db \
  -c sessions -n "${TASK_ID}/${SESSION_ID}/session-store.db"
az storage blob upload -f /tmp/session-state.tar.gz \
  -c sessions -n "${TASK_ID}/${SESSION_ID}/session-state.tar.gz"

# Option B: Postgres (store DB as bytea, events as JSONB)
# psql -c "INSERT INTO session_archive ..."
```

**Phase 2: Pre-Execution Restore** (init container or entrypoint prologue)

When resurrecting a worker to continue previous work:

1. Download the archived `session-store.db` and `session-state/` for the target session
2. Place them in `~/.copilot/` before the bridge starts
3. The bridge calls `client.resumeSession(originalSessionId)` - the CLI finds the session in its local SQLite and continues

```bash
#!/bin/bash
# pre-task-restore.sh - runs as init container or entrypoint prologue
RESUME_SESSION_ID="${RESUME_SESSION_ID:-}"

if [ -z "$RESUME_SESSION_ID" ]; then
  echo "No session to restore - fresh start"
  exit 0
fi

TASK_ID="${ORIGINAL_TASK_ID:?}"

# Download from external store
mkdir -p ~/.copilot/session-state
az storage blob download -f ~/.copilot/session-store.db \
  -c sessions -n "${TASK_ID}/${RESUME_SESSION_ID}/session-store.db"
az storage blob download -f /tmp/session-state.tar.gz \
  -c sessions -n "${TASK_ID}/${RESUME_SESSION_ID}/session-state.tar.gz"

# Extract event stream
tar xzf /tmp/session-state.tar.gz -C ~/.copilot/session-state/

echo "Session ${RESUME_SESSION_ID} restored - bridge will resume on start"
```

**Phase 3: Postgres as Session Index** (query without full restore)

While the full session data lives as blobs (SQLite files + tarballs), a **Postgres index table** enables querying session metadata without restoring the full archive:

```sql
CREATE TABLE session_archive (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id         TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    repository      TEXT,
    branch          TEXT,
    summary         TEXT,
    agent           TEXT,
    model           TEXT,
    turn_count      INTEGER,
    status          TEXT NOT NULL DEFAULT 'completed',
    -- Checkpoint data (extracted from CLI's checkpoints table)
    last_checkpoint JSONB,  -- {title, overview, work_done, next_steps}
    -- Storage references
    store_db_ref    TEXT NOT NULL,  -- blob path to session-store.db
    event_tar_ref   TEXT NOT NULL,  -- blob path to session-state.tar.gz
    -- Metadata
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Searchable
    UNIQUE(task_id, session_id)
);

CREATE INDEX idx_session_archive_repo ON session_archive(repository);
CREATE INDEX idx_session_archive_agent ON session_archive(agent);
CREATE INDEX idx_session_archive_status ON session_archive(status);

-- Extracted turns for querying without full restore
CREATE TABLE turn_archive (
    id              BIGSERIAL PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES session_archive(session_id),
    turn_index      INTEGER NOT NULL,
    user_message    TEXT,
    assistant_response TEXT,
    timestamp       TIMESTAMPTZ,
    UNIQUE(session_id, turn_index)
);

-- Full-text search across archived sessions
CREATE INDEX idx_turn_archive_search ON turn_archive
    USING GIN (to_tsvector('english', coalesce(user_message, '') || ' ' || coalesce(assistant_response, '')));
```

This enables queries like:
- "Find all sessions where agent `coder-go` worked on the `auth` module" - metadata query, no blob restore
- "What was the last checkpoint for task X?" - `last_checkpoint` JSONB, no blob restore
- "Resume the session that implemented JWT auth" - query Postgres for `session_id`, then restore blobs

#### Bridge `state.db` Externalization Path

For the bridge's own `state.db`, direct Postgres replacement is feasible. The module boundary is clean:

```
src/state/store.ts
├── getDb()                    → pg.Pool.connect()
├── getChannelSession()        → SELECT ... (identical SQL)
├── setChannelSession()        → INSERT ... ON CONFLICT (identical SQL)
├── getChannelPrefs()          → SELECT ... (identical SQL)
├── addPermissionRule()        → INSERT ... (identical SQL)
├── recordAgentCall()          → INSERT ... (identical SQL)
├── ... (~60 functions total)
└── All queries are simple CRUD - no SQLite-specific features used
```

The only SQLite-specific elements are:
- `PRAGMA journal_mode = WAL` → not needed in Postgres
- `PRAGMA foreign_keys = ON` → Postgres enforces by default
- `datetime('now')` → `now()` in Postgres
- `better-sqlite3` synchronous API → needs async wrapper for `pg`

**Effort estimate**: Moderate. The data access layer is well-isolated in a single file. The harder part is the sync-to-async conversion - `better-sqlite3` is synchronous, `pg` is async. Every caller would need `await`.

#### Recommended Approach: Tiered Externalization

| Tier | Store | Target | When | Why |
|------|-------|--------|------|-----|
| **Tier 1** | CLI session state | Blob store (S3/Azure Blob) + Postgres index | Phase 2 (extract workers) | Enables session resurrection without modifying CLI |
| **Tier 2** | Bridge `state.db` | Postgres | Phase 3 (elastic factory) | Enables multi-bridge orchestrators sharing state |
| **Tier 3** | Beads task memory | Already external (Dolt) | Today | Already distributed - no change needed |

**Tier 1 is the priority** - it directly enables the resurrection use case. Tier 2 only matters when running multiple orchestrator bridge replicas (which need shared state). Tier 3 is already solved by Beads/Dolt.

#### Queue Envelope Extension for Resurrection

The A2A message envelope (Section 2) gains a `resume` field in metadata:

```json
{
  "metadata": {
    "resume": {
      "sessionId": "529750d1-8110-47ee-8521-2e87e9432e66",
      "taskId": "dark-factory-042",
      "checkpoint": 0,
      "archiveRef": "s3://sessions/dark-factory-042/529750d1.../session-state.tar.gz"
    }
  }
}
```

When a worker bridge receives a message with `metadata.resume`, the entrypoint:
1. Downloads and restores the archived session state (pre-task-restore.sh)
2. Starts the bridge, which calls `client.resumeSession(sessionId)`
3. The CLI finds the session in its local (restored) SQLite and continues with full conversation history

Without `metadata.resume`, the worker starts fresh - no restore step, no session overhead.

---

### 10. Alignment with Existing Research

This paper fills the gaps identified in prior research. Here's how the pieces connect:

| Prior Research | Gap Identified | Resolution in This Paper |
|---------------|---------------|-------------------------|
| [Architecture](dark-software-factory-architecture.md) | "Hybrid Architecture" section is a 20-line sketch | Full multi-bridge topology, CRDs, message envelopes |
| [Containerizing](containerizing-agent-runtimes.md) | "What goes inside the container?" - resolved runtime models | Runtime harness analysis: why minimal bridge > bare CLI |
| [Containerizing](containerizing-agent-runtimes.md) | "No multi-container orchestration pattern" | Operator CRDs define Pod composition declaratively |
| [Handoff](inter-agent-task-handoff.md) | "NOT implementing full A2A in v1" | Hybrid: use A2A data model, not A2A HTTP transport |
| [Handoff](inter-agent-task-handoff.md) | `delegate_task` schema is custom | Rebase on A2A `SendMessageRequest` format |
| [Broker](message-broker-selection.md) | "No migration path from ASB" | A2A envelope is transport-agnostic; swap queue, keep schema |
| [Broker](message-broker-selection.md) | "Hybrid broker strategy - NATS for dev, ASB for prod?" | Yes - A2A envelope abstracts over transport |

---

## Options & Trade-offs (Summary)

| Decision | Options | Recommendation | Phase |
|----------|---------|---------------|-------|
| Communication model | Queue / A2A / Hybrid / Proxy | **Proxy (D)** - queue dispatch + A2A HTTP via sidecar | **Now** |
| Message schema | Custom / A2A data model | **A2A data model** - native on HTTP, not custom binding | **Now** |
| Runtime harness | CLI / Minimal bridge / Full bridge | **Minimal bridge (workers), Full bridge (orchestrator)** | **Now** |
| Chat connectivity | All bridges / Gateway only | **Gateway only** - single bot, workers headless | **Now** |
| Git branching | Random / Convention | **`agent/<feature>/<agent>/<session-id>`** | **Now** |
| Deployment | Raw YAML / Helm / Custom operator | **Helm + KEDA ScaledJob** (operator deferred) | **Now** (Helm), Later (Operator) |
| Discovery | Hardcoded / HTTP AgentCards / CRD registry | **Static config (v1)** - JSON file mapping AgentCards to queue subjects | **Now** (static), Later (NATS events, then CRD) |
| Session persistence | Ephemeral / Snapshot-restore / Live Postgres | **Ephemeral** (snapshot-restore pending validation) | **Now** (ephemeral), Later (after CLI validation) |
| Bridge state | SQLite / Postgres | **SQLite** (Postgres when multi-replica) | **Now** (SQLite), Phase 3 (Postgres) |

---

## Recommendations

### Immediate - Build This Now

These items are implementable today with no upstream changes and no unvalidated assumptions.

1. **Use the Queue-to-A2A Proxy pattern (Option D)** - a lightweight Go sidecar in each worker pod reads from NATS JetStream, makes standard A2A HTTP calls to the bridge on localhost, and writes results back to the queue. This keeps A2A on its native transport (full spec compliance, SDK compatibility, AgentCard discovery) while the queue provides decoupling, buffering, and scale-to-zero. No upstream bridge changes needed - no custom `QueueAdapter`, no A2A queue binding. The proxy is ~300 lines of Go, generic enough to work with any A2A server.

2. **Use A2A's data model natively over HTTP** - the bridge exposes a standard A2A server endpoint. The proxy is a standard A2A client. SDKs (Go, JS, Python) handle serialization. No custom schema to maintain, no spec deviation.

3. **Use minimal bridge instances as workers** - not bare CLI containers. Design a `QueueAdapter` for copilot-bridge (alongside `MattermostAdapter` / `SlackAdapter`) that reads from NATS and executes tasks headlessly. The ~200MB image overhead is worth the `.agent.md` support, MCP orchestration, and session lifecycle.

4. **Implement the gateway connectivity model** - only the orchestrator bridge connects to Mattermost. Workers are headless queue consumers. Optional log observer bridge for `#factory-logs` during development.

5. **Use structured branch naming** - `agent/<feature>/<agent>/<session-id>` convention. Simple to implement, high ROI: enables retry detection via branch scan, historical lookup, and future integration with session archives if validated.

6. **Deploy via Helm + KEDA ScaledJob** - a Helm chart with parameterized ScaledJob templates, ConfigMaps for agent files, and NATS JetStream as a subchart. This is deployable today and covers 2-5 agent types without custom operator complexity.

7. **Align the `delegate_task` schema with A2A's `SendMessageRequest`** - rebase the existing handoff research on A2A's data model. Eliminates a maintenance burden and provides future interop.

### Deferred - Validate First, Build Later

These items require validation experiments or are premature at current scale. They are tracked in the risk register and will be promoted to immediate when their prerequisites are met.

8. **Session snapshot/restore and resurrection** *(deferred pending CLI validation experiment)* - The entire session externalization architecture (Section 8, R16, R17, R18) depends on an unvalidated assumption: that the Copilot CLI's `resumeSession()` works with a restored `session-store.db`. Before building Postgres tables, blob storage pipelines, and context-aware resurrection decision trees, run the validation experiment:

   ```bash
   # Backup a session
   tar czf /tmp/session-backup.tar.gz \
     ~/.copilot/session-store.db \
     ~/.copilot/session-state/<recent-session-uuid>/
   
   # Simulate container restore: remove and re-extract
   mv ~/.copilot/session-store.db ~/.copilot/session-store.db.bak
   mv ~/.copilot/session-state/<uuid> ~/.copilot/session-state/<uuid>.bak
   tar xzf /tmp/session-backup.tar.gz -C /
   
   # Attempt resume via bridge: /resume <uuid>
   ```

   **If it works** → promote Section 8 + R17 to implementation, build the Postgres archive and snapshot hooks. **If it doesn't** → simplify to checkpoint-summary-injection (fresh session with prior checkpoint text as initial context; no blob store needed).

9. **Kubernetes Operator with custom CRDs** *(deferred - premature at 2-3 agent types)* - The operator with `AgentRuntime`, `MCPServer`, `AgentCard`, and `TaskPipeline` CRDs is the right end-state for 10+ agent types needing dynamic reconfiguration. At current scale, Helm charts with KEDA ScaledJob templates provide the same functionality with a fraction of the maintenance burden. Revisit when the factory reaches 5+ agent types and manual Helm value changes become a bottleneck.

10. **Centralized session state store** *(deferred pending item 8 validation)* - The unified architecture (R17) with `branch_mapping`, `context_usage_log`, and `turn_archive` tables is valuable but depends on session restoration working. The branch naming convention (item 5) provides lightweight retry detection and search without requiring a centralized database. Promote if/when session restoration is validated.

11. **Bridge `state.db` Postgres migration** *(deferred - not needed until multi-replica orchestrator)* - Single orchestrator instance runs fine with SQLite. The `store.ts` module has a clean boundary for future migration, but the sync-to-async conversion is non-trivial. Defer until multi-replica deployment is a real requirement.

---

## Open Questions and Risk Register

Gaps and concerns identified during architecture review, organized by severity. Items marked 🔴 are blockers that must be resolved before implementation. Items marked 🟡 should be addressed during design. Items marked 🟢 are design-time considerations that can be deferred.

### Priority Classification (updated after architecture review)

Several items in this register depend on **deferred work** (session restoration, K8s operator, centralized store). These remain documented for completeness but are not on the immediate implementation path. Items actively blocking the "Immediate - Build This Now" recommendations are marked with ⚡.

| Risk | Status | Rationale |
|------|--------|-----------|
| **R1** | ⚡ Active - branch naming convention needed for v1 | Resolved: `agent/<feature>/<agent>/<session-id>` |
| **R2** | ⚡ Active - must validate before deploying workers | SIGTERM chain untested |
| **R3** | 🟢 Deferred - measurement exercise, not architectural | Test concurrent sessions when infra exists |
| **R4** | ⚡ Active - retry strategy needed for v1 | Simplified: new branch per retry, prior diff as context |
| **R5** | 🟢 Deferred - workers run autopilot in v1 | Revisit if human-in-the-loop tasks arise |
| **R6** | ⚡ Active - observability needed from day one | Trace ID in envelope, structured logging |
| **R7** | 🟢 Deferred - depends on session restoration validation | Only relevant if resurrection is implemented |
| **R8** | 🟢 Deferred - depends on SSE transport (unbuilt) | MCP stays stdio for now |
| **R9** | 🟢 Deferred - FIFO sufficient for v1 | Revisit if priority starvation observed |
| **R10** | 🟢 Deferred - operator itself is deferred | Helm charts don't have this risk |
| **R11** | 🟢 Deferred | Single org for v1 |
| **R12** | 🟢 Deferred | Measure after first deployment |
| **R13** | ⚡ Active - testing strategy needed for v1 | Contract tests + Docker Compose integration |
| **R14** | 🟢 Deferred - depends on session restoration | Only relevant if restoration is implemented |
| **R15** | 🟡 Near-term - needed shortly after v1 | Simple alert first, smarter handling later |
| **R16** | 🟢 Deferred - depends on centralized store | Branch naming provides lightweight search without DB |
| **R17** | 🟢 Deferred - depends on session restoration validation | Promote if CLI restoration experiment succeeds |
| **R18** | 🟢 Deferred - depends on R17 | Promote if centralized store is built |

### 🔴 Critical - Could Block the Architecture

#### R1: Git Conflict Resolution in Fan-Out

Multiple workers operating on the same repo concurrently is a core factory capability, but the merge strategy is undesigned. When the orchestrator fans out `implement → test → docs` and workers push to related branches:

- **Same-branch collision**: Two workers assigned to the same branch (misconfiguration or retry) will `git push` race. Second push fails.
- **Cross-branch file overlap**: Coder writes `auth.go`, tester writes `auth_test.go` - no conflict. But if both modify `go.mod` or `main.go`, the PR merge conflicts.
- **Fan-in merge order**: When all subtasks complete, who merges? In what order? Does the orchestrator create a single PR from all branches, or one PR per worker?

**Recommended: Option A with structured branch naming**

Each worker gets a fully isolated branch. The branch name encodes enough metadata to support failure recovery, session resurrection, and historical search:

```
agent/<feature>/<agent-name>/<session-id>

Examples:
  agent/jwt-auth/coder/529750d1
  agent/jwt-auth/tester/a9aca840
  agent/jwt-auth/docs-writer/07457e78
  agent/jwt-auth/coder/16c04794      ← retry: same feature+agent, new session
```

**Naming components and their purpose:**

| Segment | Source | Purpose |
|---------|--------|---------|
| `agent/` | Convention | Namespace - distinguishes factory branches from human branches |
| `<feature>` | Task envelope `metadata.branch` or orchestrator-assigned slug | Groups all branches for one feature; supports fan-in merge |
| `<agent-name>` | Task envelope `configuration.agent` | Identifies which agent type produced this work |
| `<session-id>` | First 8 chars of CLI-generated session UUID | Uniqueness per attempt; links branch → archived session for resurrection |

**Why session ID in the branch name matters:**

1. **Failure recovery**: When a retried worker sees `agent/jwt-auth/coder/529750d1` exists, it can look up session `529750d1...` in the session archive (Postgres index) to retrieve the conversation context, understand what the previous attempt did, and decide whether to continue or start fresh. *(Cross-ref: Section 8 - Session State Externalization)*

2. **Historical search**: `git branch -r --list 'agent/jwt-auth/*'` shows all attempts across all agents for a feature. Combined with the session archive, you can query: "what happened in the coder's second attempt on jwt-auth?" by resolving the session ID to archived turns and tool calls. *(Cross-ref: R16 - Session-Aware Branch Search)*

3. **Context exhaustion recovery**: When a resurrected session exceeds the context window (R7), the orchestrator can start a *new* session on a *new* branch (`agent/jwt-auth/coder/<new-session-id>`) while injecting the previous session's checkpoint summary as initial context. The old branch and session remain as searchable history. *(Cross-ref: R7 - Context Window Exhaustion)*

4. **Idempotency**: Retry detection becomes a branch listing operation - if `agent/jwt-auth/coder/*` already has branches, the orchestrator knows prior attempts exist and can enrich the retry prompt. *(Cross-ref: R4 - Worker Idempotency)*

**Fan-in merge strategy:**

```
                    agent/jwt-auth/coder/529750d1
                   /
main ─────────────┤── agent/jwt-auth/tester/a9aca840
                   \
                    agent/jwt-auth/docs-writer/07457e78
                   
Orchestrator creates: PR "feature/jwt-auth" merging all three
  - Base: main
  - Uses GitHub's merge queue for conflict resolution
  - If merge conflicts: orchestrator dispatches a "resolve conflicts" task
    to a coder agent with all three branches as context
```

**Questions resolved:**
- ✅ Branch naming guarantees uniqueness: feature + agent + session ID
- ✅ Retry detection: list branches matching `agent/<feature>/<agent>/*`
- ✅ Orchestrator creates merge PR; GitHub merge queue handles conflicts

**Questions remaining:**
- Should the orchestrator detect likely file overlaps *before* fan-out (via static analysis or file-touch prediction)?
- What's the policy for stale branches? Auto-delete after N days? After PR merge?

#### R2: Graceful Shutdown - Bridge Signal Propagation Chain

The containerizing paper designed SIGTERM handling for bare CLI containers. But the recommended bridge harness introduces a process tree:

```
KEDA/K8s sends SIGTERM
    → Node.js (copilot-bridge process)
        → Copilot CLI (child process, spawned by SDK)
            → Potentially: MCP server child processes
```

**Unvalidated assumptions:**
- Does the bridge's Node.js process forward SIGTERM to the CLI child?
- Does the CLI commit partial work on SIGTERM, or just exit?
- If the CLI is mid-LLM-call (waiting for API response), SIGTERM during I/O may leave no committable state.
- MCP server sidecar containers receive independent SIGTERM from K8s - do they shut down before the agent finishes its last tool call?
- KEDA's `terminationGracePeriodSeconds` default (30s) may be insufficient for commit + push.

**Questions to resolve:**
- Validate SIGTERM propagation from bridge → CLI with a test harness
- Design the shutdown sequence: stop accepting new tool calls → finish current call → commit → push → ack/nack message → exit
- Set `terminationGracePeriodSeconds` based on measured commit+push latency

#### R3: Authentication Lifecycle at Scale

Three credential types are in play, each with different lifecycles:

| Credential | Lifetime | Refresh | Concern |
|-----------|----------|---------|---------|
| GitHub App installation token | 1 hour | Refresh via App API | 10-min task is fine; 45-min complex task may expire mid-push |
| Copilot subscription validation | Unknown | Unknown - CLI handles internally | Does each concurrent CLI instance consume a seat? Rate limits per org? |
| Mattermost bot token | Long-lived | Manual rotation | Only orchestrator - low risk |

**The seat question is critical**: If a GitHub org has 10 Copilot Business seats, can it run 50 concurrent bridge instances under one bot user? Or does each concurrent `client.createSession()` consume a seat? This determines whether the factory is economically viable at scale.

**Questions to resolve:**
- Test concurrent session limits under a single Copilot subscription
- Measure API rate limits (requests/min) with N concurrent CLI instances
- Design token refresh for long-running tasks (GitHub App token renewal mid-task)
- Determine if Copilot seats are per-user or per-concurrent-session

#### R4: Worker Idempotency on Retry

**Related to: R1 (branch naming), R16 (session search), R17 (centralized store), Section 8 (session externalization)**

When Worker A dies mid-task and NATS redelivers the message to Worker B:

```
Worker A:  clone → branch → implement (50%) → SIGTERM → push partial → exit(1)
           Message un-acked → redelivered after ack_wait
Worker B:  clone → branch exists! → ??? → implement → push → exit(0)
```

**Failure modes:**
- Worker B does `git checkout -b <branch>` - fails because branch exists remotely with partial work
- Worker B does `git checkout <branch>` - inherits A's partial commits, but the LLM has no context of what A did
- Worker B does `git push --force` - overwrites A's partial work, potentially losing recoverable state
- Worker B starts fresh on a new branch - now two branches exist for the same task

**Resolution using R1 branch naming + R17 centralized store:**

With the structured branch naming convention (`agent/<feature>/<agent>/<session-id>`), retry detection becomes a two-step lookup:

1. **Branch scan**: `git ls-remote --heads origin 'agent/<feature>/<agent>/*'` reveals prior attempts
2. **Session lookup**: For each prior branch, extract the session ID from the branch name → query R17's `branch_mapping` table for attempt count, status, and context usage → query `session_archive` for checkpoint summary

The retry worker then starts on a **new branch** (`agent/<feature>/<agent>/<new-session-id>`) with the previous attempt's diff injected as context in the prompt. Prior branches are preserved as history - never overwritten, never force-pushed.

**Envelope extension for retry awareness:**

```json
{
  "metadata": {
    "attempt": 2,
    "max_attempts": 3,
    "prior_attempts": [
      {
        "session_id": "529750d1",
        "branch": "agent/jwt-auth/coder/529750d1",
        "status": "failed",
        "partial_diff_ref": "s3://sessions/.../partial.diff"
      }
    ]
  }
}
```

**Questions remaining:**
- Should `max_deliver` be low (2-3) with DLQ escalation, or higher with diminishing returns?
- Should the orchestrator (not the worker) handle retry enrichment - querying prior attempts and augmenting the prompt before re-enqueuing?

#### R5: Human-in-the-Loop for Headless Workers

Headless workers have no chat connection. But several scenarios require human input:

- **Permission prompts**: Worker needs to run a destructive command (`rm -rf`, database migration). Bridge's `onPermissionRequest` callback has no UI to show.
- **Clarification**: Task is ambiguous. The `ask_user` tool has no channel to ask on.
- **Approval gates**: PR needs human review before the next pipeline step proceeds.
- **A2A `input-required` state**: The protocol supports this, but the transport path (worker → queue → orchestrator → Mattermost → human → Mattermost → orchestrator → queue → worker) is a full round-trip through the queue.

**Questions to resolve:**
- Should workers run in full `--yolo` / `autopilot` mode (no permission prompts)?
- If not, design the input-required relay: worker publishes `input-required` status → orchestrator posts to Mattermost → human responds → orchestrator enqueues response → worker resumes
- How long can a worker block waiting for human input before ack_wait expires?
- Should `input-required` tasks be evicted from workers and re-queued after human responds (to avoid holding a pod idle)?

### 🟡 Important - Should Be Addressed Before Implementation

#### R6: Observability and Distributed Tracing

No mention of OpenTelemetry, structured logging, or trace correlation. In a distributed system with orchestrator → queue → worker → GitHub, debugging a failed task requires correlating logs across 4 systems.

**Needed:**
- Trace ID in the A2A envelope, propagated through NATS message headers, into worker logs, and onto git commits
- Structured JSON logging with consistent fields (`task_id`, `session_id`, `worker_id`, `trace_id`)
- Centralized log aggregation (Loki, Elasticsearch, Azure Monitor)
- Metrics: task latency, queue depth, worker utilization, failure rate per agent type
- Dashboard: Grafana or equivalent showing factory health

#### R7: Context Window Exhaustion on Resurrection

**Related to: R18 (context-aware resurrection strategy), R17 (centralized store), R1 (branch naming), Section 8 (session externalization)**

The bridge tracks `contextUsage` (currentTokens / tokenLimit). A resurrected session with 200+ turns may immediately exceed the context window, causing the CLI to either truncate history or fail.

**Scenarios:**
- Resurrected session with 500 turns of history → context window full before the new prompt fits
- Complex multi-file task accumulated 100K tokens of tool output → no room for continuation prompt
- Model downgrade on resurrection (Opus → Sonnet) reduces context window → previous session doesn't fit

**Resolution**: R18 defines a context-aware resurrection decision tree that queries R17's `context_usage_log` before choosing between full restoration, checkpoint-based resurrection, or fresh start with diff injection. The R1 branch naming strategy ensures that each resurrection attempt gets its own branch while preserving prior branches as searchable history.

**Questions remaining:**
- Can the CLI's `resumeSession` handle context overflow gracefully (auto-truncate oldest turns)?
- What's the threshold for "too expensive to resurrect" - 80%? 90%? Does it vary by model?
- Should the `context_usage_log` be populated via `postToolUse` hook (real-time) or `sessionEnd` hook (end-of-session snapshot)?

#### R8: MCP Server Failure and Reconnection

Shared MCP servers (SSE transport) serve multiple workers simultaneously. Failure modes:

- MCP server pod restarts → all connected workers lose tool access mid-task
- MCP server returns errors → workers retry? fail? fall back to embedded tools?
- SSE connection drops → does the bridge's MCP client auto-reconnect?

**Note:** copilot-bridge currently only supports `stdio` MCP transport. SSE transport is itself an unresolved prerequisite (flagged in containerizing research). The failure handling design depends on the SSE client implementation that doesn't exist yet.

#### R9: Queue Priority Implementation

The A2A envelope includes `"priority": "high"` but NATS JetStream doesn't natively support priority queues. Options:

| Approach | Complexity | Trade-off |
|----------|-----------|-----------|
| Separate subjects per priority (`agent.tasks.coder.high`, `.normal`, `.low`) | Low | Separate KEDA triggers per priority; high-priority gets dedicated workers |
| Consumer-side priority sorting | Medium | Single subject, consumer peeks and reorders; adds latency |
| Priority header + consumer filter | Medium | NATS headers + custom consumer logic |

**Questions to resolve:**
- Is priority needed in v1, or is FIFO sufficient?
- If priority subjects: should high-priority tasks get dedicated (warm) workers?

#### R10: Operator Blast Radius and Rollback

A reconciliation bug in the custom operator could:
- Delete running ScaledJobs (workers killed mid-task)
- Misconfigure NATS subjects (messages routed to wrong workers)
- Orphan worker pods (no KEDA trigger to scale down)
- Corrupt the AgentCard registry (orchestrator routes to nonexistent agents)

**Mitigations to design:**
- Dry-run reconciliation mode (log changes without applying)
- Canary reconciliation (apply to one agent type, verify, then roll out)
- Owner references on all created resources (garbage collection on CRD delete)
- Reconciliation rate limiting (don't process all CRD changes simultaneously)

### 🟢 Design-Time Concerns

#### R11: Multi-Tenancy and Org Isolation

Can the factory serve multiple GitHub orgs or repos? Considerations:
- Copilot subscriptions are org-scoped - cross-org workers need separate credentials
- Kubernetes namespaces provide isolation, but NATS subjects are cluster-wide unless using accounts
- AgentCard CRDs in shared namespace could leak capability info across tenants

#### R12: Cost Model and Break-Even Analysis

At Copilot Enterprise pricing ($39/user/month) × N concurrent workers × AKS compute hours, the factory has a non-trivial operating cost. Compare against:
- GitHub's native Coding Agent (free with Copilot subscription, limited to single-repo)
- Manual developer time for the same tasks
- Break-even point: at what task volume does the factory pay for itself?

#### R13: Factory Integration Testing

How to validate the full pipeline in CI:
- Mock Copilot API for deterministic tests?
- Docker Compose integration test (orchestrator + NATS + one worker)?
- Chaos testing: kill worker mid-task, verify message redelivery and retry?
- Contract testing: validate A2A envelope schema between orchestrator and workers?

#### R14: CLI Version Skew on Session Restore

Session archived by CLI v0.0.421, restored into container running v0.0.425. If the CLI applies SQLite migrations on startup, the restored `session-store.db` may be migrated in-place (safe) or may fail if the schema change is backward-incompatible.

**Mitigations:**
- Pin CLI version in container images and tag archives with the CLI version
- Test restore across version pairs before upgrading worker images
- Maintain a version compatibility matrix

#### R15: Dead Letter Queue Consumer

NATS `max_deliver` sends failed messages to a dead-letter subject, but no consumer is designed:
- Alert on DLQ depth (PagerDuty, Slack notification)?
- Auto-retry with different agent or model?
- Escalate to human via Mattermost with failure context?
- Expire after N days?

#### R16: Session-Aware Branch Search and Historical Lookup

**Related to: R1 (branch naming), Section 8 (session externalization), R7 (context exhaustion)**

The structured branch naming strategy (`agent/<feature>/<agent>/<session-id>`) creates a linkage between Git history and archived session state. This unlocks a search capability: given a branch, look up the full conversation, tool calls, and decisions that produced the commits on that branch.

**Components needed:**
- **Branch → session index**: Parse branch name to extract session ID → query Postgres `session_archive` table for full metadata, turn count, checkpoint, and blob references
- **Session → branch index**: Given a session ID, find which branch(es) it operated on → query `session_archive.branch` or scan `git branch -r --list 'agent/*/*/<session-id-prefix>*'`
- **Beads/Dolt integration**: Store session-to-branch mappings in Beads (`bd remember "session 529750d1 produced agent/jwt-auth/coder/529750d1, PR #42"`) for cross-session searchability via `bd search`
- **Search interface**: Orchestrator (or human via chat) queries: "show me all sessions that touched the auth module" → search `turn_archive` FTS index + `session_files` for file paths matching `*auth*`

**Relationship to other features:**
- Depends on: Section 8 (session archive must exist in Postgres with searchable turns)
- Enables: R4 (idempotency - retry detection via branch listing), R7 (context exhaustion - find prior session's checkpoint by branch name), R1 (fan-in merge - discover all branches for a feature)
- Extends: Beads task memory (current `bd remember` captures decisions; this adds structured session-to-artifact linkage)

#### R17: Centralized Session State Store - Unified Architecture

**Related to: Section 8 (session externalization), R7 (context exhaustion), R14 (CLI version skew), R16 (branch search)**

Section 8 designs session archival as a snapshot/restore pattern with Postgres index + blob storage. R16 adds search. R7 adds context-aware resurrection. These are described as separate concerns, but they converge on a single architectural component: a **centralized session state store** that serves as the factory's long-term memory.

**Unified design:**

```
┌──────────────────────────────────────────────────────────────────┐
│                 CENTRALIZED SESSION STATE STORE                   │
│                                                                   │
│  ┌────────────────────┐   ┌────────────────────┐                 │
│  │ Postgres           │   │ Blob Store         │                 │
│  │                    │   │ (S3 / Azure Blob)  │                 │
│  │ session_archive    │   │                    │                 │
│  │ turn_archive       │   │ session-store.db   │                 │
│  │ branch_mapping     │←──│ events.jsonl       │                 │
│  │ checkpoint_index   │   │ workspace.yaml     │                 │
│  │ context_usage_log  │   │ checkpoint files   │                 │
│  │                    │   │                    │                 │
│  │ FTS on turns       │   │ Keyed by:          │                 │
│  │ GIN on metadata    │   │ {task_id}/          │                 │
│  │                    │   │  {session_id}/      │                 │
│  └────────┬───────────┘   └────────────────────┘                 │
│           │                                                       │
│  ┌────────┴───────────┐   ┌────────────────────┐                 │
│  │ Beads / Dolt       │   │ Git Branches       │                 │
│  │                    │   │                    │                 │
│  │ Task memory        │   │ agent/<feature>/   │                 │
│  │ Decision log       │←──│  <agent>/          │                 │
│  │ Session→branch map │   │   <session-id>     │                 │
│  │ Cross-session facts│   │                    │                 │
│  └────────────────────┘   └────────────────────┘                 │
│                                                                   │
│  Consumers:                                                       │
│  • Orchestrator: query before dispatch (retry detection, context) │
│  • Workers: restore on resurrection, archive on completion        │
│  • Humans: search via chat ("show sessions for auth module")     │
│  • CI/CD: look up which session produced a given PR               │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

**New tables (extending Section 8's schema):**

```sql
-- Links branches to sessions (populated by worker on branch creation)
CREATE TABLE branch_mapping (
    id              BIGSERIAL PRIMARY KEY,
    session_id      TEXT NOT NULL,
    task_id         TEXT NOT NULL,
    branch_name     TEXT NOT NULL,    -- e.g. "agent/jwt-auth/coder/529750d1"
    repository      TEXT NOT NULL,    -- e.g. "org/api-service"
    feature_slug    TEXT NOT NULL,    -- e.g. "jwt-auth" (extracted from branch)
    agent_name      TEXT NOT NULL,    -- e.g. "coder" (extracted from branch)
    attempt         INTEGER NOT NULL DEFAULT 1,
    status          TEXT NOT NULL DEFAULT 'active',
    pr_number       INTEGER,         -- Set when PR is created
    pr_url          TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    merged_at       TIMESTAMPTZ,
    UNIQUE(branch_name, repository)
);

CREATE INDEX idx_branch_mapping_feature ON branch_mapping(repository, feature_slug);
CREATE INDEX idx_branch_mapping_session ON branch_mapping(session_id);
CREATE INDEX idx_branch_mapping_agent ON branch_mapping(agent_name);

-- Context usage snapshots (populated by sessionEnd hook or postToolUse)
CREATE TABLE context_usage_log (
    id              BIGSERIAL PRIMARY KEY,
    session_id      TEXT NOT NULL,
    turn_index      INTEGER NOT NULL,
    current_tokens  INTEGER NOT NULL,
    token_limit     INTEGER NOT NULL,
    usage_pct       NUMERIC(5,2) GENERATED ALWAYS AS
                    (current_tokens::numeric / NULLIF(token_limit, 0) * 100) STORED,
    model           TEXT,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_context_usage_session ON context_usage_log(session_id);
```

**Queries this enables:**

```sql
-- "What happened in all prior attempts on jwt-auth?"
SELECT bm.branch_name, bm.attempt, bm.agent_name, sa.summary,
       sa.turn_count, bm.pr_number, bm.status
FROM branch_mapping bm
JOIN session_archive sa ON sa.session_id = bm.session_id
WHERE bm.feature_slug = 'jwt-auth' AND bm.repository = 'org/api-service'
ORDER BY bm.attempt;

-- "Is this session safe to resurrect, or will it exhaust context?"
SELECT cu.current_tokens, cu.token_limit, cu.usage_pct, cu.model
FROM context_usage_log cu
WHERE cu.session_id = '529750d1-...'
ORDER BY cu.turn_index DESC LIMIT 1;

-- "Find all sessions where the coder agent touched auth files"
SELECT DISTINCT sa.session_id, sa.summary, bm.branch_name
FROM session_archive sa
JOIN session_files sf ON sf.session_id = sa.session_id
JOIN branch_mapping bm ON bm.session_id = sa.session_id
WHERE sf.file_path LIKE '%auth%' AND bm.agent_name = 'coder';
```

**Relationship to other features:**
- Unifies: Section 8 (archive), R16 (search), R7 (context awareness)
- Depends on: Hooks infrastructure (sessionStart/sessionEnd for archive triggers)
- Enables: R4 (idempotency - query prior attempts before retry), R1 (branch naming - structured metadata extraction), R14 (version tracking - store CLI version alongside archived session)
- Extends: Beads memory (structured facts supplement unstructured `bd remember` entries)

#### R18: Context-Aware Resurrection Strategy

**Related to: R7 (context exhaustion), R17 (centralized store), R1 (branch naming), Section 8 (session externalization)**

When the orchestrator dispatches a resurrection task, it should query the centralized store to determine the optimal resurrection strategy - not blindly restore the full session.

**Decision tree:**

```
Orchestrator receives continuation request for feature "jwt-auth"
    │
    ├─ Query: SELECT context_usage, turn_count FROM session_archive
    │         WHERE feature = 'jwt-auth' AND agent = 'coder'
    │         ORDER BY created_at DESC LIMIT 1
    │
    ├─ context_usage < 60%?
    │   └─ YES → Full resurrection
    │         Restore session-store.db + events.jsonl
    │         resumeSession(originalId)
    │         Branch: reuse existing agent/jwt-auth/coder/<session-id>
    │
    ├─ context_usage 60-90%?
    │   └─ PARTIAL → Checkpoint resurrection
    │         Start NEW session (fresh context window)
    │         Inject last checkpoint as initial context:
    │           "Previous work summary: <checkpoint.overview>
    │            Work completed: <checkpoint.work_done>
    │            Continue from: <checkpoint.next_steps>"
    │         Branch: new agent/jwt-auth/coder/<new-session-id>
    │         Old branch remains as history
    │
    └─ context_usage > 90%?
        └─ FRESH → New session with diff context
              Start NEW session
              Inject only the git diff from previous branch:
                "A previous attempt produced these changes:
                 <git diff main..agent/jwt-auth/coder/old-session>
                 Continue and complete the implementation."
              Branch: new agent/jwt-auth/coder/<new-session-id>
```

**Relationship to other features:**
- Depends on: R17 (context_usage_log must be populated), Section 8 (checkpoint data in archive)
- Resolves: R7 (context exhaustion - chooses strategy based on measured usage)
- Uses: R1 branch naming (old branch preserved as history, new branch for continuation)
- Feeds: R16 (search - both old and new sessions are indexed, linked by feature_slug)

---

## Follow-up Research

- [ ] **Queue-to-A2A proxy prototype** - Build the Go sidecar (~300 lines) that reads from NATS JetStream, POSTs to a local A2A server, consumes SSE status events, and publishes results back to the queue. Test with a mock A2A server first, then with copilot-bridge.
- [ ] **copilot-bridge A2A server mode** - Determine how to run copilot-bridge as an A2A server. Does the bridge already expose HTTP endpoints, or does it need a thin wrapper? Alternatively, can the proxy call the bridge's SDK/RPC interface directly instead of HTTP?
- [ ] **Kubernetes Operator prototype** - Scaffold a Go-based operator using kubebuilder or operator-sdk. Start with the `AgentRuntime` CRD and reconciler that creates KEDA ScaledJobs.
- [ ] **Multi-bridge integration test** - Deploy orchestrator + one worker bridge + NATS via Docker Compose. Dispatch a task via chat, observe queue-based execution, verify result delivery back to chat.
- [ ] **Cost model for concurrent bridge instances** - Measure Copilot API rate limits, token consumption, and compute costs for 5, 10, 20 concurrent bridge instances under a single GitHub org.
- [ ] **TaskPipeline execution engine** - Design the orchestrator's logic for fan-out/fan-in, dependency resolution, and result merging based on `TaskPipeline` CRD definitions.
- [ ] **AgentCard-based skill matching** - Design the algorithm for matching a task description to AgentCard skills. Consider keyword matching, embedding-based similarity, and LLM-based classification.
- [ ] **Session snapshot/restore prototype** - Build the `post-task-snapshot.sh` and `pre-task-restore.sh` scripts. Test round-trip: worker completes task → archives session → new worker restores → `client.resumeSession()` succeeds with full history. Validate with Azure Blob Storage and Postgres index.
- [ ] **Postgres session index schema** - Implement the `session_archive` and `turn_archive` tables. Build the archival pipeline that extracts metadata from `session-store.db` into Postgres and uploads blobs to object storage. Validate search queries across archived sessions.
- [ ] **Bridge state.db async migration spike** - Prototype replacing `better-sqlite3` with `pg` in `store.ts`. Measure blast radius of sync-to-async conversion. Determine if a compatibility shim (sync wrapper over async `pg`) is viable for incremental migration.
- [ ] **Resurrection end-to-end test** - Dispatch a task, let worker complete, archive session. Dispatch a follow-up task with `metadata.resume` pointing to the archived session. Verify the resurrected worker has full conversation context and can continue where the previous worker left off.

---

## References

- [Dark Software Factory Architecture](dark-software-factory-architecture.md) - Master architecture doc, manufacturing analogy, hybrid architecture sketch
- [Containerizing Agent Runtimes](containerizing-agent-runtimes.md) - Three runtime models, hybrid recommendation, phased implementation
- [Inter-Agent Task Handoff](inter-agent-task-handoff.md) - `delegate_task` proposal, A2A pattern analysis, task lifecycle design
- [Message Broker Selection](message-broker-selection.md) - NATS JetStream recommendation, lock mechanism comparison
- [KEDA ScaledJob Patterns](keda-scaledjob-patterns.md) - Scaling primitives, ScaledJob vs ScaledObject, queue-driven autoscaling
- [A2A Protocol Specification v1.0.0](https://a2a-protocol.org/latest/specification/) - Canonical data model, operations, protocol bindings
- [A2A GitHub Repository](https://github.com/a2aproject/A2A) - SDKs (Go, Python, JS, Java, .NET), samples, contributing guide
- [copilot-bridge](https://github.com/ChrisRomp/copilot-bridge) - Bridge runtime, MCP server management, chat adapters
