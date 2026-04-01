# Comparison: Daedalus vs kagent

## What is kagent?

[kagent](https://kagent.dev/) is an open-source, Kubernetes-native framework for building AI agents, created by Solo.io. It provides CRDs for declaratively defining agents, their LLM models, MCP tool servers, and A2A communication - all managed by a K8s controller.

**Key facts:**
- Apache 2.0 license, CNCF community
- Go controller + Python/Go ADK engine + React UI + CLI
- 5 CRDs: `Agent`, `ModelConfig`, `ModelProviderConfig`, `RemoteMCPServer`, `ToolServer` (+ `Memory`)
- Built on Google ADK for agent execution
- A2A protocol for inter-agent communication
- MCP protocol for tool access
- OTel tracing built-in
- Currently at API version `v1alpha2`

Source: [github.com/kagent-dev/kagent](https://github.com/kagent-dev/kagent)

---

## Side-by-Side Comparison

| Dimension | **kagent** | **Daedalus** |
|-----------|-----------|----------------|
| **Purpose** | DevOps/platform engineering agent framework | Software development factory (autonomous coding) |
| **Agent engine** | Google ADK (Python/Go) | copilot-bridge + GitHub Copilot CLI |
| **LLM provider** | Any (OpenAI, Anthropic, Azure, Bedrock, Gemini, Ollama) | GitHub Copilot (subscription-based) |
| **Agent definition** | K8s CRDs (`Agent` kind with `DeclarativeAgentSpec`) | `.agent.md` files (markdown + YAML frontmatter) |
| **Interaction model** | Interactive: CLI invoke, Web UI, conversational | Autonomous: queue-dispatched, headless, produces artifacts |
| **Scaling** | Long-running Deployments (replicas) | Ephemeral KEDA ScaledJobs (scale-to-zero) |
| **Inter-agent comms** | A2A over HTTP (agent references agent as tool) | A2A over HTTP via proxy sidecar + NATS queue |
| **Queue/buffer** | None - direct A2A HTTP calls | NATS JetStream (buffering, retry, dead letter) |
| **Tool system** | MCP (`ToolServer` CRD + `RemoteMCPServer` CRD) | MCP (bridge-managed, future shared SSE servers) |
| **K8s integration** | Deep: controller, CRDs, agents ARE K8s resources | Light: K8s runs pods, agents are bridge configs |
| **Output** | Answers, actions, troubleshooting results | Git commits, PRs, documentation, code |
| **Discovery** | Agent CRDs + A2A AgentCards served by controller | Static JSON config (v1), NATS events (v2), CRD (v3) |
| **Memory** | Built-in: `MemorySpec` with embedding vectors + TTL | Beads/Dolt (dark-factory), session archive (deferred) |
| **Context mgmt** | Built-in: compaction, summarization, token threshold | Deferred (R7/R18 in risk register) |
| **Observability** | OTel tracing built-in | Planned (R6 - trace ID in A2A envelope) |
| **Maturity** | Shipping product, enterprise adopters | Research/planning phase |
| **Multi-tenancy** | Cross-namespace with AllowedNamespaces (Gateway API pattern) | Single namespace (deferred R11) |

---

## What kagent Gets Right (and We Should Learn From)

### 1. CRD Schema Design

kagent's `Agent` CRD is well-structured with clear separation of concerns:

```
Agent
  ├── type: Declarative | BYO
  ├── declarative:
  │     ├── systemMessage (or systemMessageFrom: ConfigMap/Secret)
  │     ├── modelConfig → references ModelConfig CRD
  │     ├── tools[] → references MCP servers or other Agents
  │     ├── a2aConfig.skills[] → A2A skill publication
  │     ├── memory → embedding model + TTL
  │     └── context → compaction + summarization config
  └── byo:
        └── deployment → custom container image
```

**Takeaway for Daedalus:** When we build our Phase 3 operator, adopt kagent's patterns:
- `TypedReference` for cross-resource references (kind, apiGroup, name, namespace)
- `AllowedNamespaces` for cross-namespace authorization (Gateway API pattern)
- `ValueRef` / `ValueSource` for flexible value resolution (inline, ConfigMap, Secret)
- Separate `ModelConfig` from `Agent` (one model config, many agents)

### 2. A2A Integration

kagent wires A2A directly into the CRD:
- **Publisher side:** Agent declares `a2aConfig.skills[]` - controller serves AgentCard
- **Consumer side:** Agent declares `tools[].type: Agent` with a typed reference
- **URL pattern:** `http://<controller>:8083/api/a2a/<namespace>/<agent-name>`
- **API key passthrough:** Bearer tokens from A2A requests forwarded to LLM provider

**Takeaway:** Their A2A-as-tool-reference pattern is elegant. An agent can call another agent the same way it calls an MCP tool - uniform tool interface. We should consider this when designing multi-agent pipelines.

### 3. Context Compression

kagent has built-in context management:
- `compactionInterval` - trigger after N new invocations
- `tokenThreshold` - compact when tokens exceed threshold
- `eventRetentionSize` - always keep N recent events
- `summarizer` - optional separate model for summarization

**Takeaway:** This directly addresses our R7 (context window exhaustion). When we build session resurrection, their compaction config is a good reference for checkpoint frequency and context pruning.

### 4. Cross-Namespace Security (Gateway API Pattern)

Resources declare who can reference them via `AllowedNamespaces`:
- `All` - any namespace
- `Same` - same namespace only (default)
- `Selector` - namespaces matching label selector

**Takeaway:** This is the right pattern for multi-tenancy. When our factory needs org isolation (R11), adopt this rather than inventing our own.

### 5. Secret Hash Change Detection

Controllers store a hash of secret contents in CRD status. When the secret changes, the hash mismatches and the controller reconciles - even though K8s doesn't notify on Secret content changes.

**Takeaway:** Essential for credential rotation. When our orchestrator manages GitHub App tokens or Copilot credentials, this pattern detects rotation without pod restarts.

---

## Where We Diverge (and Why)

### 1. Agent Engine: ADK vs copilot-bridge

kagent runs Google ADK agents - Python or Go programs that call LLM APIs via provider-specific SDKs. Our agents run as copilot-bridge instances wrapping the Copilot CLI. These are fundamentally different runtimes:

| | ADK Agent | copilot-bridge Agent |
|---|-----------|---------------------|
| Language | Python/Go | Node.js + Copilot CLI binary |
| LLM access | Direct API calls (OpenAI SDK, etc.) | Copilot subscription (proxied by CLI) |
| Tool execution | MCP client in ADK | Copilot CLI tool runtime (bash, git, etc.) |
| Agent definition | K8s CRD spec | `.agent.md` markdown file |
| Session state | In-memory + optional Memory CRD | CLI-managed SQLite + events.jsonl |

**Why we can't use kagent's engine:** Our agents need the Copilot CLI's code editing capabilities - file creation, git operations, PR creation, multi-turn coding conversations. ADK agents are general-purpose but don't have built-in coding workflow support.

### 2. Execution Model: Interactive vs Autonomous

kagent agents are interactive - a user invokes them via CLI or UI and gets responses. Daedalus workers are autonomous - the orchestrator dispatches a task, the worker executes for 5-15 minutes, produces artifacts (commits, PRs), and exits.

This difference drives the queue requirement: interactive agents don't need buffering (human is waiting). Autonomous factory workers do (queue absorbs bursts, retries failures, enables scale-to-zero).

### 3. Scaling: Deployments vs ScaledJobs

kagent agents run as long-lived Deployments with replicas. Our workers run as KEDA ScaledJobs that scale to zero when idle.

| | kagent | Daedalus |
|---|--------|-------------|
| Pod lifecycle | Long-running | Ephemeral (per-task) |
| Idle cost | Paying for idle pods | Zero (scale-to-zero) |
| Startup latency | None (already running) | 10-15s (pod startup) |
| State isolation | Shared across requests | Clean per task |

**Why ScaledJobs:** Software factory workloads are bursty - 50 tasks at 2am, zero at 3am. Paying for idle Deployments doesn't make sense. The 10-15s cold start is acceptable for 5-15 minute tasks.

### 4. Queue Decoupling

kagent uses direct A2A HTTP between agents. Daedalus puts NATS JetStream between the orchestrator and workers, with a proxy sidecar bridging queue to A2A HTTP.

**Why the queue matters:**
- **Scale-to-zero:** Can't POST to a pod that doesn't exist. Queue buffers until KEDA wakes a worker.
- **Retry on failure:** Worker dies mid-task, message is redelivered. No custom retry logic needed.
- **Back-pressure:** If workers are saturated, messages queue up. No request failures.
- **Dead letter:** Failed messages route to DLQ for human review.
- **Decoupling:** Orchestrator doesn't need to know worker pod IPs.

kagent doesn't need this because their agents are always running (Deployments). Our ephemeral model requires it.

---

## Could We Build on kagent?

**Short answer: Not directly, but we could contribute to it.**

kagent's controller and CRD patterns are excellent infrastructure. If kagent added:
1. A KEDA ScaledJob execution model (ephemeral pods per task)
2. Queue-based dispatch (NATS or similar)
3. A `BYO` agent type that wraps copilot-bridge
4. Git-output pipeline (commit, push, PR creation)

...it would cover our use case. But those are fundamental architectural changes, not incremental features. It would be like asking a web framework to also be a batch processing engine.

**What we should do instead:**
- Study kagent's CRD schemas when building our Phase 3 operator
- Adopt their design patterns (TypedReference, AllowedNamespaces, ValueRef, secret hashing)
- Ensure A2A compatibility so a kagent agent could call an Daedalus worker (or vice versa)
- Consider contributing a "queue transport" or "ScaledJob execution mode" upstream

---

## References

- [kagent GitHub](https://github.com/kagent-dev/kagent)
- [kagent docs](https://kagent.dev/)
- [kagent CRD types (v1alpha2)](https://github.com/kagent-dev/kagent/tree/main/go/api/v1alpha2)
- [Daedalus research](../research/hybrid-comms-architecture.md)
