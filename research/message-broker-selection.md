# Message Broker Selection: Self-Hosted OSS Options for the Dark Software Factory

## Summary

For the Dark Software Factory's agent task dispatch, the critical requirement is **message locking** (or equivalent ack-timeout semantics) - ensuring a message is invisible to other consumers while an agent processes it, with automatic redelivery if the agent fails. Six brokers were evaluated: Azure Service Bus (managed), RabbitMQ, NATS JetStream, Apache Pulsar, KubeMQ, and Valkey Streams. **NATS JetStream is the recommended self-hosted OSS option** - it's the lightest operationally (100-300 MB/node), provides `ack_wait` as a lock equivalent, has native KEDA ScaledJob support, and is purpose-built for Kubernetes. RabbitMQ is a strong alternative if advanced routing is needed. Apache Pulsar is the most feature-complete but heaviest to operate.

## Context & Motivation

The [KEDA ScaledJob research](keda-scaledjob-patterns.md) established Azure Service Bus as the recommended queue for an AKS-based factory. However, the [architecture document](dark-software-factory-architecture.md) lists multiple queue technologies as candidates and the user specifically asks: **is there a self-hosted, open-source alternative that supports message locking?**

This matters because:
- **Cost**: Azure Service Bus charges per operation ($0.05/million for Standard). At 10,000+ agent tasks/day, costs add up.
- **Portability**: A self-hosted broker decouples the factory from Azure, enabling multi-cloud or on-premises deployment.
- **Control**: Self-hosted means full configuration control - no managed service limits on lock duration, message size, or queue count.
- **License purity**: Some teams prefer fully OSS stacks without managed service dependencies.

The key technical requirement is **message locking** or equivalent: when an agent picks up a task, no other agent should process the same message for a configurable duration (5–15 minutes for agent tasks). If the agent crashes, the message must become available again.

## Knowns

