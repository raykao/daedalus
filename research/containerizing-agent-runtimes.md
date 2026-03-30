# Containerizing Agent Runtimes for the Dark Software Factory

## Summary

This paper examines the practical containerization of AI coding agent runtimes for the [Dark Software Factory](dark-software-factory-architecture.md) - a system that treats GitHub Copilot agent instances as ephemeral, elastically scalable microservices orchestrated through message queues and Kubernetes. While the [architecture document](dark-software-factory-architecture.md) establishes the vision and the [KEDA research](keda-scaledjob-patterns.md) addresses scaling mechanics, a critical gap remains: what exactly goes inside the container, how does it authenticate, how does it receive work, and how does it produce output?

We identify three runtime models - copilot-bridge as a "thick" container, the Copilot CLI as a "thin" container, and a hybrid where the bridge orchestrates CLI worker containers - and analyze each across five dimensions: image design, authentication, session lifecycle, networking, and distributed systems patterns. The analysis draws on existing research into [message brokers](message-broker-selection.md), [inter-agent communication](inter-agent-task-handoff.md), and [secrets management](1password-cli-docker-secrets.md) to present a phased implementation path from the current bare-metal setup to a fully elastic, event-driven factory.

The central finding is that the hybrid model - copilot-bridge as a persistent orchestrator with Copilot CLI containers as disposable workers - offers the best balance of capability, isolation, and operational simplicity. This mirrors the well-established controller/worker pattern in distributed computing and aligns with Kubernetes-native primitives (KEDA ScaledJob, Jobs, and sidecar containers).

---

## Context & Motivation

The Dark Software Factory envisions a system where specialized AI agents (devops, frontend, security, docs) are dispatched as containerized workers in response to events - GitHub issues, PR reviews, webhook triggers, scheduled tasks. Each agent clone repo, creates a branch, executes its task via the Copilot CLI, commits results, pushes a PR, and terminates. [KEDA ScaledJobs](keda-scaledjob-patterns.md) handle the scale-to-zero and burst scaling. [Message brokers](message-broker-selection.md) provide the dispatch layer.

But the existing research treats the container itself as a black box - a Dockerfile sketch in the architecture doc, an `entrypoint.sh` outline, and an assumption that "it works." In practice, containerizing an agent runtime involves solving several non-trivial problems:

1. **Copilot CLI requires an active subscription** - how does this work in ephemeral containers that may spin up hundreds of times per day?
2. **MCP servers provide tool access** - the current setup runs them as local Docker containers or stdio processes. Inside a container, this becomes Docker-in-Docker or sidecar orchestration.
3. **Session state matters** - copilot-bridge manages hooks, scheduling, inter-agent communication, and persistent memory (Beads). A "thin" CLI container loses all of this.
4. **Graceful shutdown is critical** - an agent killed mid-commit can leave orphaned branches, incomplete PRs, and un-acked queue messages.

This paper resolves these questions and provides a buildable foundation for the factory's container layer.

---

## Knowns

- **Copilot CLI supports headless execution** via `copilot -p "prompt" --yolo --allow-all-tools`. Auth via `GITHUB_TOKEN` or `GH_TOKEN` env vars. No interactive prompt required. ([architecture doc](dark-software-factory-architecture.md))
- **copilot-bridge is a Node.js application** that wraps the Copilot CLI with chat platform adapters (Mattermost/Slack), session management, MCP server orchestration, hooks, scheduling, skills, and inter-agent communication. It runs on Node.js v22+.
- **MCP servers can run as stdio processes or Docker containers**. The current GitHub MCP server config uses `docker run -i --rm` to spawn a container per session. The GitHub-hosted MCP server at `https://api.githubcopilot.com/mcp/` uses SSE/HTTP transport but copilot-bridge only supports `stdio` transport today.
- **Container images for the Copilot CLI** need: Node.js 22+, npm, git, gh CLI, and the `@github/copilot` npm package. Base image `node:22-alpine` produces images around 200–300MB.
- **KEDA ScaledJob is the correct scaling primitive** for one-shot agent tasks - it creates a Kubernetes Job per queue message and terminates on completion. ([KEDA research](keda-scaledjob-patterns.md))
- **Azure Workload Identity provides keyless authentication** via OIDC federation - no stored secrets needed for Azure services. ([KEDA research](keda-scaledjob-patterns.md), [architecture doc](dark-software-factory-architecture.md))
- **1Password CLI's `op run` pattern** resolves secret references in-memory at process start - secrets never touch disk. ([1Password research](1password-cli-docker-secrets.md))
- **copilot-bridge's `ask_agent` is synchronous and limited** to 300s timeout. True async delegation would require a `delegate_task` pattern backed by a queue or task store. ([inter-agent research](inter-agent-task-handoff.md))

