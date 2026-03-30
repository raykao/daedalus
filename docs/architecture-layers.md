# Agent Stack Architecture: The Four-Layer Model

## Overview

The agent ecosystem has four distinct layers. Each project in the space occupies one or more of these layers. Understanding where each sits clarifies what Agent Forge provides, what users bring, and where protocols like ACP and A2A fit.

```
┌─────────────────────────────────────────────────────────────────────┐
│ Layer 3: ORCHESTRATION PLATFORM                                      │
│   Queue dispatch, elastic scaling, discovery, observability          │
│   Agent Forge, kagent                                                │
│                                                                       │
│   Protocols: A2A (inter-agent), NATS (dispatch)                      │
├─────────────────────────────────────────────────────────────────────┤
│ Layer 2: AGENT WRAPPERS / FRAMEWORKS                                 │
│   Session management, inter-agent comms, hooks, personas, memory     │
│   copilot-bridge, GasTown, acpx/OpenClaw                            │
│                                                                       │
│   Protocols: ACP (to Layer 1), custom (internal)                     │
├─────────────────────────────────────────────────────────────────────┤
│ Layer 1: AGENT CLIs (the actual coding agents)                       │
│   LLM access, tool execution, code editing, MCP client               │
│   copilot --acp, claude --acp, codex --acp, gemini --acp            │
│                                                                       │
│   Protocols: ACP (to Layer 2/3), MCP (to tools)                     │
├─────────────────────────────────────────────────────────────────────┤
│ Layer 0: LLM APIs                                                    │
│   OpenAI API, Anthropic API, GitHub Copilot API, Google Gemini API   │
└─────────────────────────────────────────────────────────────────────┘
```

## Protocol Map

Three protocols serve different relationships. The key distinction is where each protocol operates and what transport carries it:

| Protocol | Relationship | Where Used | Transport | Role in Agent Forge |
|----------|-------------|-----------|-----------|-------------------|
| **ACP** (Agent Client Protocol) | Client drives Agent | Intra-pod (proxy to agent) | stdio or TCP (JSON-RPC/NDJSON) | Proxy sidecar drives agent CLI |
| **A2A** (Agent-to-Agent Protocol) | Peer agents collaborate | Inter-agent (orchestrator to workers) | **Data model on queue** (not HTTP) | Message envelope format on NATS |
| **MCP** (Model Context Protocol) | Agent calls Tools | Agent to tool servers | stdio or SSE/HTTP | Agent runtime accesses tools |

```
                    A2A data model (on NATS queue, not HTTP)
Orchestrator ──────────────────────────────────────────────► Worker Pod
  │                                                            │
  │  Future: A2A HTTP endpoint                            ACP (intra-pod)
  │  for external callers                               Proxy ◄──────────► Agent CLI
  │                                                                          │
  ▼                                                                     MCP (tools)
External Agent ──A2A HTTP──► Orchestrator                           Agent CLI ──► MCP Servers
  (Phase 3, optional)          (same envelope,
                                just HTTP ingress)
```

**ACP is the LSP of agents** - it standardizes the client-agent interface so any editor (or proxy, or orchestrator) can drive any agent. Just as LSP lets VS Code talk to any language server, ACP lets our proxy talk to Copilot CLI, Claude Code, Codex, Gemini, or any of 17+ ACP-compatible agents.