- **Azure Service Bus** provides native Peek-Lock with configurable lock duration (max 5 min) and SDK-based auto-renewal. It's the current baseline from KEDA research. ([Microsoft Learn](https://learn.microsoft.com/en-us/azure/service-bus-messaging/message-transfers-locks-settlement))
- **RabbitMQ** uses ack-based redelivery, not explicit message locks. The `consumer_timeout` (default 30 min, configurable) closes channels of unresponsive consumers, triggering redelivery. Prefetch=1 per pod ensures one message per consumer. Has built-in DLQ via dead-letter exchanges. ([RabbitMQ Docs](https://www.rabbitmq.com/docs/confirms))
- **NATS JetStream** provides `ack_wait` per consumer - functionally equivalent to a message lock/visibility timeout. If a consumer doesn't ack within `ack_wait`, the message is redelivered. Supports `In-Progress` ack to extend the "lease" (equivalent to lock renewal). `max_deliver` + dead-letter subjects for DLQ. ([NATS Docs](https://docs.nats.io/nats-concepts/jetstream))
- **Apache Pulsar** has `ackTimeout` per consumer (configurable, default 60s), negative ack for immediate retry, and built-in Dead Letter Policy with `maxRedeliverCount`. Full-featured and battle-tested at scale (Yahoo, Tencent). ([Pulsar Docs](https://pulsar.apache.org/docs/2.9.x/concepts-messaging/))
- **KubeMQ Community** is Apache 2.0 licensed and has native visibility timers (true SQS-style message locking). The visibility window can be extended during processing. However, advanced features require a paid Enterprise license. ([KubeMQ Docs](https://docs.kubemq.io/learn/message-patterns/queue))
- **Valkey** (Redis OSS fork, BSD licensed, Linux Foundation) supports Streams with consumer groups. Pending Entry List (PEL) + `XCLAIM` provides a message-locking equivalent - unacked messages can be claimed by other consumers after idle time. However, DLQ must be built manually. ([Valkey Docs](https://valkey.io/topics/streams-intro/))
- **All six brokers have KEDA ScaledJob support**: Azure Service Bus (built-in), RabbitMQ (built-in), NATS JetStream (built-in), Apache Pulsar (built-in), Redis/Valkey Streams (built-in via Redis Lists/Streams scaler). KubeMQ also has a built-in KEDA scaler. ([KEDA Scalers](https://keda.sh/docs/2.19/scalers/))

## Unknowns

- **NATS JetStream KEDA scaler maturity for redelivered messages**: A known issue ([kedacore/keda#3787](https://github.com/kedacore/keda/issues/3787)) shows the NATS scaler may not count redelivered messages in lag calculation. This could cause KEDA to not scale up for "stuck" messages. Need to verify current status.
- **Pulsar operator stability on AKS**: Apache Pulsar on Kubernetes requires BookKeeper + ZooKeeper (or the newer PIP-45 metadata store). Operational complexity is significantly higher than NATS. Actual AKS operational experience data is sparse.
- **KubeMQ Community Edition feature limits**: The Apache 2.0 Community edition has "limited" production features. Exactly which limits (queue depth? throughput? HA?) aren't clearly documented.
- **Valkey Streams XCLAIM automation**: Implementing automatic message reassignment after idle timeout requires custom code or a sidecar. No native "auto-rebalance on timeout" exists - it's a DIY pattern.
- **License compliance for enterprise deployment**: Some organizations may have restrictions on which OSS licenses are acceptable. Need to verify that the chosen broker's license aligns with organizational policy.

## Gaps

1. **No benchmarks specific to our workload**: Agent tasks are low-throughput (100–10,000/day) but long-duration (30s–15min per message). No broker has been benchmarked for this profile - most benchmarks focus on high-throughput, low-latency scenarios.
2. **No HA testing**: None of the self-hosted options have been tested for high availability in our target environment (AKS with KEDA).
3. **No migration path from Azure Service Bus**: If we start with ASB and later move to self-hosted, the message schema and agent entrypoint would need abstraction.

## Analysis

### Lock Mechanism Comparison

The "message lock" requirement is the key differentiator. Here's how each broker implements it:

| Broker | Lock Mechanism | Lock Duration | Lock Renewal | Auto-Redeliver on Timeout | DLQ Support |
|---|---|---|---|---|---|
| **Azure Service Bus** | Peek-Lock (native) | Max 5 min | SDK auto-renew (unlimited) | ✅ Yes | ✅ Built-in |
| **RabbitMQ** | Ack-based + `consumer_timeout` | Default 30 min (configurable) | N/A - timeout is per-channel, not per-message | ✅ On channel close | ✅ Dead-letter exchange |
| **NATS JetStream** | `ack_wait` per consumer | Configurable (e.g., 600s) | ✅ `In-Progress` ack extends lease | ✅ Yes | ✅ Via `max_deliver` + dead-letter subject |
| **Apache Pulsar** | `ackTimeout` per consumer | Configurable (e.g., 600s) | Not needed - timeout is absolute | ✅ Yes | ✅ Built-in Dead Letter Policy |
| **KubeMQ** | Visibility timer (native) | Configurable | ✅ Extend visibility during processing | ✅ Yes | ✅ Built-in |
| **Valkey Streams** | PEL + `XCLAIM` (manual) | Consumer idle time threshold | N/A - claim is manual | ❌ Requires custom code | ❌ Must build manually |

**Key insight**: For the Dark Factory's agent workload (long-running, 5–15 min tasks), the lock mechanism MUST support either:
- Lock renewal / lease extension (ASB, NATS, KubeMQ), or
- Long absolute timeouts (Pulsar, NATS, RabbitMQ)

Valkey is the weakest here - its locking is entirely manual and doesn't integrate with KEDA's scaling decisions.

### Operational Footprint Comparison

| Broker | Minimum K8s Pods | Memory per Node | CPU per Node | Persistence | Complexity |
|---|---|---|---|---|---|
| **Azure Service Bus** | 0 (managed) | 0 | 0 | Managed | None - SaaS |
| **NATS JetStream** | 3 (cluster) | 100–300 MiB | Low | Built-in (file-based) | **Low** - single binary, Go |
| **RabbitMQ** | 3 (cluster) | 200 MiB–2+ GiB | Moderate–High | Mnesia + PVCs | **Medium** - Erlang, tuning-sensitive |
| **Apache Pulsar** | 6+ (broker + BookKeeper + ZK) | 1–4+ GiB per component | Moderate | BookKeeper (dedicated) | **High** - multi-component |
| **KubeMQ** | 3 (cluster) | ~100–200 MiB | Low | Built-in | **Low** - single binary, Go |
| **Valkey Streams** | 3 (cluster/sentinel) | 100–500 MiB | Low | RDB/AOF | **Low** - familiar Redis ops |

**Key insight**: NATS JetStream and KubeMQ are the lightest operationally. Apache Pulsar is the heaviest by a wide margin. For a project in early prototyping phase, operational simplicity matters more than enterprise features.

### Throughput & Latency (for Context)

The factory's workload is low-throughput: 100–10,000 messages/day. All brokers easily handle this. For reference:

| Broker | Msgs/sec (durable) | Latency (p99) | Relevance to Factory |
|---|---|---|---|
| **NATS JetStream** | 200k–400k | 1–5 ms | Massive overkill - plenty of headroom |
| **RabbitMQ** | 50k–100k | 5–20 ms | Overkill - still plenty |
| **Apache Pulsar** | 100k–300k | 5–10 ms | Overkill |
| **Valkey Streams** | 100k+ | <1 ms | Overkill |
| **Azure Service Bus** | 1k–5k (Standard tier) | 10–50 ms | Sufficient, but lowest ceiling |

Throughput is not the deciding factor. Lock mechanics, operational simplicity, and KEDA integration quality are.

### KEDA Scaler Quality

| Broker | KEDA Scaler | Scale-to-Zero | Scales on Backlog | Scales on Redelivered | Notes |
|---|---|---|---|---|---|
| **Azure Service Bus** | Built-in, mature | ✅ | ✅ | ✅ | Best-tested scaler for ScaledJob |
| **RabbitMQ** | Built-in, mature | ✅ | ✅ (`queueLength`) | ✅ (acks pending = visible) | Well-documented, widely used |
| **NATS JetStream** | Built-in | ✅ | ✅ (`lagThreshold`) | ⚠️ Known issue [#3787](https://github.com/kedacore/keda/issues/3787) | May miss redelivered messages in lag |
| **Apache Pulsar** | Built-in | ✅ | ✅ (`msgBacklog`) | ✅ | Includes unacked in backlog |
| **KubeMQ** | Built-in | ✅ | ✅ | ✅ | Less community testing |
| **Valkey/Redis Streams** | Built-in (Redis scaler) | ✅ | ✅ (`pendingEntriesCount`) | ❌ | Manual XCLAIM not visible to KEDA |

### License Comparison

| Broker | License | Truly OSS? | Self-Host Risk |
|---|---|---|---|
| **NATS** | Apache 2.0 | ✅ Yes | None |
| **RabbitMQ** | MPL 2.0 | ✅ Yes | Broadcom acquired Pivotal; no license change yet |
| **Apache Pulsar** | Apache 2.0 | ✅ Yes | None |
| **KubeMQ Community** | Apache 2.0 | ⚠️ Partial - advanced features are paid | Feature ceiling unknown |
| **Valkey** | BSD 3-Clause | ✅ Yes | Linux Foundation governance |
| **Azure Service Bus** | Proprietary (managed) | ❌ No | Vendor lock-in |

## Options & Trade-offs

| Option | Lock Support | KEDA Quality | Ops Complexity | Self-Hosted Cost | OSS License | Recommendation |
|---|---|---|---|---|---|---|
| **Azure Service Bus** | ✅ Native Peek-Lock | ✅ Best | None (managed) | $50–500/mo | ❌ Proprietary | **Yes** - if Azure-only is acceptable |
| **NATS JetStream** | ✅ `ack_wait` + `In-Progress` | ⚠️ Good (redelivery caveat) | **Low** | ~$50/mo infra | ✅ Apache 2.0 | **Yes** - best OSS balance |
| **RabbitMQ** | ⚠️ Ack-based (coarse) | ✅ Mature | **Medium** | ~$100–200/mo infra | ✅ MPL 2.0 | **Maybe** - if routing needed |
| **Apache Pulsar** | ✅ `ackTimeout` + DLQ | ✅ Good | **High** | ~$200–500/mo infra | ✅ Apache 2.0 | **No** - overengineered for our scale |
| **KubeMQ** | ✅ Visibility timer (native) | ✅ Good | **Low** | Free (Community) | ⚠️ Partial OSS | **Maybe** - watch feature limits |
| **Valkey Streams** | ⚠️ Manual (XCLAIM) | ⚠️ Limited | **Low** | ~$30–50/mo infra | ✅ BSD | **No** - too manual for critical path |

## Recommendations

1. **Start with Azure Service Bus if staying Azure-native**: It has the best KEDA integration, native Peek-Lock, built-in DLQ, and zero operational overhead. This is the fastest path to a working prototype. Use it as the baseline, then evaluate self-hosted later if cost or portability becomes a concern.

2. **NATS JetStream is the top self-hosted OSS pick**: It provides `ack_wait` (lock equivalent), `In-Progress` ack (lock renewal), `max_deliver` + dead-letter subjects (DLQ), and a minimal operational footprint (3 pods, ~300 MiB total). Apache 2.0 license. Purpose-built for cloud-native workloads. The KEDA redelivery counting issue ([#3787](https://github.com/kedacore/keda/issues/3787)) should be verified against the latest KEDA version before committing.

3. **Abstract the message layer from day one**: Design the agent entrypoint to receive tasks via an interface (e.g., `TaskSource`), not a broker-specific SDK. This allows swapping Azure Service Bus → NATS (or anything else) without rewriting agent logic. The message schema should be broker-agnostic JSON.

4. **Don't use Apache Pulsar**: It's the most feature-complete but requires BookKeeper + ZooKeeper/metadata store - minimum 6 pods, significant operational investment. Overkill for 100–10,000 tasks/day. Revisit only if the factory scales to enterprise-level multi-tenant workloads.

5. **Don't use Valkey Streams for the primary task queue**: The manual `XCLAIM`-based locking doesn't integrate with KEDA's scaling model and requires custom sidecar logic. Valkey/Redis is better suited as a caching layer or secondary data store within the factory.

6. **Watch KubeMQ**: Its native visibility timer is the closest equivalent to Azure Service Bus's Peek-Lock in the OSS world. But the Community Edition's production feature limits are poorly documented. If the team is willing to evaluate and potentially pay for Enterprise, it's worth a deeper look.

## Follow-up Research

- [ ] **Verify NATS JetStream KEDA scaler redelivery behavior**: Test with KEDA 2.19+ to confirm whether [issue #3787](https://github.com/kedacore/keda/issues/3787) has been resolved. If not, evaluate whether a custom scaler or workaround is viable. *Outcome*: Go/no-go for NATS as primary broker.

- [ ] **Prototype: NATS JetStream + KEDA ScaledJob on AKS**: Deploy a 3-node NATS cluster with JetStream enabled, create a subject, deploy a ScaledJob that processes messages with 60s `ack_wait`. Validate scale-to-zero, lock expiry/redelivery, and DLQ routing. *Outcome*: Working prototype with latency numbers.

- [ ] **Design broker abstraction layer for agent entrypoint**: Define a `TaskSource` interface that abstracts: receive message, extend lock, complete, fail/DLQ. Implement for Azure Service Bus and NATS JetStream. *Outcome*: Swappable message layer.

- [ ] **KubeMQ Community Edition feature audit**: Deploy KubeMQ Community on K8s and systematically test: max queue depth, visibility timer duration, HA failover, throughput limits. Document which features are gated behind paid tiers. *Outcome*: Clear go/no-go for KubeMQ.

- [ ] **Cost comparison: Azure Service Bus vs self-hosted NATS at scale**: Model costs for 100, 1000, and 10000 tasks/day across both options. Include compute (AKS node pool for NATS), storage, and operational labor. *Outcome*: Break-even analysis.

## Open Questions

- **Hybrid broker strategy**: Could we use Azure Service Bus for production and NATS JetStream for local development/testing? The abstraction layer (Recommendation #3) would enable this, but adds testing matrix complexity.

- **Message ordering requirements**: If agent tasks ever need strict ordering (e.g., "implement feature" before "write tests"), NATS JetStream doesn't have session-like ordering (Azure Service Bus does). Is this a future need?

- **Multi-region considerations**: If the factory eventually runs in multiple Azure regions or multi-cloud, how does each broker handle cross-region replication? NATS has native leaf node federation; RabbitMQ has federation/shovel plugins; ASB has Geo-DR.

- **Encryption at rest**: For sensitive task payloads (containing repo tokens, issue content), does each broker support encryption at rest? NATS JetStream stores to disk but encryption is file-system level (not built-in). Important for compliance-sensitive deployments.

- **NATS vs RabbitMQ community trajectory**: Broadcom's acquisition of VMware (which owned Pivotal/RabbitMQ) has created uncertainty in the RabbitMQ community. Is the NATS community growing faster? Does this affect long-term support?

## References

- [NATS JetStream Documentation](https://docs.nats.io/nats-concepts/jetstream) - Persistence, ack_wait, consumer configuration
- [NATS JetStream KEDA Scaler](https://keda.sh/docs/2.19/scalers/nats-jetstream/) - Configuration parameters, lag threshold
- [KEDA Issue #3787: NATS Jetstream redelivery counting](https://github.com/kedacore/keda/issues/3787) - Known issue with redelivered message lag
- [RabbitMQ Consumer Acknowledgements and Publisher Confirms](https://www.rabbitmq.com/docs/confirms) - Ack semantics, prefetch, redelivery
- [RabbitMQ Memory Threshold](https://www.rabbitmq.com/docs/memory) - Resource tuning for Kubernetes
- [RabbitMQ TTL and Expiration](https://www.rabbitmq.com/docs/ttl) - Message timeout configuration
- [Apache Pulsar Messaging Concepts](https://pulsar.apache.org/docs/2.9.x/concepts-messaging/) - ackTimeout, nack, redelivery
- [Apache Pulsar KEDA Scaler](https://keda.sh/docs/2.19/scalers/pulsar/) - Configuration for ScaledJob
- [StreamNative: How Pulsar Helps Agents Stay Resilient](https://streamnative.io/blog/reliability-that-thinks-ahead-how-pulsar-helps-agents-stay-resilient) - Pulsar for agent workloads
- [KubeMQ Documentation - Queue Patterns](https://docs.kubemq.io/learn/message-patterns/queue) - Visibility timers, locking
- [KubeMQ Community Edition (GitHub)](https://github.com/kubemq-io/kubemq-community) - Apache 2.0 licensed edition
- [Valkey Streams Documentation](https://valkey.io/topics/streams-intro/) - Consumer groups, PEL, XCLAIM
- [Valkey 9.0 Roadmap (Percona)](https://www.percona.com/blog/valkey-9-0-features-enterprise-ready-open-source-and-coming-september-15-2025/) - Enterprise features planned
- [KEDA All Scalers](https://keda.sh/docs/2.19/scalers/) - Complete list of supported event sources
- [NATS vs RabbitMQ Comparison (Aiven)](https://aiven.io/tools/streaming-comparison/nats-vs-rabbitmq) - Feature and performance comparison
- [NATS/RabbitMQ/Kafka 2025 Benchmarks (Onidel)](https://onidel.com/blog/nats-jetstream-rabbitmq-kafka-2025-benchmarks) - Throughput and latency numbers
- [Pulsar vs RabbitMQ vs NATS (StreamNative)](https://streamnative.io/pulsar/pulsar-vs-rabbitmq-vs-nats) - Comparative analysis
- [NATS Compare Page](https://docs.nats.io/nats-concepts/overview/compare-nats) - Official feature comparison matrix
- [KEDA ScaledJob Patterns Research](keda-scaledjob-patterns.md) - Parent research on KEDA integration
- [Dark Software Factory Architecture](dark-software-factory-architecture.md) - Core architecture document
