# Observability

> **Status:** Pass 1 implementation spec. This doc tells the implementer
> what to build. The strategic rationale and option analysis live in
> [research/daedalus-observability.md](https://github.com/raykao/dark-factory/blob/main/research/daedalus-observability.md)
> in `raykao/dark-factory`. Read that first if you need to understand
> *why* this shape; read this doc to understand *what* to build.

## Scope

**In scope (Pass 1):**

- Trace ID end-to-end audit and gap-fill
- Per-agent-type fleet dashboard plus a fleet overview, authored against
  agent-type labels (not hardcoded to copilot)
- Structural alert rules with no SLO thresholds
- Runbook entries for each alert
- Phase 5 acceptance-criteria coverage section

**Out of scope (Pass 2, separate epic):**

- SLO threshold alerts (cold-start P99, task latency P99, error rate)
- Rate-of-change alerts (P99 doubled week-over-week, error rate up 3x)
- Trace sampling policy beyond 100% sampling
- Log-retention extension past Loki defaults

**Out of scope (different track entirely):**

- DLQ policy decision (auto-retry vs escalate vs expire) - separate scoping doc
- Multi-tenant dashboard segregation - waits on Phase 6+ multi-tenancy
- Audit-trail logging - production-hardening track (Option E)

## Architecture summary

Today the platform has:

- OTel scaffolding in `internal/telemetry/` (provider, NATS header
  propagation, structured logging, span instrumentation at every
  queue/ACP hop)
- PR #30 production overlay deploying `kube-prometheus-stack`, Tempo,
  Loki, an OTel collector, ServiceMonitors, PodMonitors, and Grafana
  datasources

What is missing is the part that makes the stack *useful*: dashboards,
alerts, runbook entries, and a proof that the trace ID survives the
queue boundary under load.

## Implementation work items

### 1. Trace ID end-to-end audit

- Verify trace context propagates across every hop:
  Mattermost ingress -> orchestrator -> NATS publish -> NATS consume
  -> ACP `session/new` -> ACP `session/prompt` -> ACP `session/update`
  stream -> result publish -> orchestrator collector.
- Gap-fill any drop. Most likely candidates:
  - Proxy stdout pipe to the agent CLI (no propagation today).
  - Result `Artifact` envelope (verify `trace_id` field is populated,
    not regenerated downstream).
- Add an integration test under `test/integration/trace-propagation/`:
  - Publishes 100 tasks concurrently
  - Collects all spans from a test OTLP collector
  - Asserts every task has exactly one root trace with all expected
    child spans
  - Asserts no orphan spans (parent `trace_id` not present in any
    other span)

**Acceptance:** the integration test is green and the README of that
test directory documents the expected span tree.

### 2. Per-agent-type fleet dashboards

Author Grafana dashboards as JSON committed under
`deploy/helm/daedalus/dashboards/` and provisioned via the Grafana
sidecar config map.

**Per-agent dashboard** (one file per agent type, parameterized by
`agent_type` template variable):

| Panel | PromQL sketch |
|------|---------------|
| Cold-start latency histogram | `histogram_quantile(0.50/0.95/0.99, rate(daedalus_cold_start_seconds_bucket{agent_type="$agent_type"}[5m]))` |
| Queue depth | `nats_jetstream_consumer_num_pending{stream=~"agent_$agent_type.*"}` |
| Active workers | `kube_job_status_active{job_name=~"daedalus-worker-$agent_type-.*"}` |
| Task throughput (success/failure split) | `sum by (status) (rate(daedalus_tasks_total{agent_type="$agent_type"}[5m]))` |
| Task-to-artifact latency histogram | `histogram_quantile(...)` over `daedalus_task_duration_seconds` |
| Error rate (5min rolling) | success/failure derived from `daedalus_tasks_total` |
| Top 10 slowest tasks (last hour) | Tempo TraceQL link |

**Fleet overview dashboard** renders the same panels aggregated across
agent types with an `agent_type` legend breakdown.

Every metric must carry an `agent_type` label. If a metric does not
carry it today, this is a code-side fix in `internal/telemetry/` first,
not a dashboard hack.

**Acceptance:**
- Dashboards committed as JSON.
- Helm chart provisions them via Grafana sidecar.
- A fresh `make deploy-aks-test` run renders both dashboards with live
  data without manual import.
- The dashboards have an "SLO panels: baseline collection in progress"
  marker on any panel intended to gain a threshold in Pass 2.

### 3. Structural alert rules

Add Prometheus / Alertmanager rules under
`deploy/helm/daedalus/templates/prometheusrule.yaml`:

| Alert | Condition | Severity | Runbook anchor |
|-------|-----------|----------|----------------|
| `WorkerImagePullBackOff` | `kube_pod_container_status_waiting_reason{reason="ImagePullBackOff", pod=~"daedalus-worker-.*"} > 0` for 2m | page | `worker-image-pull-backoff` |
| `WorkerCrashLoopBackOff` | `rate(kube_pod_container_status_restarts_total{pod=~"daedalus-worker-.*"}[10m]) > 0` for 10m | page | `worker-crashloop` |
| `NATSConsumerLagUnbounded` | `delta(nats_jetstream_consumer_num_pending[10m]) > 0` AND `nats_jetstream_consumer_num_pending > 100` | page | `nats-consumer-lag` |
| `KEDAScalerError` | `rate(keda_scaler_errors_total[5m]) > 0` | page | `keda-scaler-error` |
| `OTelCollectorDown` | `up{job="otel-collector"} == 0` for 5m | page | `otel-collector-down` |
| `OrchestratorDown` | `kube_deployment_status_replicas_available{deployment="daedalus-orchestrator"} == 0` for 2m | page | `orchestrator-down` |
| `NATSStreamUnhealthy` | `nats_jetstream_stream_messages_lost_total > 0` OR `nats_jetstream_stream_storage_bytes / nats_jetstream_stream_max_bytes > 0.9` | warn | `nats-stream-unhealthy` |

**No SLO threshold alerts in Pass 1.** Cold-start, task latency, and
error-rate alerts wait for Pass 2.

**On-call channel routing** is a product decision and is **not** picked
in this epic. Default routing wires every alert to a `null` Alertmanager
receiver and exposes a `values.yaml` flag
(`alerting.receiver.{type,config}`) that switches it to Mattermost,
GitHub issues, or PagerDuty in three lines of values overrides. The
runbook documents how.

**Acceptance:**
- All seven rules committed.
- A fresh `make deploy-aks-test` provisions them.
- An induced failure (e.g. set the worker image to a broken tag) fires
  `WorkerImagePullBackOff` within 3 minutes.

### 4. Runbook entries

For each alert above, append a section to `docs/runbook.md` matching
the anchor in the alert table. Each section follows the same template:

```
### <runbook-anchor>

**Means:** one-sentence description of the failure mode.

**Reproduce in test cluster:**
- exact commands or config change

**Diagnose:**
- which Grafana dashboard panel
- which Loki log query (LogQL)
- which Tempo trace query (TraceQL)

**Mitigate:**
- short-term action (e.g. roll back image, scale to zero)
- ticket-the-fix link (GH issue template URL)
```

**Acceptance:** every alert has a complete runbook entry. An on-call
engineer who has never seen the alert can resolve it from the runbook
plus the dashboard.

### 5. Phase 5 validation appendix

Add a section to this doc titled "Phase 5 Acceptance Criteria Coverage"
(below) that maps each observability-gated AC to the specific dashboard
panel and PromQL query that proves it:

- **AC1: deploy in 25 minutes.** Query
  `histogram_quantile(0.99, daedalus_cold_start_seconds_bucket)` against
  the post-deploy window; assert the value is below 1500 seconds.
  Dashboard panel: cold-start latency histogram on the fleet overview.
- **AC2: e2e task asserted.** Query
  `daedalus_task_duration_seconds{task_id="<sentinel>"}` for the
  sentinel task published by the AKS e2e harness. Cross-link the
  Tempo trace by `trace_id` from the test output.
- **AC5: workflow_dispatch passes.** GHA job uploads a Grafana snapshot
  URL of the fleet overview dashboard scoped to the run window as a
  workflow artifact. The snapshot is the proof.

**Acceptance:** an engineer following this section can validate the
three observability-gated Phase 5 ACs without reading any other doc.
Once validated, mark epic #33 closeable and tag v0.3.0.

### 6. On-call channel rollout (deferred decision)

Wire all alerts to the `null` receiver by default. Document the three
lines of Helm values that switch the receiver to Mattermost / GitHub /
PagerDuty when the human picks. Do **not** pick in this epic.

## Open questions for the implementer

1. **Cardinality of `agent_type` label.** Confirm with the orchestrator
   team that `agent_type` is bounded (one of a known set, not free-form)
   before publishing it on every metric. If unbounded, propose a
   normalization layer.
2. **Grafana dashboard authoring tool.** Hand-authored JSON for Pass 1
   (small surface). If you reach for Grafonnet or
   `grafana-foundation-sdk`, document why in the PR.
3. **Test OTLP collector.** The trace-propagation integration test
   needs an in-process collector. Recommend the
   `otelcol/configtelemetrycollector` test harness or a small
   custom collector backed by an in-memory exporter. Pick one and
   note in the test README.

## References

- `internal/telemetry/{provider.go,nats.go,context.go,logging.go}` -
  existing OTel scaffolding (do not redo)
- `deploy/helm/values-example-production.yaml` - the PR #30 overlay
  that this work consumes
- `docs/plan.md` § Phase 5 Acceptance Criteria
- `docs/runbook.md` - target for new alert sections
- `docs/phase6-options.md` § Option C - the parent-doc framing
- `research/daedalus-observability.md` (in `raykao/dark-factory`) - the
  research doc this spec implements