## Unknowns

- **Copilot subscription validation in ephemeral containers**: Does the CLI validate the subscription on every invocation, or is there a cached token with a TTL? This affects cold start latency and cost.
- **MCP SSE transport in copilot-bridge**: What's the effort to add SSE/HTTP client transport to copilot-bridge so it can connect to remote MCP servers (e.g., GitHub-hosted) instead of spawning local stdio processes?
- **Docker-in-Docker vs sidecar for MCP**: If an agent container needs to run MCP servers as Docker containers, does it need DinD (privileged), DooD (mount host socket), or can MCP servers run as Kubernetes sidecars?
- **copilot-bridge state portability**: The bridge stores session state, permissions, and configuration in SQLite (`state.db`) and config files. How portable is this across container restarts?
- **Cost of concurrent Copilot CLI instances**: What are the API rate limits and cost implications of running 10, 50, or 100 concurrent Copilot sessions under a single GitHub organization?
- **Graceful shutdown signal propagation**: When Kubernetes sends SIGTERM to a pod, does the Copilot CLI handle it gracefully (commit partial work) or terminate immediately?

## Gaps / Open Questions

1. **No container image exists** - the Dockerfile in the architecture doc is a sketch, not a tested artifact.
2. **No SSE transport for MCP** - copilot-bridge only supports `stdio` MCP servers, blocking use of the GitHub-hosted MCP server.
3. **No graceful shutdown handler** - no documented pattern for trapping SIGTERM and preserving work-in-progress.
4. **No multi-container orchestration pattern** - how bridge + CLI worker + MCP sidecar containers compose in a Pod spec.
5. **No benchmarks** - cold start time, image pull time, clone time, and end-to-end task latency are unmeasured.

---

## Analysis

### 1. Three Runtime Models

```
┌──────────────────────────────────────────────────────────────────────┐
│                     RUNTIME MODEL SPECTRUM                           │
│                                                                      │
│  THIN ◄─────────────────────────────────────────────────► THICK      │
│                                                                      │
│  ┌──────────────┐    ┌──────────────────┐    ┌─────────────────┐    │
│  │ Copilot CLI  │    │ Hybrid           │    │ copilot-bridge  │    │
│  │              │    │                  │    │                 │    │
│  │ Single shot  │    │ Bridge manages   │    │ Full runtime    │    │
│  │ -p "prompt"  │    │ CLI workers      │    │ in container    │    │
│  │ --yolo       │    │ via queue/API    │    │                 │    │
│  │              │    │                  │    │ Chat adapter    │    │
│  │ No state     │    │ State in bridge  │    │ MCP servers     │    │
│  │ No hooks     │    │ Hooks in bridge  │    │ Hooks, skills   │    │
│  │ No chat      │    │ Chat in bridge   │    │ Scheduling      │    │
│  └──────────────┘    └──────────────────┘    └─────────────────┘    │
│                                                                      │
│  Best for:           Best for:               Best for:               │
│  One-shot tasks      Factory orchestration   Single-server deploy    │
│  Max isolation       Elastic scaling         Dev/small team          │
└──────────────────────────────────────────────────────────────────────┘
```

**Model A: Copilot CLI Container ("Thin")**

The simplest model. A container with Node.js, the Copilot CLI, git, and gh CLI. It receives a prompt via environment variable or stdin, executes `copilot -p "$PROMPT" --yolo`, and exits. No session state, no hooks, no chat integration.

- **Pros**: Maximum isolation, minimal attack surface, fast startup, trivially scalable via KEDA ScaledJob.
- **Cons**: No inter-agent communication, no scheduling, no MCP servers (unless pre-configured), no persistent memory, no hooks. Every invocation is a cold start with no context beyond the prompt.
- **Best for**: Leaf tasks in a delegation tree - "implement this function," "write this test," "fix this lint error."

