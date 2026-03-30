# KEDA ScaledJob Patterns: Event-Driven Agent Autoscaling for the Dark Software Factory

## Summary

KEDA (Kubernetes Event-Driven Autoscaling) is a CNCF-graduated project that provides two primary scaling primitives: **ScaledObject** (for long-running Deployments) and **ScaledJob** (for one-shot Kubernetes Jobs). For the Dark Software Factory - where each agent invocation is a discrete, stateless task (clone → branch → execute → push → exit) - **ScaledJob is the correct primitive**. It creates a fresh Kubernetes Job per event (or batch of events) from a message queue, runs to completion, and terminates. Scale-to-zero is inherent: when the queue is empty, zero Jobs exist. Cold start latency is typically 5–30 seconds for lightweight containers, tunable to <5s with image caching and node pre-provisioning.

## Context & Motivation

The [Dark Software Factory architecture](dark-software-factory-architecture.md) defines agents as ephemeral, containerized workers triggered by messages on a queue. The architecture document identifies KEDA as the autoscaling mechanism (follow-up item #3: "KEDA + ScaledJob prototype") but does not deeply explore:

- Which KEDA primitive (ScaledObject vs ScaledJob) best fits the agent execution model
- How ScaledJob configuration maps to agent lifecycle requirements
- What cold start latency to expect and how to mitigate it
- How to wire authentication (Azure Service Bus, Workload Identity) into ScaledJob triggers
- Production patterns: max concurrency, failure handling, job cleanup, observability
- Multi-queue routing: one ScaledJob per agent type, or a single dispatcher

This research resolves those questions and produces actionable configuration patterns for the factory's KEDA layer.

## Knowns

- **KEDA is CNCF-graduated** (graduated 2024), production-ready, and supports 60+ event sources including Azure Service Bus, RabbitMQ, NATS, Kafka, Redis Streams, AWS SQS, and GCP Pub/Sub. Current version: 2.19.x (as of early 2026). ([keda.sh](https://keda.sh))
- **ScaledJob creates Kubernetes Jobs per event**: Unlike ScaledObject (which adjusts replica count of a Deployment), ScaledJob directly creates `batch/v1` Job resources in response to detected events. Each Job runs to completion and terminates. ([keda.sh/docs/2.19/concepts/scaling-jobs](https://keda.sh/docs/2.19/concepts/scaling-jobs/))
- **ScaledJob supports `maxReplicaCount`**: Caps the maximum concurrent Jobs to prevent resource starvation. Critical for controlling blast radius in a multi-agent factory. ([keda.sh](https://keda.sh/docs/2.19/concepts/scaling-jobs/))
- **ScaledJob supports two scaling strategies**: `default` (creates jobs proportional to pending messages) and `accurate` (creates jobs only for "unlocked"/unprocessed messages, avoiding duplicates). The `accurate` strategy is recommended for queue-based workloads where message locking matters. ([keda.sh](https://keda.sh/docs/2.19/concepts/scaling-jobs/))
- **Azure Service Bus scaler** supports queues, topics, and subscriptions. Parameters: `queueName`, `topicName`, `subscriptionName`, `messageCount` (threshold), `activationMessageCount` (minimum to activate from zero), `namespace`, `useRegex`. ([keda.sh/docs/2.19/scalers/azure-service-bus](https://keda.sh/docs/2.19/scalers/azure-service-bus/))
- **Authentication options for Azure Service Bus**: Connection string (via Secret), pod identity (AAD Pod Identity / Workload Identity), or MSI (Managed Service Identity). Workload Identity with OIDC federation is the recommended keyless approach. ([keda.sh](https://keda.sh/docs/2.19/scalers/azure-service-bus/))
- **Cold start latency for scale-to-zero is typically 5–30 seconds** for lightweight stateless containers, assuming cached images and available nodes. Variables: node availability (cluster autoscaler may add 30s–2min), image pull time, application init time. ([dev.to](https://dev.to/viradiaharsh/optimizing-kubernetes-scaling-with-keda-balancing-performance-and-cost-efficiency-1n3j), [Google Cloud Blog](https://cloud.google.com/blog/products/containers-kubernetes/scale-to-zero-on-gke-with-keda))
- **Polling interval is configurable**: `pollingInterval` (default 30s) controls how often KEDA checks the event source. For latency-sensitive workloads, can be set as low as 1s. ([keda.sh](https://keda.sh/docs/2.19/concepts/scaling-jobs/))
- **Job history limits are configurable**: `successfulJobsHistoryLimit` and `failedJobsHistoryLimit` control how many completed/failed Jobs are retained for debugging. ([keda.sh](https://keda.sh/docs/2.19/concepts/scaling-jobs/))
- **KEDA integrates with Kubernetes HPA** for ScaledObject but bypasses it for ScaledJob - Jobs are created directly by KEDA's operator, providing more direct control. ([keda.sh](https://keda.sh/docs/2.19/concepts/scaling-jobs/))
- **KEDA supports pausing/unpausing** via annotations: `autoscaling.keda.sh/paused: "true"` on the ScaledJob resource. Useful for maintenance windows. ([docs.kedify.io/best-practices](https://docs.kedify.io/best-practices))
- **Sample implementations exist**: `kedacore/sample-dotnet-worker-servicebus-queue` is the official reference for Azure Service Bus + ScaledJob patterns. ([GitHub](https://github.com/kedacore/sample-dotnet-worker-servicebus-queue))

## Unknowns

- **Message-to-Job cardinality for agent tasks**: Should each queue message create exactly one Job (1:1), or should a single Job batch-process multiple messages? Agent tasks vary in duration (30s for a linting fix vs 10min for a complex feature). 1:1 is simpler but may create excessive Job churn for bursty queues.
- **Job completion signal reliability**: When the agent container exits (code 0), the Job is marked complete. But what if the agent pushes a PR successfully but the container crashes before the exit code propagates? Need to understand Kubernetes Job completion guarantees and at-least-once semantics.
- **Multi-queue vs single-queue routing**: Should each agent type (devops, frontend, backend, security) have its own ScaledJob + queue, or should a single queue with message metadata be used with a dispatcher? The architecture doc suggests per-type queues - need to validate this is the right pattern at scale.
- **Cost of idle KEDA operator**: When all ScaledJobs are scaled to zero, the KEDA operator itself still runs (3 pods: operator, metrics-apiserver, admission-webhooks). What are the resource costs for the operator in a mostly-idle factory?
- **Interaction with cluster autoscaler**: If KEDA creates 20 Jobs simultaneously and the cluster needs more nodes, what's the end-to-end latency? Is there a way to pre-provision a warm node pool?
- **ScaledJob rollout strategy for updates**: When updating the agent container image, how do in-flight Jobs behave? ScaledJob supports a `rollout.strategy` field - need to understand `gradual` vs `default` behavior.
- **Graceful shutdown and message redelivery**: If a Job is terminated (node drain, preemption, OOM), does the message get redelivered by Azure Service Bus? How does `activeDeadlineSeconds` interact with message lock duration?

## Gaps

1. **No prototype exists**: The architecture doc calls for a KEDA + ScaledJob prototype but none has been built. Need a minimal working example: queue + ScaledJob + container that processes a message.
2. **No failure handling pattern**: Dead-letter queue (DLQ) routing, retry policies, and alerting on failed Jobs are not yet designed.
3. **No observability wiring**: ScaledJob creates Jobs, but there's no defined pattern for emitting metrics (job duration, success rate, queue drain rate) to the factory's observability stack.
4. **No cost model**: No estimate of per-agent-invocation Kubernetes resource cost (CPU/memory request × duration) or KEDA operator overhead.
5. **No multi-tenant isolation model**: If multiple repositories or teams share the factory, how are ScaledJobs namespaced and resource-quota'd?

## Analysis

### ScaledJob vs ScaledObject: Why ScaledJob Is the Right Primitive

The Dark Software Factory agent model is fundamentally a **one-shot task processor**: receive message → clone repo → execute agent → push results → exit. This maps directly to KEDA's ScaledJob, which creates a Kubernetes `batch/v1` Job per event (or batch).

| Characteristic | ScaledObject (Deployment) | ScaledJob (Job) | Agent Fit |
|---|---|---|---|
| Lifecycle | Long-running, scaled up/down | One-shot, run-to-completion | **ScaledJob** - agents are ephemeral |
| Scaling mechanism | Adjusts replica count via HPA | Creates new Job per event | **ScaledJob** - each task is independent |
| Scale-to-zero | Supported but requires cooldown tuning | Inherent - zero messages = zero Jobs | **ScaledJob** - simpler config |
| Failure handling | Pod restarts, CrashLoopBackOff | `backoffLimit`, then Job fails | **ScaledJob** - cleaner failure semantics |
| Resource cleanup | Manual - Pods persist | Automatic via history limits | **ScaledJob** - self-cleaning |
| Rollout strategy | Rolling update, blue/green | `default` (kill in-flight) or `gradual` (drain) | **ScaledJob** - `gradual` preserves running agents |

**Verdict**: ScaledJob is the correct primitive. ScaledObject would require additional logic to prevent Pods from being killed mid-task during scale-down, whereas ScaledJob's run-to-completion guarantee aligns with agent semantics.

### Multi-Queue Architecture: One ScaledJob per Agent Type

The recommended pattern is **one dedicated queue + one ScaledJob per agent type**:

```
Azure Service Bus Namespace
├── queue: agent-devops        → ScaledJob: agent-devops-job
├── queue: agent-frontend      → ScaledJob: agent-frontend-job
├── queue: agent-backend       → ScaledJob: agent-backend-job
├── queue: agent-security      → ScaledJob: agent-security-job
├── queue: agent-docs          → ScaledJob: agent-docs-job
└── queue: agent-dlq           → ScaledJob: agent-dlq-processor (or manual)
```

**Benefits**:
- **Independent scaling**: DevOps queue can spike to 20 Jobs while frontend stays at 0, with independent `maxReplicaCount` per type.
- **Independent resource limits**: Security agents might need more memory than docs agents - different container specs per ScaledJob.
- **Fault isolation**: A bug in the frontend agent container won't affect devops queue processing.
- **Independent rollout**: Update the security agent image without touching other ScaledJobs. Using `gradual` strategy, in-flight security Jobs finish with the old image.
- **Per-queue observability**: Queue depth, drain rate, and job success rate are directly attributable to agent type.

**Trade-off**: More Kubernetes resources (one ScaledJob + TriggerAuthentication per type). At ~5-10 agent types, this is manageable. At 50+, consider a dispatcher pattern.

### Message Lock Duration vs Job Lifetime

This is the most critical timing interaction to get right. Azure Service Bus uses **Peek-Lock** mode: a consumer "locks" a message for processing, and must complete/abandon it within the lock duration.

**Timing constraints**:

```
Lock Duration (default 30s, max 5min)  ←→  Agent Execution Time (30s – 10min+)
                                        ←→  activeDeadlineSeconds (K8s hard kill)
```

**The problem**: Agent tasks often take 2–10 minutes (clone, LLM inference, commit, push). The default 30s lock is far too short. If the lock expires, the message becomes available to another consumer → **duplicate agent execution**.

**Solution - Lock Auto-Renewal**:

1. The agent container's entrypoint must use the Azure Service Bus SDK's **auto-renew lock** feature (available in .NET, Python, Node.js SDKs). This continuously renews the lock in the background while the agent works.
2. Set `maxAutoLockRenewalDuration` to exceed the worst-case agent execution time (e.g., 15 minutes).
3. Set `activeDeadlineSeconds` in the ScaledJob slightly above `maxAutoLockRenewalDuration` as a safety net.
4. If the Job exceeds `activeDeadlineSeconds`, Kubernetes kills it, the lock eventually expires, and the message is redelivered → DLQ after max delivery count.

**Recommended timing configuration**:

| Parameter | Value | Rationale |
|---|---|---|
| Service Bus lock duration | 5 minutes | Maximum allowed; provides buffer |
| SDK `maxAutoLockRenewalDuration` | 15 minutes | Covers worst-case agent tasks |
| K8s `activeDeadlineSeconds` | 900 (15 min) | Hard kill matches renewal timeout |
| ScaledJob `backoffLimit` | 2 | Retry twice, then fail to DLQ |
| Service Bus `maxDeliveryCount` | 3 | After 3 failed deliveries → DLQ |

### Failure Handling Pattern: Retry + Dead-Letter Queue

KEDA does not manage business-level failure routing - it creates Jobs and relies on Kubernetes Job semantics for retries. The failure cascade:

```
Message received
    │
    ├── Agent succeeds → Complete message → Job exits 0 → ✅
    │
    └── Agent fails → Job exits non-zero
            │
            ├── backoffLimit not reached → K8s retries Pod
            │       └── (Message lock still held via auto-renew)
            │
            └── backoffLimit reached → Job marked Failed
                    └── Message lock expires → ASB redelivers
                            │
                            ├── maxDeliveryCount not reached → New Job processes
                            │
                            └── maxDeliveryCount reached → Message → DLQ
                                    └── Alert fires → Human investigates
```

**DLQ processing options**:
- **Manual**: Operator inspects DLQ via Service Bus Explorer or CLI, replays manually.
- **Automated**: A dedicated `agent-dlq-processor` ScaledJob monitors the DLQ, logs failures, creates GitHub Issues for investigation.

### Rollout Strategy for Agent Image Updates

KEDA ScaledJob supports two rollout strategies:

| Strategy | In-Flight Behavior | Use Case |
|---|---|---|
| `default` | Running Jobs are terminated immediately | Short-lived jobs where interruption is acceptable |
| `gradual` | Running Jobs finish naturally; only new Jobs use new image | **Recommended for agents** - tasks are stateful (mid-commit) |

Configuration:
```yaml
spec:
  rollout:
    strategy: gradual
    propagationPolicy: foreground
```

With `gradual`, updating the agent container image (e.g., new Copilot CLI version, new agent definition) is safe: in-flight agents complete their work, and new Jobs pick up the updated image.

### Cold Start Latency Analysis

When all agents are idle (queue empty, zero Jobs), the first message triggers:

| Phase | Latency | Notes |
|---|---|---|
| KEDA polling detects message | 0–`pollingInterval` sec | Set to 10s for responsive activation |
| Job creation by KEDA operator | <1s | Direct API call, no HPA involved |
| Pod scheduling | 1–5s | Depends on node availability |
| Image pull | 0s (cached) to 30s+ (first pull) | Use `imagePullPolicy: IfNotPresent` + pre-cached images |
| Container start + init | 1–3s | Node.js + npm deps already in image |
| Git clone | 2–10s | Depends on repo size; use shallow clone |
| **Total cold start** | **~5–30s typical** | **Acceptable for async agent tasks** |

**Mitigation strategies**:
- Pre-cache agent images on nodes via DaemonSet or `ImagePullJob` (OpenKruise).
- Use a `minReplicaCount: 1` ScaledJob for the most critical agent type (warm standby).
- Keep container images small (<500MB) - `node:22-alpine` base is ~180MB.

### KEDA Operator Overhead

The KEDA operator itself runs 3 components:

| Component | Typical Idle Resources | Purpose |
|---|---|---|
| `keda-operator` | ~30–50 MiB, <50m CPU | Watches ScaledJob/ScaledObject CRDs, polls event sources |
| `keda-operator-metrics-apiserver` | ~30–50 MiB, <50m CPU | Exposes custom metrics to K8s API |
| `keda-admission-webhooks` | ~20–30 MiB, <20m CPU | Validates CRD changes |

**Total idle overhead**: ~100–130 MiB memory, <120m CPU. Negligible compared to agent workloads. At 10 ScaledJobs with polling intervals of 10s, expect minimal increase.

### Authentication: Azure Workload Identity (Keyless)

For the Dark Software Factory running on AKS, **Azure Workload Identity** is the recommended authentication method for KEDA triggers. This aligns with the architecture doc's secrets management research (which recommends OIDC federation over stored credentials).

**Setup flow**:
1. Enable OIDC issuer + Workload Identity on AKS cluster
2. Create a User-Assigned Managed Identity (e.g., `keda-scaler-mi`)
3. Grant it `Azure Service Bus Data Receiver` role on the Service Bus namespace
4. Create federated credential linking K8s ServiceAccount → Managed Identity
5. Annotate the ServiceAccount with `azure.workload.identity/client-id`
6. Create `TriggerAuthentication` with `podIdentity.provider: azure-workload`

```yaml
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata:
  name: servicebus-workload-identity
spec:
  podIdentity:
    provider: azure-workload
    identityId: "<MANAGED_IDENTITY_CLIENT_ID>"
```

**Benefits**: No connection strings or secrets stored in K8s. Tokens are short-lived and auto-rotated. Fully auditable via Azure AD sign-in logs.

### Illustrative ScaledJob Configuration for Agent Factory

The following shows a complete ScaledJob for the `devops` agent type:

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledJob
metadata:
  name: agent-devops-job
  namespace: dark-factory
spec:
  jobTargetRef:
    parallelism: 1
    completions: 1
    activeDeadlineSeconds: 900   # 15 min hard kill
    backoffLimit: 2              # Retry twice, then fail
    template:
      metadata:
        labels:
          app: dark-factory-agent
          agent-type: devops
      spec:
        serviceAccountName: dark-factory-agent-sa
        containers:
        - name: agent
          image: ghcr.io/raykao/dark-factory-agent:latest
          env:
          - name: AGENT_TYPE
            value: "devops"
          - name: AGENT_DEFINITION
            value: ".github/agents/devops.agent.md"
          resources:
            requests:
              cpu: "500m"
              memory: "512Mi"
            limits:
              cpu: "2000m"
              memory: "2Gi"
        restartPolicy: Never
  pollingInterval: 10              # Check queue every 10s
  maxReplicaCount: 10              # Max 10 concurrent devops agents
  successfulJobsHistoryLimit: 10
  failedJobsHistoryLimit: 10
  scalingStrategy:
    strategy: accurate             # Only count unprocessed messages
  rollout:
    strategy: gradual              # Don't kill in-flight agents on image update
  triggers:
  - type: azure-servicebus
    metadata:
      queueName: agent-devops
      namespace: dark-factory-bus
      messageCount: "1"            # 1 Job per message
    authenticationRef:
      name: servicebus-workload-identity
```

## Options & Trade-offs

### Queue Technology Selection (for KEDA ScaledJob triggers)

| Option | Pros | Cons | Complexity | Recommendation |
|---|---|---|---|---|
| **Azure Service Bus** | Managed, DLQ built-in, sessions/ordering, Workload Identity, rich KEDA scaler | Azure-only, cost at scale ($0.05/M operations for Standard) | Low (managed) | **Yes** - best fit for AKS-based factory |
| **NATS JetStream** | OSS, lightweight, self-hosted, very fast, multi-cloud | No managed option, DLQ must be built, less mature KEDA scaler | Medium | Maybe - good for multi-cloud future |
| **RabbitMQ** | Mature, proven, rich routing, good KEDA support | Self-hosted (or CloudAMQP), operational overhead | Medium | No - more complexity than ASB for equivalent features |
| **Redis Streams** | Simple, fast, already common in stacks | No DLQ, limited message guarantees, not designed for this | Low | No - wrong tool for reliable task dispatch |

### ScaledJob Scaling Strategy

| Option | Behavior | Best For | Recommendation |
|---|---|---|---|
| `default` | Creates Jobs proportional to total pending messages | Simple workloads | No - may over-count locked messages |
| `accurate` | Creates Jobs only for unlocked/unprocessed messages | Queue-based with message locking | **Yes** - prevents duplicate Jobs |
| `custom` | User-defined formula | Complex multi-metric scaling | No - unnecessary for single-queue triggers |

### Job-per-Message vs Batch Processing

| Option | Pros | Cons | Recommendation |
|---|---|---|---|
| **1 Job per message** | Simple, isolated, clear success/failure | Job churn on bursty queues, overhead of Job creation | **Yes** - agent tasks are independent and variable-duration |
| **Batch (N messages per Job)** | Less Job churn, amortized overhead | Complex shutdown logic, partial failure handling | No - adds complexity without clear benefit for agent workloads |

## Recommendations

1. **Use ScaledJob (not ScaledObject) for all agent workloads**: Each agent task is stateless, one-shot, and run-to-completion. ScaledJob's semantics match perfectly.

2. **Deploy one ScaledJob per agent type with dedicated Azure Service Bus queues**: Provides independent scaling, fault isolation, and per-type observability. Start with 3-5 agent types and scale from there.

3. **Use `accurate` scaling strategy**: Prevents KEDA from creating duplicate Jobs for messages that are already locked/in-processing.

4. **Use `gradual` rollout strategy**: Ensures in-flight agent tasks complete when the container image is updated.

5. **Implement lock auto-renewal in the agent container entrypoint**: The biggest operational risk is lock expiry during long-running agent tasks. Use the Azure Service Bus SDK's `maxAutoLockRenewalDuration` set to 15 minutes.

6. **Use Azure Workload Identity for KEDA trigger authentication**: No stored secrets, short-lived tokens, fully auditable. Aligns with the factory's secrets management architecture.

7. **Set `pollingInterval: 10` for responsive activation**: The default 30s adds unnecessary latency for the first message after scale-to-zero.

8. **Configure DLQ + alerting from day one**: Azure Service Bus has built-in DLQ. Set `maxDeliveryCount: 3` on queues. Create a monitoring alert on DLQ depth > 0.

9. **Start with `maxReplicaCount: 10` per agent type**: Conservative cap to prevent resource starvation. Tune upward as you understand actual concurrency needs.

10. **Pre-cache agent container images on nodes**: Use a DaemonSet or OpenKruise ImagePullJob to keep images warm, reducing cold start from ~30s to ~5s.

## Follow-up Research

- [ ] **Prototype: KEDA ScaledJob + Azure Service Bus on AKS** - Build a minimal working example: deploy KEDA on AKS, create an Azure Service Bus queue, deploy a ScaledJob with a mock agent container that receives a message, sleeps 30s, and logs success. Validate scale-to-zero, activation latency, and DLQ routing. *Outcome*: Proven pattern with measured latency numbers.

- [ ] **Container image design for agent entrypoint** - Design the entrypoint script that: receives message from queue → parses task JSON → clones repo → executes Copilot CLI → commits/pushes → completes message. Focus on lock renewal, error handling, and exit code semantics. *Outcome*: Documented entrypoint architecture. Cross-ref: [dark-software-factory-architecture.md §Container Image](dark-software-factory-architecture.md).

- [ ] **Observability wiring for ScaledJob metrics** - Research how to emit per-Job metrics (duration, success/failure, agent type) to Prometheus/Grafana. Consider KEDA's built-in metrics vs custom exporter vs OpenTelemetry from the agent container. *Outcome*: Metrics architecture doc.

- [ ] **Cost modeling: AKS + KEDA + Service Bus** - Estimate monthly cost for various workload profiles: 100 tasks/day, 1000 tasks/day, 10000 tasks/day. Include compute (AKS node pool), Service Bus operations, and Copilot API costs. *Outcome*: Cost projection spreadsheet.

- [ ] **Cluster autoscaler integration testing** - Test the interaction between KEDA creating burst Jobs and the AKS cluster autoscaler provisioning new nodes. Measure end-to-end latency for the "cold cluster" case (0 spare nodes). *Outcome*: Latency numbers, node pool sizing recommendations.

- [ ] **Multi-tenant namespace isolation** - If multiple teams share the factory, research Kubernetes ResourceQuota, LimitRange, and NetworkPolicy patterns for per-team or per-repo isolation of ScaledJobs. *Outcome*: Namespace design doc.

- [ ] **KEDA Kedify commercial add-on evaluation** - Kedify offers enterprise features (enhanced metrics, dashboard, multi-cluster). Evaluate whether it's worth the cost vs pure OSS KEDA for the factory's needs. *Outcome*: Buy-vs-build decision.

## Open Questions

- **Agent task timeout variability**: Some agent tasks (linting fix) take 30 seconds, others (complex feature) take 10+ minutes. Should there be separate "fast" and "slow" queues with different `activeDeadlineSeconds`, or a single queue with generous timeouts? This affects resource efficiency and DLQ routing.

- **Message ordering guarantees**: Azure Service Bus supports sessions for ordered delivery. Do any agent workflows require strict ordering (e.g., "implement feature" before "write tests")? If so, sessions would need to be configured, which complicates ScaledJob scaling.

- **Agent container image registry**: Should agent images be published to GHCR (GitHub Container Registry) for GitHub-native workflow, or ACR (Azure Container Registry) for AKS-native pull performance? Or both with a mirror?

- **Hybrid ScaledObject for orchestrator**: The triage/dispatcher service that classifies incoming work and routes to agent queues may be better served by a ScaledObject (long-running Deployment) rather than ScaledJob. This design decision is deferred to the orchestrator research.

- **KEDA HTTP add-on for webhook ingestion**: KEDA has an HTTP add-on that can scale based on incoming HTTP requests. Could this replace the need for a separate webhook ingestion service? Needs investigation.

## References

- [KEDA Official Documentation - Scaling Jobs](https://keda.sh/docs/2.19/concepts/scaling-jobs/) - ScaledJob CRD spec, scaling strategies, configuration reference
- [KEDA ScaledJob Specification Reference](https://keda.sh/docs/2.19/reference/scaledjob-spec/) - Full YAML spec with all fields documented
- [KEDA Azure Service Bus Scaler](https://keda.sh/docs/2.19/scalers/azure-service-bus/) - Queue/topic trigger parameters, authentication options
- [KEDA Azure AD Workload Identity](https://keda.sh/docs/2.19/authentication-providers/azure-ad-workload-identity/) - Keyless authentication via OIDC federation
- [KEDA Best Practices (Kedify)](https://docs.kedify.io/best-practices) - Production tuning: polling intervals, fallbacks, annotations
- [Securely Scale Applications Using KEDA and Workload Identity on AKS (Microsoft Learn)](https://learn.microsoft.com/en-us/azure/aks/keda-workload-identity) - Official AKS + KEDA + Workload Identity guide
- [kedacore/sample-dotnet-worker-servicebus-queue](https://github.com/kedacore/sample-dotnet-worker-servicebus-queue) - Official sample: .NET worker processing Service Bus messages via ScaledJob
- [KEDA ScaledJobs: Python CRDs for Event-Driven Autoscaling (2025)](https://johal.in/keda-scaledjobs-python-crds-for-event-driven-autoscaling-in-kubernetes-2025/) - 2025 guide with Python examples
- [KEDA Kubernetes: The Ultimate Guide for 2025 (Plural)](https://www.plural.sh/blog/keda-kubernetes-autoscaling/) - Comprehensive overview with cost optimization patterns
- [Optimizing Kubernetes Scaling with KEDA (DEV Community)](https://dev.to/viradiaharsh/optimizing-kubernetes-scaling-with-keda-balancing-performance-and-cost-efficiency-1n3j) - Cold start analysis and cost balancing
- [Scale to Zero on GKE with KEDA (Google Cloud Blog)](https://cloud.google.com/blog/products/containers-kubernetes/scale-to-zero-on-gke-with-keda) - GKE-specific cold start benchmarks
- [KEDA vs Knative vs HPA Comparison](https://thinhdanggroup.github.io/keda-knative-kubenetes/) - Comparative analysis of autoscaling approaches
- [KEDA Autoscaling on Kubernetes in Production (Jorijn)](https://jorijn.com/en/blog/keda-autoscaling-on-kubernetes-in-production/) - Production architecture, trade-offs, lessons learned
- [Intelligent Wake-Up Proxy Whitepaper (proxy-KEDA)](https://proxy-keda.com/proxy-KEDA2%20white%20paper.pdf) - Sub-5s cold start patterns using proxy layers
- [Azure Service Bus Message Locks and Settlement (Microsoft Learn)](https://learn.microsoft.com/en-us/azure/service-bus-messaging/message-transfers-locks-settlement) - Lock duration, auto-renewal, peek-lock semantics
- [Azure Service Bus Lock Auto-Renewal Guide](https://www.tutorialpedia.org/blog/autotmatically-renewing-locks-correctly-on-azure-service-bus/) - SDK patterns for lock renewal in long-running processors
- [Kubernetes & Long Running Batch Jobs with KEDA (Larry Claman)](https://larry.claman.net/post/2021-11-19-21-keda2/) - activeDeadlineSeconds interaction with KEDA Jobs
- [Scale Pods in AKS with KEDA (Chaminda)](https://chamindac.blogspot.com/2024/01/scale-pod-in-aks-with-kubernetes-event.html) - AKS-specific walkthrough with troubleshooting
- [KEDA Workshop on AKS (Microsoft)](https://microsoft.github.io/k8s-on-azure-workshop/lab-3/7_keda/index.html) - End-to-end workshop for Azure
- [Azure AKS KEDA Integration Guide (OneUptime, Jan 2026)](https://oneuptime.com/blog/post/2026-01-30-azure-aks-keda-integration/view) - Recent AKS + KEDA + Workload Identity integration walkthrough
- [ScaledJob Deep Dive (DeepWiki)](https://deepwiki.com/kedacore/keda/2.2-scaledjob) - Code-level analysis of ScaledJob internals
- [Reducing Cloud Costs with KEDA Autoscaling (Hokstad Consulting)](https://www.hokstadconsulting.com/blog/reducing-cloud-costs-keda-autoscaling) - Cost analysis and ROI for KEDA adoption
- [Dark Software Factory Architecture](dark-software-factory-architecture.md) - Parent research document (follow-up item #3)
