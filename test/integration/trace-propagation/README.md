# Trace Propagation Integration Test

Phase 6 sub-task 6.1: end-to-end W3C TraceContext propagation across every
hop of the daedalus task pipeline, plus a 100-task concurrent integration
test that proves the propagation under load.

This directory contains:

- `trace_propagation_test.go` - the integration test (build tag
  `//go:build integration`).
- `helpers_test.go` - in-process helpers (NATS embed, fake ACP server,
  trace assertion utilities).
- This README - audit findings, expected span tree, and run instructions.

---

## 1. Audit findings

The repo already has the OTel scaffolding (`internal/telemetry/provider.go`,
`internal/telemetry/nats.go`, `internal/telemetry/context.go`). What was
missing pre-Phase-6.1 was that **none of the hops actually used the
scaffolding**: trace context was not injected at publish time, not
extracted at consume time, and the proxy handler did not start spans.
Trace IDs would have shown up in logs (via `telemetry.NewLogger`) but
spans would have been orphans on the back end of NATS.

### Hops audited

| # | Hop | Pre-fix state | Post-fix state | File touched |
|---|-----|---------------|----------------|--------------|
| 1 | Mattermost ingress -> orchestrator | n/a (ingress not yet wired in this codebase; see deviation note below) | n/a | - |
| 2 | orchestrator -> NATS publish (`agent.tasks.*`) | `queue.Publisher.PublishJSON` published bytes-only via `js.Publish`; no NATS headers, no span. **GAP.** | `PublishJSON` now publishes via `nats.Msg` with `traceparent` injected via `telemetry.InjectNATSHeaders`, and emits a `nats.publish` span. | `internal/queue/nats.go` |
| 3 | NATS consume -> worker | `queue.Consumer.processMessage` passed `msg.Data()` to the handler with the original `ctx`; headers were never extracted. **GAP.** | `processMessage` extracts headers via `telemetry.ExtractNATSHeaders` and starts a `nats.consume` span (linked to the publish span via the W3C parent). | `internal/queue/nats.go` |
| 4 | worker -> ACP `session/new` | `proxy.Handler.Handle` did not start a span; ACP calls were untraced. **GAP.** | `Handle` starts a `proxy.handle` span; `acp.session.new`, `acp.session.prompt`, and the result publish are children. | `internal/proxy/handler.go` |
| 5 | ACP `session/prompt` | Same as #4. **GAP.** | Wrapped in `acp.session.prompt` span. | `internal/proxy/handler.go` |
| 6 | ACP `session/update` stream (server -> client notifications) | The ACP wire (NDJSON over TCP) does not carry a header channel and the ACP spec does not define one. Streaming notifications correlate to the in-flight `session/prompt` call by `sessionId`, so they remain logically inside the `acp.session.prompt` span on the client side. **No code change needed**: the client-side span already covers the streaming window. | Unchanged. Documented. | - |
| 7 | result publish (`agent.results.*`) | Same root cause as hop 2. The result `a2a.Task` envelope did not carry a `trace_id` field either. **GAP.** | Result publish goes through the now-instrumented `Publisher.PublishJSON` (NATS headers carry the full W3C `traceparent`). The result `Task.Metadata["trace_id"]` is also stamped by the proxy as a belt-and-suspenders escape hatch for downstream consumers that cannot read NATS headers (e.g. a future Mattermost client speaking JSON-only). | `internal/proxy/handler.go` |
| 8 | orchestrator collector | `ResultCollector.handleResult` did not extract headers and did not emit a span. Trace context died on the way back. **GAP.** | `handleResult` and `handleStatus` extract NATS headers via `telemetry.ExtractNATSHeaders`, also pull `trace_id` from `Task.Metadata` as a fallback, and emit a `collector.receive.result` (resp. `collector.receive.status`) span as the child of the publish span. | `internal/orchestrator/collector.go` |

### Note on the spec's "stdout pipe to the agent CLI" gap candidate

The spec listed "proxy stdout pipe to the agent CLI" as a likely gap. The
current proxy does **not** spawn the agent CLI as a subprocess - it dials
ACP over TCP (`internal/acp/client.go`, `Connect()` -> `net.DialContext`).
The agent process is started out-of-band (Kubernetes pod, docker compose
service, etc.). There is therefore no stdout pipe to instrument today.

