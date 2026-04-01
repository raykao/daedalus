# Daedalus Operator

A Kubernetes operator that manages pluggable AI agent runtimes via Custom Resource Definitions (CRDs). The operator provisions and orchestrates agent workers, LLM configurations, MCP tool servers, and multi-stage task pipelines -- enabling autonomous, queue-driven agent execution on Kubernetes.

## CRD Overview

| CRD | API Version | Purpose |
|-----|-------------|---------|
| **AgentRuntime** | `daedalus.dev/v1alpha1` | Defines a worker runtime: container image, NATS queue binding, KEDA scaling, and agent capabilities. Supports `Declarative` (platform-managed) and `BYO` (user-provided) runtime types. |
| **ModelConfig** | `daedalus.dev/v1alpha1` | LLM provider configuration with secret-backed API keys. Supports cross-namespace sharing via Gateway API `AllowedNamespaces` pattern. |
| **MCPServer** | `daedalus.dev/v1alpha1` | Shared MCP (Model Context Protocol) tool server that agent runtimes reference. Supports SSE, StreamableHTTP, and Stdio transports. |
| **TaskPipeline** | `daedalus.dev/v1alpha1` | Multi-stage workflow routing with fan-out/fan-in, dependency ordering, and result aggregation across agent runtimes. |
| **AgentCard** | `daedalus.dev/v1alpha1` | Standalone capability discovery resource (A2A protocol). Can be inlined in AgentRuntime or shared across runtimes. |

## Architecture

```
                              +---------------------+
                              |    TaskPipeline      |
                              |  (workflow routing)  |
                              +----------+----------+
                                         |
                          stages[] reference AgentRuntimes
                                         |
                    +--------------------+--------------------+
                    |                                         |
          +---------+---------+                   +-----------+---------+
          |   AgentRuntime    |                   |    AgentRuntime     |
          |  (Declarative)    |                   |      (BYO)         |
          +----+----+----+----+                   +----+----+----------+
               |    |    |                             |    |
               |    |    +-- env[]                     |    +-- agentCard (inline)
               |    |                                  |
               |    +-- agentCard / agentCardRef       +-- queue -> NATS JetStream
               |
               +-- modelConfigRef -----> ModelConfig (LLM creds)
               |
               +-- mcpServers[] -------> MCPServer   (tool servers)
               |
               +-- queue --------------> NATS JetStream
               |
               +-- scaling ------------> KEDA ScaledJob

  Cross-namespace access controlled by AllowedNamespaces (Gateway API pattern)
```

## Quick Start

### Prerequisites

- Go 1.24.6+
- Docker 17.03+
- kubectl 1.11.3+
- Access to a Kubernetes 1.11.3+ cluster
- [KEDA](https://keda.sh/) installed (for ScaledJob scaling)
- [NATS JetStream](https://docs.nats.io/nats-concepts/jetstream) deployed in-cluster

### 1. Install CRDs

```sh
make install
```

### 2. Deploy the operator

```sh
make docker-build docker-push IMG=<your-registry>/daedalus-operator:latest
make deploy IMG=<your-registry>/daedalus-operator:latest
```

### 3. Create a namespace and secrets

```sh
kubectl create namespace daedalus
kubectl -n daedalus create secret generic github-credentials \
  --from-literal=token=$GITHUB_TOKEN
```

### 4. Deploy sample resources

Apply the ModelConfig first (referenced by the AgentRuntime):

```sh
kubectl apply -f config/samples/forge_v1alpha1_modelconfig.yaml
kubectl apply -f config/samples/forge_v1alpha1_mcpserver.yaml
kubectl apply -f config/samples/forge_v1alpha1_agentruntime.yaml
```

Or apply all samples at once:

```sh
kubectl apply -k config/samples/
```

### 5. Verify

```sh
kubectl -n daedalus get agentruntime,modelconfig,mcpserver
```

## Sample Resources

The `config/samples/` directory contains realistic examples:

| File | Description |
|------|-------------|
| `forge_v1alpha1_agentruntime.yaml` | Declarative copilot-bridge worker with KEDA scaling, inline AgentCard, and ModelConfig reference |
| `forge_v1alpha1_agentruntime_byo.yaml` | BYO Python doc-analyzer agent with minimal configuration |
| `forge_v1alpha1_modelconfig.yaml` | GitHub Copilot LLM provider with secret-backed API key |
| `forge_v1alpha1_modelconfig_openai.yaml` | OpenAI GPT-4o provider with endpoint and parameters |
| `forge_v1alpha1_mcpserver.yaml` | GitHub MCP tool server with StreamableHTTP and auth headers |
| `forge_v1alpha1_taskpipeline.yaml` | 3-stage code review pipeline (analyze -> test -> review) |
| `forge_v1alpha1_agentcard.yaml` | Standalone AgentCard for shared capability discovery |

## Conformance Levels

Agent runtimes declare a conformance level based on the [A2A Runtime Contract](../docs/runtime-contract.md):

| Level | Requirements |
|-------|-------------|
| **Level 1** (Minimal) | Health endpoint, AgentCard, task execution |
| **Level 2** (Production) | + SIGTERM graceful shutdown, JSON structured logging, OpenTelemetry tracing |
| **Level 3** (Full) | + SSE streaming, context compaction, session resume |

## Uninstall

```sh
kubectl delete -k config/samples/   # Remove sample CRs
make uninstall                       # Remove CRDs
make undeploy                        # Remove operator
```

## Related Documentation

- [A2A Runtime Contract](../docs/runtime-contract.md) - Interface contract between platform and agent runtimes
- [Comparison with kagent](../docs/comparison-kagent.md) - Design patterns adopted from kagent (TypedReference, ValueRef, AllowedNamespaces)

## Development

```sh
make help          # Show all available targets
make generate      # Generate code (DeepCopy, RBAC, CRD manifests)
make manifests     # Generate CRD manifests
make test          # Run tests
make lint          # Run linters
```

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
