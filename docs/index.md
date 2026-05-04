---
title: Daedalus
hide:
  - navigation
---

# Daedalus

> Kubernetes-native agent orchestration platform - dispatches work to ephemeral AI agent workers via message queues, using the [A2A protocol](https://a2a-protocol.org/) for structured communication and [KEDA](https://keda.sh/) for elastic scaling.

```mermaid
flowchart LR
  User([User · Mattermost]) -->|A2A tasks| Orch[Orchestrator]
  Orch -->|A2A envelope| NATS[(NATS JetStream)]
  NATS --> Worker

  subgraph Worker[Worker Pod]
    direction TB
    subgraph Platform["PLATFORM LAYER · Daedalus"]
      Proxy["Queue-to-ACP Proxy sidecar<br/>reads queue (A2A) · writes results back"]
    end
    subgraph UserLayer["USER LAYER · bring your own"]
      direction TB
      Agent["ACP-compatible agent<br/>copilot --acp (default)<br/>claude --acp · codex --acp<br/>gemini --acp · qwen --acp<br/>+ 12 more ACP agents"]
      Wrapper["Optional Layer 2 wrappers<br/>acpx (sessions, flows)<br/>copilot-bridge (hooks, personas)"]
    end
    Proxy -->|ACP| Agent
    Agent -.->|optional| Wrapper
  end

  Proxy -->|results| NATS

  classDef platform fill:#e8f0ff,stroke:#3b6fd1,color:#000
  classDef user fill:#f3f0ff,stroke:#7a5cd1,color:#000
  class Platform,Proxy platform
  class UserLayer,Agent,Wrapper user
```

## What is Daedalus?

Daedalus is the platform layer for running AI agents as Kubernetes-native workloads. It provides queue-based task dispatch, KEDA-driven autoscaling, agent discovery, and observability - and stays runtime-agnostic. Bring your own agent: any ACP-compatible CLI (Copilot, Claude, Codex, Gemini, Qwen, and a dozen more) drops in unmodified. The platform handles the boring parts (queueing, scaling, tracing, retries) so the agent layer can focus on the work.

## Explore

<div class="grid cards" markdown>

- :material-rocket-launch: **Getting Started**

    Stand up Daedalus on AKS in under an hour.

    [:octicons-arrow-right-24: Deploy on AKS](aks-deployment.md)

- :material-sitemap: **Architecture**

    The four-layer model, ACP vs A2A, and the runtime contract.

    [:octicons-arrow-right-24: Layered Model](architecture-layers.md)

- :material-tools: **Operations**

    Runbooks, observability, alerts, SIGTERM behavior.

    [:octicons-arrow-right-24: Runbook](runbook.md)

- :fontawesome-brands-github: **Source**

    Issues, PRs, and roadmap on GitHub.

    [:octicons-arrow-right-24: raykao/daedalus](https://github.com/raykao/daedalus)

</div>