If a future runtime *does* spawn the agent CLI in-process (Phase 7
multi-runtime work), the propagation pattern recommended in the spec is
to set `TRACEPARENT` (and optionally `TRACESTATE`) as environment
variables on the child process. The child can read these on startup and
seed its own TracerProvider with the parent context. This avoids
modifying any agent CLI's protocol input. We have **not** added this
hook proactively because there is no subprocess to attach it to in this
codebase; doing so would be dead code.

### Note on `trace_id` in the `Artifact` envelope

The spec asked: "verify the envelope carries `trace_id` end-to-end".
The result envelope is `a2a.Task` (it contains an `Artifact`); the
`Artifact` itself does not carry a `trace_id` separately because the
trace ID for the whole task is identical to the trace ID for its single
artifact - a per-artifact `trace_id` would be redundant. We stamp
`trace_id` once into `Task.Metadata` and rely on the W3C header on the
NATS message for the full trace context. Downstream consumers (the
orchestrator collector) read both.

---

## 2. Expected span tree per task

For each of the 100 tasks the test publishes, the following spans are
emitted and form a single trace:

```
test.dispatch                              (root, kind=internal)
└─ nats.publish agent.tasks.<taskId>       (kind=producer)
   └─ nats.consume agent.tasks.<taskId>    (kind=consumer)
      └─ proxy.handle <taskId>             (kind=internal)
         ├─ acp.session.new                (kind=client)
         ├─ acp.session.prompt             (kind=client)
         └─ nats.publish agent.results.<taskId>  (kind=producer)
            └─ collector.receive.result    (kind=consumer)
```

Span counts per task: **8 spans**, of which exactly **1** is the root
(`test.dispatch`). All 8 share the same `trace_id`. Non-root spans each
have a `parent_span_id` that exists somewhere in the tree.

Across 100 tasks: 800 total spans, 100 distinct trace IDs, 100 roots,
zero cross-trace orphans.

The test also publishes intermediate `agent.status.*` messages (working,
completed); the propagation path is identical to the result path, but
the test only asserts on the result trace because that is the
deterministic terminal hop.

---

## 3. How to run

### Prerequisites

- Go 1.25.0 or later.
- No external NATS or Docker required: the test embeds NATS via
  `github.com/nats-io/nats-server/v2/test` (already a transitive test
  dep) and stands up an in-process fake ACP server.

### Run command

```bash
go test -tags=integration -count=1 -timeout 120s \
    ./test/integration/trace-propagation/...
```

This is also wired into the Makefile via the existing `make
test-integration` target (the new directory is picked up by the
`./test/integration/...` glob). Note that `make test-integration` also
runs the docker-compose-based suite; to run *only* the propagation
test:

```bash
go test -tags=integration -count=1 -timeout 120s \
    ./test/integration/trace-propagation/
```

### Skipping

The test does not silently skip. If embedded NATS fails to start
(unusual; would indicate a port-allocation or filesystem-permissions
problem), the test fails loudly with a `t.Fatal`. There is no env-var
gate.

---

## 4. Trace exporter choice

We use `go.opentelemetry.io/otel/sdk/trace/tracetest.InMemoryExporter`.

Rationale:

- The propagation question is "does the trace context survive each
  boundary"; that is fully answerable from the in-memory span graph. A
  real OTLP receiver would add wire-format and gRPC concerns that have
  nothing to do with propagation correctness.
- `InMemoryExporter` is in-tree (`go.opentelemetry.io/otel/sdk`), so
  no new module dependency is needed.
- The test runs the exporter with a `SimpleSpanProcessor` so that
  `ForceFlush` is a no-op and assertions happen synchronously after
  `tp.ForceFlush(ctx)`.

If we ever need wire-format coverage, a follow-up test should use
`otlptracegrpc` against a recorder; that is out of scope for trace
propagation.

---

## 5. Deviations from the prompt

- The prompt called out "Mattermost ingress -> orchestrator" as hop 1.
  No Mattermost ingress exists in this repo today. The test substitutes
  a `test.dispatch` root span that takes the role of the ingress
  service for propagation purposes.
- The prompt said the proxy "spawns the agent CLI as a subprocess and
  pipes stdin/stdout". The current proxy uses TCP, not a subprocess.
  See the audit note above. No `TRACEPARENT` env var injection was
  added because there is no subprocess to receive it.
- The prompt asked for `trace_id` as a first-class field on `Artifact`.
  We added it on `Task.Metadata` (the parent envelope) instead, since
  the artifact's trace ID is by definition the same as the task's.
  Adding it on `Artifact` separately would require a schema change and
  serve no consumer.
