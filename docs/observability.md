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
| `NATSConsumerLagUnbounded` | `delta(nats_consumer_num_pending{stream="<keda.natsStream>"}[10m]) > 0` AND `nats_consumer_num_pending{stream="<keda.natsStream>"} > 100` (description references `$labels.consumer_name` and `$labels.stream` to match nats-surveyor's actual label set: per `collector_statz.go`, the JSZ-derived consumer metric is `nats_consumer_num_pending` with labels including `stream` and `consumer_name`. The `stream=` filter scopes the alert to the daedalus task stream so a shared nats-surveyor watching unrelated streams does not page this rule.) | page | `nats-consumer-lag` |
| `KEDAScalerError` | `rate(keda_scaler_errors_total{namespace="<release-ns>"}[5m]) > 0` (scoped to the chart's release namespace so other teams' ScaledObjects do not page this rule) | page | `keda-scaler-error` |
| `OTelCollectorDown` | `absent(up{job="otel-collector"})` OR `up{job="otel-collector"} == 0` for 5m. The expr selector is intentionally not scoped by `namespace=`: the OTel collector typically runs in its own namespace (e.g. `monitoring`), not the chart's release namespace, so a `namespace=` matcher would never match and `absent()` would be permanently true. The alert carries a static `namespace="<release-ns>"` rule label so the AlertmanagerConfig sub-route routes it to the chart's receiver regardless of where the source `up{}` series actually lives. | page | `otel-collector-down` |
| `OrchestratorDown` | `absent(kube_deployment_status_replicas_available{namespace="<release-ns>", deployment="daedalus-orchestrator"})` OR `kube_deployment_status_replicas_available{namespace="<release-ns>", deployment="daedalus-orchestrator"} == 0` for 2m (alert also carries a static `namespace="<release-ns>"` label so the AlertmanagerConfig sub-route's namespace matcher matches even on the absent() leg) | page | `orchestrator-down` |
| `NATSStreamUnhealthy` | `nats_stream_consumer_count{stream="<keda.natsStream>"} == 0` AND `nats_stream_total_messages{stream="<keda.natsStream>"} > 0` for 10m. Both legs filter on the daedalus task stream so a shared nats-surveyor cannot fire this rule on an unrelated team's stream. The original spec used `nats_jetstream_stream_messages_lost_total > 0` OR a capacity ratio against `nats_jetstream_stream_max_bytes` / `nats_jetstream_stream_storage_bytes`; none of those metrics exist in nats-surveyor's exposition (verified against `collector_statz.go`). The replacement signal catches the same drainage-failure mode using `nats_stream_consumer_count` and `nats_stream_total_messages`, which surveyor does emit. | warn | `nats-stream-unhealthy` |

#### Spec corrections from Pass 1 implementation

A fresh-eyes cross-check of the rendered `PrometheusRule` against this
spec table caught five PromQL defects in the original draft. The table
above is the corrected form. Originals and rationale:

- **Finding 1 - `WorkerCrashLoopBackOff` threshold (was `> 0.3`).** `0.3`
  restart events per second sustained over 10 minutes is 180 restarts in
  10 minutes; Kubernetes CrashLoopBackOff caps the gap between restarts
  at ~5 minutes, so worst-case real flapping reaches ~0.005 r/s. The
  original threshold was unreachable. Corrected to `> 0` held for 10m.
- **Finding 2 - `NATSConsumerLagUnbounded` referenced a non-existent
  metric.** The original AND-leg used
  `rate(nats_jetstream_consumer_acks_total[10m]) == 0`, but
  nats-surveyor exposes only gauges - that counter does not exist under
  any name. Rewritten using gauges surveyor reliably exposes:
  `delta(...num_pending[10m]) > 0` AND `...num_pending > 100`. The 100-
  message floor avoids paging on transient single-digit blips.
- **Finding 3 - `NATSStreamUnhealthy` divided by zero on unlimited
  streams.** Unlimited-storage streams report `max_bytes == 0`; the
  unguarded `storage_bytes / max_bytes` produced `+Inf`, and `+Inf > 0.9`
  evaluates true, so every unlimited stream became a permanent false
  positive. The capacity ratio is now guarded by `max_bytes > 0`.
- **Finding 4 - `OrchestratorDown` did not fire on absent deployment.**
  Bare `kube_deployment_status_replicas_available{...} == 0` matches
  zero series when the deployment does not exist (which is today's
  state, before the orchestrator deployment lands). OR'd with
  `absent(<same selector>)` so the alert covers both "deployment exists
  with 0 available replicas" and "deployment is missing entirely".
- **Finding 5 - `NATSStreamUnhealthy` implicit join.** Vector-to-vector
  division relied on implicit auto-matching, which can silently produce
  empty results if surveyor adds extra labels (e.g. `server_id`) on one
  side. Now uses explicit `on(account, stream_name)` matching.

#### Spec corrections from Cross-check 2

A second fresh-eyes cross-check, run against nats-surveyor's actual
metric exposition (`collector_statz.go` @ commit 725f52d) and against
the AlertmanagerConfig sub-route routing semantics, surfaced six
additional defects. The table above is the corrected form.

- **CC2-1 (HIGH, was a merge blocker) - `OrchestratorDown` lacked a
  namespace label on the `absent()` leg.** The Prometheus Operator
  wraps each AlertmanagerConfig in a sub-route that injects a
  `namespace=<AMC-namespace>` matcher. The synthesised series from
  `absent()` carries only the labels in the inner selector; without
  one, the series escaped the AMC and fell through to the global
  Alertmanager default receiver. Both legs now carry an explicit
  `namespace="<release-ns>"` matcher and the alert carries a static
  `namespace` label.
- **CC2-2 - `NATSConsumerLagUnbounded` used a non-existent metric and
  the wrong consumer label.** `nats_jetstream_consumer_num_pending`
  does not exist; surveyor's JSZ-derived metric is
  `nats_consumer_num_pending`. The consumer label is `consumer_name`
  (with `_name`), not `consumer`. Both expr and description now
  match surveyor's actual exposition.
- **CC2-3 - `NATSStreamUnhealthy` referenced three fictional metrics.**
  None of `nats_jetstream_stream_messages_lost_total`,
  `nats_jetstream_stream_max_bytes`, or
  `nats_jetstream_stream_storage_bytes` exist in surveyor's
  exposition. The rule was silently never firing on either leg.
  Replaced with a surveyor-real signal that catches the same
  drainage-failure mode: `nats_stream_consumer_count == 0 and
  nats_stream_total_messages > 0` for 10m. The capacity-ratio leg is
  dropped because no per-stream storage cap is exposed.
- **CC2-4 - `OTelCollectorDown` had no `absent()` guard.** Same class
  of bug as CC2-1 / pre-existing finding 4. If the collector and its
  ServiceMonitor are missing entirely, `up{}` returns no series and
  the bare `==0` does not fire. Now wraps in `absent()` and carries a
  static `namespace` label so the AMC sub-route routes correctly
  regardless of the source `up{}` series' label set.
- **CC2-5 - `KEDAScalerError` was cluster-scoped.** KEDA emits
  `keda_scaler_errors_total` cluster-wide; without a namespace
  filter, errors from any other team's ScaledObject paged this rule
  with `daedalus_component=keda` attribution. Now scoped to
  `namespace="<release-ns>"`.
- **CC2-6 - PagerDuty example secret name diverged across surfaces.**
  `values.yaml`, `docs/runbook.md`, and the chart test used three
  different example names. Aligned to `pagerduty-routing-key` (the
  runbook's literal `kubectl create secret` invocation).
- **CC2 in-loop follow-up - same CC2-1 class of bug on both NATS
  alerts.** `NATSConsumerLagUnbounded` and `NATSStreamUnhealthy` fire
  on `nats_consumer_*` / `nats_stream_*` metrics from nats-surveyor,
  which typically runs in its own namespace; the source series'
  `namespace` label is surveyor's pod namespace, not the release
  namespace, so the AMC sub-route matcher does not match and the
  alerts escape to the global default. Both alerts now carry a static
  `namespace="<release-ns>"` rule label. Defense-in-depth: the same
  static label is applied to `WorkerImagePullBackOff` and
  `WorkerCrashLoopBackOff` (whose kube-state-metrics source label
  already resolves to the release namespace) so the chart is hardened
  against any future ServiceMonitor relabeling drift. A new test in
  `alerts_test.sh` asserts every rendered alert carries a static
  `namespace` label so future additions cannot regress this class.

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