**Model B: copilot-bridge Container ("Thick")**

The entire bridge runtime - Mattermost/Slack adapter, session manager, MCP server orchestration, hooks, scheduling, skills, Beads integration - packaged in a single container. This is a direct lift-and-shift of the current bare-metal deployment.

- **Pros**: Full feature parity with the current setup. Chat integration, persistent sessions, `ask_agent`, scheduling, hooks, skills all work.
- **Cons**: Heavy image (~500MB+), long startup, stateful (SQLite, config files), hard to scale horizontally (chat adapter is 1:1 with a channel), not designed for ephemeral execution.
- **Best for**: The orchestrator role - a persistent container that manages the factory, not a worker that processes tasks.

**Model C: Hybrid ("Bridge + CLI Workers")**

copilot-bridge runs as a persistent orchestrator container. When it receives a task (from chat, queue, or schedule), it dispatches it to an ephemeral Copilot CLI container. The bridge handles context, credentials, and coordination; the CLI container handles execution.

- **Pros**: Best of both worlds - bridge retains state, hooks, inter-agent communication, and chat; CLI containers are isolated, stateless, and elastically scalable. Mirrors the controller/worker pattern in distributed systems.
- **Cons**: More complex orchestration (bridge must manage container lifecycle), requires a queue or API between bridge and workers, adds network hop latency.
- **Best for**: The full dark factory - elastic scaling with operational control.

### 2. Container Image Design

**Base image selection:**

| Base | Size | Pros | Cons |
|------|------|------|------|
| `node:22-alpine` | ~180MB | Small, fast pull, sufficient for CLI | musl libc - some npm packages may fail |
| `node:22-slim` | ~250MB | glibc, broader compatibility | Slightly larger |
| `ubuntu:24.04` + Node.js | ~400MB | Full toolchain, easiest debugging | Large, slow pull |

**Recommended: `node:22-slim`** - glibc compatibility avoids surprises with native npm modules, and the 70MB delta vs alpine is negligible compared to the Copilot CLI package itself.

**Multi-stage Dockerfile for the thin (CLI) runtime:**

```dockerfile
# Stage 1: Install dependencies
FROM node:22-slim AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    git curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN npm install -g @github/copilot

# Install GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] \
    https://cli.github.com/packages stable main" \
    | tee /etc/apt/sources.list.d/github-cli.list > /dev/null && \
    apt-get update && apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# Stage 2: Slim runtime
FROM node:22-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=builder /usr/local/bin/copilot /usr/local/bin/copilot
COPY --from=builder /usr/bin/gh /usr/bin/gh

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Non-root user for security
RUN useradd -m agent
USER agent
WORKDIR /home/agent

ENTRYPOINT ["/entrypoint.sh"]
```

**MCP server wiring inside containers:**

Three approaches, in order of preference:

1. **Remote MCP via SSE/HTTP** (requires copilot-bridge SSE transport support): Agent connects to `https://api.githubcopilot.com/mcp/` or a shared MCP server on the cluster. No local process needed. This is the cleanest model but blocked by copilot-bridge's stdio-only MCP transport.

2. **Sidecar container**: MCP server runs as a separate container in the same Pod. Agent connects via localhost. Standard Kubernetes pattern.

```yaml
containers:
- name: agent
  image: ghcr.io/raykao/dark-factory-agent:latest
- name: github-mcp
  image: ghcr.io/github/github-mcp-server:latest
  ports:
  - containerPort: 8082
  env:
  - name: GITHUB_PERSONAL_ACCESS_TOKEN
    valueFrom:
      secretKeyRef:
        name: github-token
        key: token
```

3. **Embedded stdio**: MCP server binary included in the agent image, spawned as a child process. Simplest for single-MCP setups but bloats the image.

### 3. Authentication & Credential Passthrough

The authentication challenge has two layers:

**Layer 1: GitHub/Copilot Authentication**

The Copilot CLI needs a token with the "Copilot Requests" permission scope. Options:

| Method | Lifetime | Best For |
|--------|----------|----------|
| `GITHUB_TOKEN` env var (PAT) | Long-lived | Dev/prototype |
| GitHub App installation token | 1 hour | Production - scoped, auditable |
| `gh auth token` from logged-in CLI | Session | Interactive use, not containers |
| Azure Workload Identity → GitHub OIDC | Per-request | Enterprise - fully keyless |

