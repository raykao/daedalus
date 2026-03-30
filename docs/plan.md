# Agent Forge - Implementation Plan

## Overview

Agent Forge is a Kubernetes-native platform that orchestrates ephemeral AI agent workers. It connects a chat-facing orchestrator (copilot-bridge on Mattermost) to headless worker bridges via NATS JetStream, using A2A protocol for structured agent communication and KEDA for elastic scaling.

This plan covers Phase 0 (foundation) through the first deployable system. Later phases (session resurrection, K8s operator, multi-tenancy) are tracked as deferred items in the [research risk register](../research/hybrid-comms-architecture.md#open-questions-and-risk-register).

## Guiding Principles

- Build the simplest thing that validates the architecture
- Use A2A on its native HTTP transport (no custom bindings)
- Queue provides decoupling - proxy bridges queue to A2A
- Workers are headless copilot-bridge instances (not bare CLI)
- Helm + KEDA for deployment (no custom operator yet)
- Static AgentCard config for discovery (dynamic registration deferred)

---

## Phase 0 - Foundation and Validation

Goal: validate the core primitives individually before wiring them together.

### 0.1 Queue-to-A2A Proxy prototype
Build the Go sidecar that reads from NATS JetStream, POSTs to a local A2A server (mock first, then bridge), consumes SSE status events, and publishes results back to the queue.

**Deliverables:**
- Go module in `cmd/proxy/`
- NATS consumer with ack/nack handling
- A2A HTTP client (SendMessage, StreamMessage)
- SSE event consumer that forwards status to `agent.status` subject
- Result publisher to `agent.results` subject
- Unit tests with mock A2A server
- Dockerfile for the proxy (~20MB alpine-based image)

### 0.2 copilot-bridge A2A server mode
Determine how to run copilot-bridge as an A2A-compatible HTTP server. Options:
- Thin HTTP wrapper around the bridge SDK that accepts A2A `message/send` requests
- Direct adapter that translates A2A requests into bridge session calls

**Deliverables:**
- Working bridge instance that accepts A2A `message/send` over HTTP on localhost
- Serves AgentCard at `/.well-known/agent-card.json`
- Returns A2A Task responses with status updates via SSE
- Can run headless (no Mattermost connection required)

### 0.3 Docker Compose integration test
Wire proxy + bridge + NATS in a single Docker Compose stack. Validate the full loop: publish task to queue, proxy dequeues, proxy calls bridge, bridge executes, result flows back through queue.

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
### 2.2 Dependency-ordered execution (implement, then test, then docs)
### 2.3 Result merging and PR creation
### 2.4 Dynamic AgentCard registry (NATS events)

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
│  │  (Agent Forge provides)│    │  (User brings their own)   │    │
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
- Agent Forge handles queue dispatch, scaling, discovery, observability
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