**A2A provides the data model, not the transport.** We use A2A's Task, Message, Part, and Artifact types as the queue message schema. We do *not* use A2A's HTTP/gRPC transport between orchestrator and workers - the queue handles that. This gives us a standardized, well-maintained schema without the overhead of HTTP hops. See [A2A Protocol Decision](#a2a-protocol-decision-data-model-yes-transport-no) for the full rationale.

### A2A Protocol Decision: Data Model Yes, Transport No

This decision was reached after evaluating whether A2A protocol is needed in the internal factory loop, and if so, which parts.

**The tension:** A2A adds JSON nesting and protocol ceremony (jsonrpc, method, params) to what could be a flat JSON message. Is it worth it?

**What A2A's data model costs us:** A few extra fields per message. The envelope is ~30% larger than a minimal custom schema. Every developer touching the queue needs to understand A2A's `Part`, `Message`, `Artifact` nesting.

**What A2A's data model buys us:**
- **No schema to maintain** - A2A community maintains the spec, SDKs validate it
- **Future interop is free** - if we expose the queue as an A2A HTTP endpoint for external agents, the messages are already A2A-shaped. No translation layer, no schema migration.
- **AgentCards as a discovery format** - even without A2A's HTTP discovery, the AgentCard JSON structure is a useful, standardized way to describe agent capabilities
- **Task lifecycle states** - submitted, working, completed, failed, canceled - already defined and agreed upon by the industry

**What we skip (A2A HTTP transport):**
- No HTTP calls between orchestrator and workers - queue provides decoupling, buffering, scale-to-zero
- No `/.well-known/agent-card.json` served by workers - they're ephemeral, can't serve HTTP. Registry handles discovery.
- No SSE streaming from workers to orchestrator - status updates flow through queue subjects instead

**The architectural payoff - external interop becomes trivial:**

```
Today (internal only):
  Orchestrator --A2A envelope--> NATS --> Proxy --ACP--> Agent

Phase 3 (add external interop):
  External Agent --A2A HTTP POST--> Orchestrator --same A2A envelope--> NATS --> (same)
                                     │
                                     └── Thin HTTP adapter (~50 lines)
                                         Accepts A2A message/send
                                         Enqueues to NATS
                                         Returns A2A Task ID
                                         Streams status via SSE from agent.status subject
```

The adapter is trivial because the internal messages are *already* A2A-shaped. If we'd used a custom schema, this adapter would need a full bidirectional translation layer.

**Decision:** Use A2A's data model as the queue envelope. Skip A2A's HTTP transport internally. Add A2A HTTP ingress on the orchestrator when external interop is needed (Phase 3). The marginal cost of A2A's JSON structure is low; the optionality it preserves is high.

## Layer-by-Layer Analysis

### Layer 1: Agent CLIs

These are the actual AI coding agents. They connect to LLM APIs, execute tools, edit code, and produce artifacts. They all now support ACP:

| Agent CLI | LLM Provider | ACP Support | Notes |
|-----------|-------------|-------------|-------|
| `copilot --acp` | GitHub Copilot | ✅ native | Our primary runtime |
| `claude --acp` | Anthropic Claude | ✅ via adapter | Via claude-agent-acp |
| `codex --acp` | OpenAI | ✅ via adapter | Via codex-acp (Zed) |
| `gemini --acp` | Google Gemini | ✅ native | Native support |
| `cursor --acp` | Cursor AI | ✅ native | cursor-agent acp |
| `qwen --acp` | Alibaba Qwen | ✅ native | qwen --acp |
| `kiro --acp` | AWS Kiro | ✅ native | kiro-cli-chat acp |
| `opencode --acp` | Various | ✅ via npx | opencode-ai acp |
| + 9 more | Various | ✅ | See acpx agent registry |

**Key ACP capabilities every CLI provides:**
- `initialize` - capability negotiation
- `session/new` - create session with working directory + MCP servers
- `session/prompt` - send prompts, receive streaming responses
- `session/cancel` - cooperative cancellation
- `session/load` - resume previous sessions (if supported)
- `session/request_permission` - tool use approval flow

### Layer 2: Agent Wrappers / Frameworks

These add capabilities on top of raw agent CLIs. They're optional - you can skip Layer 2 and go straight from Layer 3 to Layer 1.

#### copilot-bridge
**What it adds:** Chat platform adapters (Mattermost/Slack), agent personas (`.agent.md`), hooks (6 types), skills, scheduling (cron), custom tool injection, inter-agent calls (`ask_agent`), Beads memory integration.

**Best for:** Interactive chat-based agent workflows where you need personas, hooks, and human-in-the-loop via Mattermost.

**Trade-off:** Full-featured but heavy (~500MB image, 10-15s startup). Copilot-only (doesn't wrap other CLIs).

#### GasTown (Steve Yegge)
**What it adds:** Multi-agent orchestration (`gt` CLI), inter-agent messaging (`gt nudge` ephemeral, `gt mail` persistent), Beads/Dolt storage, full OTel observability (VictoriaMetrics + Grafana), Wasteland federation layer.

**Best for:** Complex multi-agent coordination where agents need to communicate with each other during execution, with full observability.

**Trade-off:** Go-based, tightly integrated with Beads/Dolt. Not ACP-native (uses its own orchestration protocol).

#### acpx / OpenClaw
**What it adds:** Headless ACP client, persistent sessions, named parallel sessions (`-s api`, `-s docs`), prompt queueing, fire-and-forget (`--no-wait`), cooperative cancel, crash reconnect with `session/load`, multi-agent support (17+ agents), TypeScript flow engine for multi-step workflows.

**Best for:** Headless automation where you want structured ACP communication without PTY scraping. Multi-agent with different CLIs in the same workflow.

**Trade-off:** Thin wrapper - doesn't add hooks, personas, or inter-agent messaging. But that thinness is a feature for our use case.

### Layer 3: Orchestration Platforms

#### Agent Forge (this project)
**What it adds:** Queue-based task dispatch (NATS JetStream), elastic scaling (KEDA ScaledJob, scale-to-zero), AgentCard discovery, structured branch naming, fan-out/fan-in pipelines, observability (OTel tracing).

**Unique:** Runtime-agnostic. The proxy speaks ACP to any agent, A2A externally.

#### kagent
**What it adds:** K8s CRDs for agents (`Agent`, `ModelConfig`, `RemoteMCPServer`), Go controller with reconciliation, Google ADK engine, A2A inter-agent communication, Web UI, CLI.

**Different from Agent Forge:** Interactive (not batch), long-running pods (not ephemeral), ADK-based (not pluggable runtime).

## Where Each Layer 2 Fits in Agent Forge

```
Worker Pod options:

Option A: Proxy → CLI directly (thinnest, simple tasks)
  ┌──────────────┐    ┌─────────────────┐
  │ Proxy sidecar│ACP │ copilot --acp   │
  │ (platform)   │◄──►│ (or any CLI)    │
  └──────────────┘    └─────────────────┘

Option B: Proxy → acpx → CLI (thin wrapper, multi-step/parallel sessions)
  ┌──────────────┐    ┌───────┐    ┌──────────────┐
  │ Proxy sidecar│ACP │ acpx  │ACP │ codex --acp  │
  │ (platform)   │◄──►│       │◄──►│ (or any CLI) │
  └──────────────┘    └───────┘    └──────────────┘

Option C: Proxy → copilot-bridge → CLI (thick, personas/hooks/skills)
  ┌──────────────┐    ┌──────────────────┐    ┌──────────────┐
  │ Proxy sidecar│ACP │ copilot-bridge   │SDK │ Copilot CLI  │
  │ (platform)   │◄──►│ (.agent.md,hooks)│◄──►│              │
  └──────────────┘    └──────────────────┘    └──────────────┘

Option D: Proxy → GasTown → CLIs (multi-agent coordination)
  ┌──────────────┐    ┌──────────┐    ┌──────────────┐
  │ Proxy sidecar│ACP │ GasTown  │    │ Agent 1 CLI  │
  │ (platform)   │◄──►│ (gt)     │◄──►│ Agent 2 CLI  │
  └──────────────┘    └──────────┘    └──────────────┘
```

**Option A** is ideal for single-shot tasks: "implement this function", "write these tests". The proxy creates an ACP session, sends a prompt, collects the result.

**Option B** adds acpx's session management for multi-turn tasks and its flow engine for multi-step workflows. The proxy delegates session lifecycle to acpx.

**Option C** is for tasks that need agent personas, pre/post tool hooks, or copilot-bridge's skill system. The bridge wraps the CLI and adds its own capabilities. The proxy talks ACP to the bridge (requires bridge ACP server mode - to be validated).

**Option D** is for complex tasks where multiple agents need to coordinate during execution (not just fan-out from the orchestrator).

## Implications for Agent Forge Design

### 1. The Proxy Speaks ACP, Not A2A, to the Agent

Our original design had the proxy speaking A2A HTTP to the agent. With ACP:
- **ACP is simpler** for the client-agent relationship (session/new, session/prompt, session/cancel)
- **ACP has native session management** (session/load for resume, session IDs)
- **ACP has permission relay** (session/request_permission)
- **ACP has MCP server config** (pass mcpServers[] at session creation)
- **The Copilot CLI already implements ACP** - no custom wrapper needed

A2A remains the right protocol for the orchestrator side (inter-agent discovery and task delegation via queue). The proxy becomes a **protocol bridge**: A2A on the queue/external side, ACP on the agent side.

### 2. acpx Overlap with Our Proxy

acpx already implements several things our proxy needs:

| Need | Proxy (custom) | acpx |
|------|:-:|:-:|
| ACP client | Build | ✅ Built |
| Session lifecycle | Build | ✅ Built |
| Prompt queueing | Build | ✅ Built |
| Fire-and-forget | Build | ✅ Built (--no-wait) |
| Multi-agent support | Build | ✅ Built (17 agents) |
| Crash reconnect | Build | ✅ Built (session/load fallback) |
| Graceful cancel | Build | ✅ Built (session/cancel + SIGTERM) |
| Named sessions | Build | ✅ Built (-s name) |
| Flow engine | Not planned | ✅ Built (multi-step) |
| NATS integration | Build | ❌ Missing |
| A2A translation | Build | ❌ Missing |
| AgentCard serving | Build | ❌ Missing |
| Trace propagation | Build | ❌ Missing |

**Options:**
1. **Use acpx as a library/dependency** - import its ACP client and session management, add our NATS/A2A/tracing layer on top
2. **Contribute NATS support to acpx** - add queue-based prompt submission upstream
3. **Build our own, study acpx** - reference its session management patterns but build from scratch in Go for our needs

### 3. Session Resume via ACP

ACP's `session/load` method solves part of our session resurrection problem (R7/R18):
- Agent declares `loadSession: true` in capabilities
- Client calls `session/load` with a previous sessionId
- Agent replays conversation history as `session/update` notifications
- Client can then continue sending prompts

This means the CLI itself handles session persistence - we don't need to archive `session-store.db` blobs if the CLI supports `session/load` natively. The proxy just needs to remember the sessionId and pass it to `session/load` on restart.

**What we still need externally:** A sessionId registry (which sessions exist, what tasks they belong to, what branch they produced) - but that's metadata, not the full session state.

### 4. Permission Relay via ACP

ACP's `session/request_permission` addresses R5 (human-in-the-loop for headless workers):

```
Agent CLI: "I want to run `rm -rf /tmp/build`"
    ↓ session/request_permission
Proxy sidecar: receives permission request
    ↓ publish to agent.permissions queue
Orchestrator: dequeues, posts to Mattermost
    ↓ human approves in chat
Orchestrator: publishes approval to agent.permissions.response
    ↓ proxy consumes
Proxy sidecar: returns { outcome: "approved" }
    ↓ back to agent
Agent CLI: executes the command
```

Or for full autopilot: proxy returns `{ outcome: "approved" }` for everything (equivalent to `--approve-all`).

## Summary: What Changed

| Before (original hybrid design) | After (layered ACP + A2A) |
|--------|-------|
| Proxy speaks A2A HTTP to agent | Proxy speaks **ACP** to agent; **A2A data model** on queue (not HTTP) |
| A2A HTTP transport between all components | A2A **data model only** on queue; HTTP transport **deferred** to Phase 3 edge |
| copilot-bridge is the agent runtime | copilot-bridge is **one option**; any ACP agent works (17+) |
| Custom A2A server wrapper needed for bridge | **Not needed** - Copilot CLI speaks ACP natively |
| Session restoration via SQLite blob archive | **ACP session/load** may handle it natively |
| Permission relay undesigned (R5) | **ACP request_permission** provides the mechanism |
| 1 supported agent (Copilot) | **17+ agents** via ACP ecosystem |
| Layer 2 always required | Layer 2 is **optional** - direct CLI for simple tasks |
| External interop requires new protocol work | External interop is **trivial** - messages already A2A-shaped, add HTTP ingress |