**Recommended for factory**: GitHub App installation tokens, fetched at container startup by a credential-helper init container or sidecar. The orchestrator (bridge) generates a scoped installation token and injects it as an environment variable for the CLI worker container.

**Layer 2: MCP Server & External Service Auth**

MCP servers (GitHub, Azure, custom) need their own credentials. The [three-layer secrets architecture](dark-software-factory-architecture.md) from the architecture doc applies:

```
Secret Store (1Password / Azure KV / Vault)
    │
    ▼
K8s Integration (ESO / CSI Driver / 1Password Injector)
    │
    ▼
Agent Pod (env vars or mounted files)
```

For the GitHub MCP server specifically, the current `docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN=$(gh auth token)` pattern doesn't work inside containers (no `gh auth` session). Instead:
- Use the same GitHub App installation token
- Or run a persistent MCP server on the cluster and connect via SSE/HTTP

### 4. Session Lifecycle in Containers

**Thin container (CLI) lifecycle:**

```
Container Start
    │
    ├── 1. Read env vars (AGENT_TASK_PROMPT, AGENT_REPO, GITHUB_TOKEN, ...)
    ├── 2. Configure git identity
    │      git config user.name "dark-factory-agent"
    │      git config user.email "agent@dark-factory"
    ├── 3. Clone repo (shallow)
    │      git clone --depth=1 https://x-access-token:${GITHUB_TOKEN}@github.com/${REPO}.git
    ├── 4. Create branch
    │      git checkout -b agent/${AGENT_TYPE}-${RUN_ID}
    ├── 5. Execute agent
    │      copilot -p "${TASK_PROMPT}" --model ${MODEL} --yolo --timeout ${TIMEOUT}
    ├── 6. Commit & push
    │      git add -A && git commit && git push
    ├── 7. Create PR (if configured)
    │      gh pr create --title "..." --body "..."
    ├── 8. Complete queue message (ack)
    └── 9. Exit 0
```

**Graceful shutdown (SIGTERM handling):**

```bash
#!/bin/bash
trap 'graceful_shutdown' SIGTERM SIGINT

graceful_shutdown() {
    echo "SIGTERM received - saving work in progress"
    cd /workspace
    git add -A
    if ! git diff --cached --quiet; then
        git commit -m "agent(${AGENT_TYPE}): WIP - interrupted by SIGTERM

Run-ID: ${RUN_ID}
Status: INTERRUPTED"
        git push origin "${BRANCH}" || true
    fi
    # Abandon (not complete) the queue message so it gets redelivered
    exit 1
}
```

