# Daedalus - Implementation Plan

## Overview

Daedalus is a Kubernetes-native platform that orchestrates ephemeral AI agent workers. It connects a chat-facing orchestrator (copilot-bridge on Mattermost) to headless worker bridges via NATS JetStream, using A2A protocol for structured agent communication and KEDA for elastic scaling.

This plan covers Phase 0 (foundation) through the first deployable system. Later phases (session resurrection, K8s operator, multi-tenancy) are tracked as deferred items in the [research risk register](../research/hybrid-comms-architecture.md#open-questions-and-risk-register).

## Guiding Principles

- Build the simplest thing that validates the architecture
- **ACP for intra-pod** (proxy to agent) - the agent CLIs already speak it
- **A2A for inter-agent** (orchestrator to workers via queue) - standardized task model
- Queue provides decoupling - proxy bridges NATS queue to ACP agent
- **Runtime-agnostic** - any ACP-compatible agent CLI plugs in (Copilot, Claude, Codex, Gemini, 17+ others)
- Layer 2 wrappers (copilot-bridge, acpx) are optional, not required
- Helm + KEDA for deployment (no custom operator yet)
- Static AgentCard config for discovery (dynamic registration deferred)
- See [architecture-layers.md](architecture-layers.md) for the full four-layer model

---

## Phase 0 - Foundation and Validation

Goal: validate the core primitives individually before wiring them together.

### 0.1 Queue-to-ACP Proxy prototype
Build the proxy sidecar that reads from NATS JetStream and drives an agent via ACP. The proxy speaks A2A externally (queue side) and ACP internally (agent side).

**Deliverables:**
- Go module in `cmd/proxy/`
- NATS consumer with ack/nack handling (A2A envelope as queue message format)
- ACP client: `initialize`, `session/new`, `session/prompt`, `session/cancel`
- ACP `session/update` consumer that forwards status to `agent.status` subject
- ACP `session/request_permission` handler (auto-approve for v1, relay for v2)
- Result publisher to `agent.results` subject (A2A Task format)
- Unit tests with mock ACP agent
- Dockerfile for the proxy (~20MB alpine-based image)
- Evaluate acpx as a dependency vs. building ACP client from scratch

### 0.2 Validate Copilot CLI as ACP agent
Copilot CLI supports `copilot --acp --port 3000` (TCP mode). Validate that our proxy can drive it end-to-end without copilot-bridge as an intermediary.

**Deliverables:**
- Start Copilot CLI in ACP TCP mode inside a container
- Proxy connects, creates session, sends prompt, receives streamed response
- Validate MCP server passthrough (proxy passes mcpServers[] in session/new)
- Test session/load for resume (if Copilot CLI supports loadSession capability)
- Document which ACP capabilities Copilot CLI advertises
- Compare with claude --acp and codex --acp to confirm runtime-agnostic behavior

### 0.3 Docker Compose integration test
Wire proxy + agent CLI + NATS in a single Docker Compose stack. Validate the full loop: publish A2A task to queue, proxy dequeues, proxy drives agent via ACP, result flows back through queue.

**Deliverables:**
- `docker-compose.yml` with NATS (JetStream enabled), bridge worker, proxy sidecar
- Test script that publishes A2A SendMessageRequest to queue and validates result on `agent.results`
- Mock task: "create a file called hello.txt with the contents 'hello world'"
- Document measured latencies: queue to proxy to bridge to result

### 0.4 SIGTERM/graceful shutdown validation (R2)
Test the signal propagation chain: K8s SIGTERM to Node.js bridge to Copilot CLI child process. Determine whether the bridge needs a custom shutdown handler.

**Deliverables:**
- Test harness: start bridge in Docker, send SIGTERM, observe behavior
- Document what happens to in-flight CLI operations
- If needed: implement shutdown handler (stop new work, wait for current, nack message)
- Set `terminationGracePeriodSeconds` recommendation

### 0.5 Structured branch naming convention (R1)
Implement the `agent/<feature>/<agent>/<session-id>` branch naming in the worker entrypoint. Validate retry detection via branch listing.

**Deliverables:**
- Entrypoint script or bridge configuration that creates branches with structured names
- Retry detection: if prior branches exist for same feature+agent, inject diff as context
- Test: run task, kill worker, rerun task, verify new branch + prior work detected

### 0.6 Static AgentCard registry
Create the static JSON config file mapping AgentCards to queue subjects. Wire into orchestrator bridge for task routing.

**Deliverables:**
- `deploy/config/agent-registry.json` with 2-3 sample agent definitions
- Orchestrator reads registry on startup, routes tasks by skill match
- Test: send "implement X" to orchestrator, verify it publishes to correct queue subject

---

## Phase 1 - Deployment and Scaling

Goal: deploy to Kubernetes with KEDA-based autoscaling.

### 1.1 Helm chart
- Worker bridge deployment (one per agent type)
- NATS JetStream subchart
- KEDA ScaledJob per agent type
- ConfigMaps for agent files and registry
- Proxy sidecar in pod spec

### 1.2 KEDA ScaledJob configuration
- Scale-to-zero for workers
- ack_wait aligned with task timeout
- Queue depth triggers
- terminationGracePeriodSeconds from 0.4 findings

### 1.3 Observability stack (R6)
- Trace ID in A2A envelope metadata
- Structured JSON logging (proxy + bridge)
- NATS message headers with W3C Trace Context
- Git commit trailers with Trace-ID
- Grafana + Loki (or equivalent) for log aggregation

### 1.4 Contract tests (R13)
- JSON Schema validation for A2A envelope
- Queue subject naming validation
- AgentCard schema validation
- CI pipeline (GitHub Actions)

---

## Phase 2 - Multi-Agent and Fan-Out

### 2.1 Fan-out task dispatch

Dispatch a compound task to multiple agents in parallel. The orchestrator receives a list of explicit skill/prompt pairs and routes each to the appropriate agent via NATS.

**Deliverables:**
- Structured input model: `TaskSpec` struct with explicit skill ID, prompt, and optional metadata
- `Orchestrator` type in `internal/orchestrator/` that accepts `[]TaskSpec`, routes each to the right agent via Registry skill matching, and publishes `SendMessageRequest` per task to the agent's NATS subject
- `Dispatcher` in `internal/dispatch/` that handles the actual NATS publishing and status tracking
- Result collector that subscribes to `agent.results.<taskId>` subjects and aggregates responses
- Unit tests with mock NATS (embedded server from test infrastructure)

### 2.2 Dependency-ordered execution (implement, then test, then docs)
### 2.3 Result merging and PR creation

### 2.4 Dynamic AgentCard registry (NATS events)

Replace the static JSON registry with a live NATS JetStream KV store. Agents self-register on startup and renew via heartbeat. The orchestrator watches for changes in real time.

**Deliverables:**
- NATS JetStream KV store for AgentCards (bucket: `agent-cards`, key: agent name, value: JSON AgentCard)
- `DynamicRegistry` in `internal/registry/dynamic.go` that wraps NATS KV and implements the same lookup interface as the static `Registry` (FindBySkill, FindByTag, FindByName, Route, RouteByScore, All)
- KV Watch for real-time updates (agent registration/departure)
- TTL-based heartbeat expiry (configurable, default 30s)
- Fallback: loads static registry file, then overlays dynamic entries
- Unit tests with embedded NATS server

### Phase 2 Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Task splitting (2.1) | Structured input (`[]TaskSpec`) | Deterministic, testable, no LLM dependency in orchestrator hot path. The orchestrator receives explicit skill/prompt pairs. LLM-based compound prompt analysis deferred to v2 as an optional pre-processing step. |
| Dynamic registry transport (2.4) | NATS JetStream KV store | Purpose-built for this use case: key=agent name, value=AgentCard JSON. Built-in Watch API for real-time updates eliminates custom pub/sub. TTL support provides heartbeat expiry without custom protocol. The existing static registry becomes the bootstrap/fallback layer. |
| Registry interface (2.4) | Same interface, new implementation | `DynamicRegistry` implements the same lookup methods as the static `Registry`. Callers (orchestrator, proxy) don't need to know whether discovery is static or dynamic. This is a clean extension, not a rewrite. |
| DAG representation (2.2) | TBD | Will be decided when Batch 2 starts. Candidates: adjacency list (simple), or topological sort with in-degree tracking. |
| Merge strategy (2.3) | TBD | Will be decided when Batch 3 starts. Candidates: octopus merge, sequential cherry-pick, or rebase-based. |
| PR creation tool (2.3) | TBD | `gh` CLI vs Go GitHub client. |

---

## Phase 3 - Pluggable Runtime and Operator

Goal: make the platform runtime-agnostic and declaratively managed via K8s CRDs.

### Architectural Insight: The Two-Layer Container Model

The proxy sidecar pattern from Phase 0 is inherently runtime-agnostic. The proxy speaks queue (NATS) on one side and A2A HTTP on the other. It doesn't know or care what's behind the A2A endpoint. This means the platform naturally separates into two layers:

```
┌─────────────────────────────────────────────────────────────────┐
│  Worker Pod                                                      │
│                                                                   │
│  ┌────────────────────────┐    ┌────────────────────────────┐    │
│  │  PLATFORM LAYER        │    │  USER LAYER                │    │
│  │  (Daedalus provides)│    │  (User brings their own)   │    │
│  │                        │    │                            │    │
│  │  Queue-to-A2A Proxy    │◄──►│  Any A2A-compliant server  │    │
│  │  - NATS consumer       │HTTP│  ┌──────────────────────┐  │    │
│  │  - A2A HTTP client     │    │  │ copilot-bridge       │  │    │
│  │  - SSE event forwarder │    │  │ kagent ADK           │  │    │
│  │  - Health check        │    │  │ LangGraph            │  │    │
│  │  - Trace propagation   │    │  │ CrewAI               │  │    │
│  │  - Graceful shutdown   │    │  │ Custom Go/Python/... │  │    │
│  │                        │    │  └──────────────────────┘  │    │
│  └────────────────────────┘    └────────────────────────────┘    │
│                                                                   │
│  Contract: agent runtime MUST:                                    │
│    1. Accept A2A message/send on localhost:PORT                    │
│    2. Return A2A Task responses (with SSE streaming optional)    │
│    3. Serve AgentCard at /.well-known/agent-card.json             │
│    4. Handle SIGTERM gracefully                                   │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

**What this enables:**
- Users package their agent runtime as a container image
- They declare it in an `AgentRuntime` CRD (or Helm values)
- Daedalus handles queue dispatch, scaling, discovery, observability
- No lock-in to copilot-bridge - swap runtimes without changing the platform
- A kagent ADK agent and a copilot-bridge agent can run side by side in the same factory
- Third-party agents (LangGraph, CrewAI, BeeAI, custom) plug in with zero platform changes

**copilot-bridge becomes the default runtime, not the only runtime.**

### 3.1 Runtime Contract and Interface Spec

Define the formal contract between the platform layer (proxy) and the user layer (agent runtime):

**Deliverables:**
- A2A server interface spec (minimum viable: `message/send`, AgentCard, health)
- Container interface spec (ports, env vars, volume mounts, shutdown signals)
- Conformance test suite that validates any runtime against the contract
- Reference implementation using copilot-bridge

### 3.2 K8s Operator with CRDs

Build a Go operator using kubebuilder, adopting patterns from kagent's v1alpha2 CRD design:

**CRDs (informed by kagent analysis):**

| CRD | Purpose | kagent Equivalent | Adopted Patterns |
|-----|---------|-------------------|-----------------|
| `AgentRuntime` | Defines a worker type: container image, scaling, queue binding, AgentCard | `Agent` (BYO type) | `TypedReference` for cross-resource refs, `AllowedNamespaces` for multi-tenant |
| `MCPServer` | Shared MCP server deployment (SSE transport) | `RemoteMCPServer` | Protocol enum (SSE/StreamableHTTP), `headersFrom` via `ValueRef` |
| `ModelConfig` | LLM provider configuration | `ModelConfig` | Secret hash tracking in status, provider-specific config blocks |
| `TaskPipeline` | Multi-step workflow routing | No equivalent | Fan-out/fan-in, dependency ordering |
| `AgentCard` | A2A capability registry | Inline in `Agent.spec.a2aConfig` | Skill definitions with tags, input/output modes |

**Design decisions from kagent to adopt:**

1. **`TypedReference`** for all cross-resource references:
   ```go
   type TypedReference struct {
       Kind      string `json:"kind"`
       ApiGroup  string `json:"apiGroup"`
       Name      string `json:"name"`
       Namespace string `json:"namespace,omitempty"`
   }
   ```

2. **`AllowedNamespaces`** for cross-namespace authorization (Gateway API pattern):
   ```go
   type AllowedNamespaces struct {
       From     FromNamespaces        `json:"from"` // All | Same | Selector
       Selector *metav1.LabelSelector `json:"selector,omitempty"`
   }
   ```

3. **`ValueRef` / `ValueSource`** for flexible config resolution:
   ```go
   type ValueRef struct {
       Name      string       `json:"name"`
       Value     string       `json:"value,omitempty"`
       ValueFrom *ValueSource `json:"valueFrom,omitempty"`
   }
   ```

4. **Secret hash in status** for credential rotation detection:
   ```go
   type ModelConfigStatus struct {
       SecretHash string `json:"secretHash,omitempty"`
       // Controller reconciles when hash changes
   }
   ```

5. **Agent type enum** (Declarative vs BYO) - maps to our model:
   - `Declarative` = copilot-bridge with `.agent.md` files (platform-managed)
   - `BYO` = user-provided container image speaking A2A (user-managed)

**Deliverables:**
- Kubebuilder scaffolded operator in `go/`
- CRD schemas (OpenAPI v3 validation)
- Controller reconcilers: AgentRuntime -> KEDA ScaledJob + ConfigMap
- Helm chart for operator deployment

### 3.3 Context Management (from kagent R7/R18)

Adopt kagent's context compression config as a reference for our session management:

```yaml
# In AgentRuntime CRD spec
context:
  compaction:
    compactionInterval: 5    # Compact after N invocations
    tokenThreshold: 80000    # Compact when tokens exceed this
    eventRetentionSize: 10   # Always keep N recent events
    overlapSize: 2           # Overlap for continuity
    summarizer:
      modelConfig: "summarizer-model"  # Optional separate model
```

**Deliverables:**
- Context compaction config in AgentRuntime CRD
- Integration with session resurrection strategy (R18 decision tree)
- Proxy-side context usage tracking (forward from bridge metrics)

### 3.4 Multi-Runtime Integration Test

Validate the pluggable runtime model by running two different agent runtimes in the same factory:

**Deliverables:**
- copilot-bridge worker handling coding tasks
- kagent ADK worker (or simple Python A2A server) handling a different task type
- Both dispatched by the same orchestrator via queue
- Both discovered via AgentCard registry
- Validate A2A interop: one agent's output fed as input to the other

---

## Deferred (Pending Validation)

These items are tracked in the [risk register](../research/hybrid-comms-architecture.md#open-questions-and-risk-register) and will be promoted when their prerequisites are met:

| Item | Trigger to Promote |
|------|--------------------|
| Session snapshot/restore | CLI `resumeSession()` validation experiment succeeds |
| Centralized session state store (Postgres) | Session restore validated |
| Context-aware resurrection (R18) | Centralized store built |
| K8s Operator with CRDs | Factory reaches 5+ agent types |
| Bridge state.db Postgres migration | Multi-replica orchestrator needed |

---

## Active Risks

| Risk | Status | Mitigation |
|------|--------|-----------|
| R1: Git conflicts in fan-out | Resolved in design (structured branch naming) | Validate in 0.5 |
| R2: Graceful shutdown chain | Active - must validate | Phase 0.4 |
| R4: Worker idempotency on retry | Resolved in design (new branch per retry) | Validate in 0.5 |
| R6: Observability | Active - needed from day one | Phase 1.3 |
| R13: Testing strategy | Active - needed for CI | Phase 1.4 |
