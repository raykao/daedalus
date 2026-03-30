# agent-forge

Kubernetes-native agent orchestration platform. Dispatches work to ephemeral AI agent workers via message queues, using the [A2A protocol](https://a2a-protocol.org/) for structured communication and [KEDA](https://keda.sh/) for elastic scaling.

## Architecture (TL;DR)

```
User (Mattermost) --> Orchestrator Bridge --> NATS JetStream --> Worker Pod
                                                                  |-- Queue-to-A2A Proxy (sidecar)
                                                                  |     reads queue, POSTs to bridge
                                                                  +-- copilot-bridge (A2A Server)
                                                                        reads .agent.md, executes task
                                                                        pushes to Git, returns result
```

**Key design decisions:**
- **A2A on native HTTP** - agents speak standard A2A protocol. No custom bindings.
- **Queue for decoupling** - NATS JetStream provides buffering, retry, scale-to-zero.
- **Proxy bridges the gap** - a thin Go sidecar (~300 lines) reads from queue, calls A2A HTTP on localhost, writes results back.
- **Bridge as runtime harness** - copilot-bridge interprets `.agent.md` files, provides MCP tools, manages sessions.
- **Helm + KEDA for deployment** - workers scale 0-to-N based on queue depth.

## Repository Structure

```
research/           # Architecture research papers (from dark-factory)
docs/               # Planning and tracking documents
  plan.md           # Implementation plan and phase breakdown
cmd/
  proxy/            # Queue-to-A2A proxy sidecar (Go)
deploy/
  helm/             # Helm chart for factory deployment
  docker/           # Dockerfiles for bridge worker + proxy
```

## Research

This project builds on extensive research conducted in [raykao/dark-factory](https://github.com/raykao/dark-factory):

| Paper | Topic |
|-------|-------|
| [Hybrid Comms Architecture](research/hybrid-comms-architecture.md) | A2A + queue + proxy pattern, AgentCard discovery, session state, risk register |
| [Dark Factory Architecture](research/dark-software-factory-architecture.md) | Vision, manufacturing analogy, component deep dive |
| [Containerizing Agent Runtimes](research/containerizing-agent-runtimes.md) | Three runtime models, hybrid recommendation |
| [Inter-Agent Task Handoff](research/inter-agent-task-handoff.md) | Async delegation, delegate_task design |
| [Message Broker Selection](research/message-broker-selection.md) | NATS JetStream recommendation |
| [KEDA ScaledJob Patterns](research/keda-scaledjob-patterns.md) | Scaling primitives, queue-driven autoscaling |

## Status

**Phase 0** - Planning and validation. See [docs/plan.md](docs/plan.md) for the implementation roadmap.

## Related

- [raykao/dark-factory](https://github.com/raykao/dark-factory) - Agent harness, research, and workspace conventions
- [A2A Protocol](https://a2a-protocol.org/) - Agent-to-Agent communication standard
- [copilot-bridge](https://github.com/ChrisRomp/copilot-bridge) - Chat platform bridge for GitHub Copilot
- [KEDA](https://keda.sh/) - Kubernetes Event Driven Autoscaling