The key insight: on SIGTERM, commit and push partial work (so it's not lost), but exit non-zero so the queue message is **not** acked and gets redelivered for retry.

**Thick container (bridge) lifecycle:**

copilot-bridge has its own session model with `sessionStart` and `sessionEnd` hooks. Inside a container, these still fire normally. The bridge persists state in SQLite (`state.db`), so a container restart loses session history unless the database is mounted on a persistent volume.

For the factory, the bridge container should be a **StatefulSet** (not a Deployment) with a PVC for `state.db`, or use an external database (Postgres) for state.

### 5. Networking & Communication Patterns

```
┌─────────────────────────────────────────────────────────────────┐
│                    NETWORKING TOPOLOGY                           │
│                                                                 │
│  ┌─────────────┐         ┌──────────────┐                      │
│  │ Bridge      │◄──queue──│ Message      │◄── GitHub Webhooks   │
│  │ (orchestr.) │         │ Broker       │◄── Scheduled Tasks   │
│  │             │──spawn──►│ (NATS/ASB)   │◄── Manual Triggers   │
│  └──────┬──────┘         └──────────────┘                      │
│         │                        │                              │
│         │ HTTP/gRPC              │ KEDA watches                 │
│         ▼                        ▼                              │
│  ┌─────────────┐         ┌─────────────┐                      │
│  │ MCP Server  │         │ CLI Worker  │                      │
│  │ (shared,    │◄──SSE───│ Pod         │                      │
│  │  persistent)│         │ ┌─────────┐ │                      │
│  └─────────────┘         │ │copilot  │ │───► Git Push         │
│                          │ │-p "..." │ │───► PR Create        │
│                          │ └─────────┘ │                      │
│                          │ ┌─────────┐ │                      │
│                          │ │MCP side-│ │                      │
│                          │ │car      │ │                      │
│                          │ └─────────┘ │                      │
│                          └─────────────┘                      │
└─────────────────────────────────────────────────────────────────┘
```

**Agent-to-Agent**: In the hybrid model, agents don't talk to each other directly. Communication flows through the queue:
- Bridge receives result from Worker A → enqueues task for Worker B
- This is choreography, not orchestration - each message is self-contained

**Agent-to-MCP (SSE Transport)**: The biggest architectural unlock is SSE/HTTP transport for MCP servers. Today, copilot-bridge spawns a new Docker container per session for the GitHub MCP server. With SSE transport:
- A single MCP server instance serves all agents
- No Docker-in-Docker needed inside agent containers
- Auth is centralized at the MCP server, not per-agent
- copilot-bridge would need a new transport type (`"type": "sse"` or `"type": "http"`) in its MCP config, with URL and optional Bearer token support

**Agent-to-Git**: HTTPS with token auth (`x-access-token:${GITHUB_TOKEN}`) is the standard pattern for containers. SSH adds key management complexity. Shallow clones (`--depth=1`) reduce clone time. Sparse checkout further reduces it for monorepos.

### 6. The Distributed Agent Model

The dark factory maps cleanly to established distributed computing patterns:

| Pattern | Factory Equivalent |
|---------|-------------------|
| **Controller/Worker** | Bridge dispatches tasks to CLI containers |
| **Actor Model** | Each agent is an actor with its own mailbox (queue) |
| **MapReduce** | Fan-out tasks across agents, fan-in results via Git PRs |
| **Saga Pattern** | Multi-step workflows (implement → test → review) with compensating actions |
| **Sidecar** | MCP servers, credential helpers, log forwarders alongside agent containers |

**Container-per-task vs container-per-session:**

| Model | Pros | Cons | Use When |
|-------|------|------|----------|
| Per-task | Max isolation, clean slate, simple | Cold start overhead, no context reuse | Independent tasks, strict isolation needed |
| Per-session | Context reuse, warm cache, lower latency | State leaks between tasks, harder to scale | Multi-turn conversations, iterative work |

**Recommendation**: Container-per-task for worker agents (thin CLI), container-per-session for the orchestrator (thick bridge). This matches KEDA ScaledJob (per-task) and StatefulSet (per-session) primitives.

### 7. Phased Implementation Path

**Phase 0 - Current State (Bare Metal)**
- copilot-bridge runs directly on a VM
- MCP servers run as Docker containers
- No containerization, no elastic scaling
- Single-user, single-machine

**Phase 1 - Containerize copilot-bridge**
- Package bridge + dependencies in a Docker image
- Use Docker Compose for local development
- Mount state.db and .env as volumes
- MCP servers as sidecar containers or host-network Docker
- **Outcome**: Reproducible deployment, testable in CI

**Phase 2 - Extract CLI Workers**
- Bridge dispatches tasks via queue (NATS JetStream)
- CLI worker containers process tasks independently
- Each worker: clone → branch → execute → push → exit
- Bridge monitors results via response queue or Git webhooks
- **Outcome**: Horizontal scaling of agent execution

**Phase 3 - Full Elastic Factory (KEDA)**
- Deploy to Kubernetes (AKS/EKS/GKE)
- KEDA ScaledJobs watch queue depth per agent type
- Scale-to-zero when idle, burst to N on demand
- Workload Identity for keyless auth
- Shared MCP servers via SSE transport
- Observability via OpenTelemetry
- **Outcome**: The dark factory vision realized

---

## Options & Trade-offs

| Dimension | Thin (CLI) | Thick (Bridge) | Hybrid |
|-----------|-----------|----------------|--------|
| Image size | ~300MB | ~500MB+ | 300MB workers + 500MB orchestrator |
| Startup time | 3–5s | 10–30s | 3–5s for workers |
| State management | None | SQLite + config | State in bridge only |
| Chat integration | None | Full | Via bridge |
| MCP servers | Embedded or sidecar | Full orchestration | Shared cluster service |
| Inter-agent comms | None | `ask_agent` | Queue-based |
| Scaling model | KEDA ScaledJob | StatefulSet | Both |
| Isolation | Maximum | Minimal | Workers isolated, bridge shared |
| Complexity | Low | Medium | High |
| Factory-ready | Partially | No (doesn't scale) | **Yes** |

---

## Recommendations

1. **Adopt the hybrid model** (Bridge as orchestrator + CLI as workers). It provides the strongest balance of capability, isolation, and scalability.

2. **Prioritize SSE transport for MCP servers**. This is the single highest-leverage change - it eliminates Docker-in-Docker for MCP, enables shared MCP server instances, and unblocks the use of GitHub's hosted MCP server. File a feature request or PR against copilot-bridge.

3. **Build the thin CLI container image first** (Phase 1). It's the simplest artifact, immediately useful for KEDA prototyping, and validates the auth and lifecycle patterns.

4. **Use NATS JetStream for task dispatch**. It's the [recommended self-hosted OSS broker](message-broker-selection.md), lightweight, Kubernetes-native, and has native KEDA scaler support.

5. **Implement graceful shutdown from day one**. The SIGTERM → commit → push → exit-non-zero pattern prevents work loss and enables safe queue message redelivery.

6. **Use GitHub App installation tokens** for worker containers. Short-lived (1 hour), scoped, auditable, and compatible with the Copilot subscription model.

7. **Start with container-per-task** for workers. Simpler, cleaner isolation, and aligns with KEDA ScaledJob semantics. Only move to container-per-session if benchmarks show unacceptable cold start overhead.

---

## Follow-up Research

- [ ] **Prototype: Thin CLI container image** - Build, test, and publish to GHCR. Validate cold start, auth, clone, execute, push cycle. Measure end-to-end latency.
- [ ] **SSE transport for copilot-bridge** - Analyze the session-manager MCP resolution code, design the SSE client transport, estimate LOE. Cross-ref: overnight research on `research-agent/sse-transport-copilot-bridge`.
- [ ] **Graceful shutdown testing** - Send SIGTERM to a running Copilot CLI process mid-task. Does it handle it? What state is left behind? Design the trap handler.
- [ ] **GitHub App installation token lifecycle** - Can a GitHub App token be used for both Copilot API access and Git push? What scopes are needed? How does refresh work in a 10-minute agent task?
- [ ] **Multi-container Pod spec** - Design the Kubernetes Pod spec for a CLI worker with MCP sidecar, credential-helper init container, and log forwarder sidecar.
- [ ] **NATS JetStream integration prototype** - Wire NATS → KEDA ScaledJob → CLI container. Validate message lock/ack patterns with `ack_wait` and `In-Progress` ack extension.
- [ ] **Cost modeling** - Estimate per-task cost across dimensions: compute (CPU/memory × duration), Copilot API (tokens), Git operations, queue operations.
- [ ] **Bridge containerization** - Dockerfile for copilot-bridge itself, Docker Compose for local dev, volume mount strategy for state.db.

---

## References

- [Dark Software Factory Architecture](dark-software-factory-architecture.md) - Master architecture doc, container image sketch, secrets management design
- [KEDA ScaledJob Patterns](keda-scaledjob-patterns.md) - Scaling primitive analysis, cold start latency, Azure Workload Identity
- [Message Broker Selection](message-broker-selection.md) - Queue technology evaluation, NATS JetStream recommendation
- [Inter-Agent Task Handoff](inter-agent-task-handoff.md) - Async delegation patterns, `delegate_task` proposal
- [1Password CLI & Docker Secrets](1password-cli-docker-secrets.md) - Secrets injection patterns for containers
- [GitHub Copilot CLI Documentation](https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-getting-started) - Headless execution, flags, authentication
- [GitHub Copilot SDK](https://github.com/github/copilot-sdk) - Programmatic agent sessions via JSON-RPC
- [KEDA Documentation](https://keda.sh/docs/2.19/) - ScaledJob, ScaledObject, scalers
- [MCP Specification](https://spec.modelcontextprotocol.io/) - Model Context Protocol transports (stdio, SSE, HTTP)
- [NATS JetStream Documentation](https://docs.nats.io/nats-concepts/jetstream) - Message locking via ack_wait
- [Kubernetes Jobs Documentation](https://kubernetes.io/docs/concepts/workloads/controllers/job/) - Run-to-completion semantics
