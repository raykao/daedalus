# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- AKS Nightly Cleanup workflow now runs once daily at 09:00 UTC instead
  of every 30 minutes. Manual runs remain available via
  `workflow_dispatch`.

### Added

- **MkDocs Material documentation site scaffolded.** New public docs site
  served at <https://daedalus.raykao.io> via GitHub Pages, built with
  MkDocs Material 9.5.x and deployed by `.github/workflows/docs.yml` on
  every push to `main` that touches `docs/**`, `mkdocs.yml`,
  `requirements-docs.txt`, or the workflow itself. PRs build with
  `mkdocs build --strict` (no deploy) so broken anchors and missing nav
  entries fail review (cross-tree file links currently downgraded to
  info; see `mkdocs.yml`). Existing `docs/*.md` content was wired into the
  nav unmodified - no source markdown was rewritten. Internal planning
  docs (`phase5-epic-draft.md`, `phase5-plan.md`, `phase6-options.md`,
  `plan.md`) are listed under `exclude_docs` so they remain in the repo
  but never publish. Mermaid diagrams already embedded in the markdown
  render via the `pymdownx.superfences` custom-fences config. The
  custom domain is preserved across deploys by `docs/CNAME` (copied
  into `site/CNAME` by the mkdocs build). Files added: `mkdocs.yml`,
  `requirements-docs.txt`, `docs/index.md`, `docs/CNAME`,
  `.github/workflows/docs.yml`. `.gitignore` updated to ignore `site/`
  and `.venv-docs/`.
  **One-time manual setup required** (documented in the workflow
  header): GitHub repo Settings -> Pages -> Source = "GitHub Actions",
  custom domain = `daedalus.raykao.io`, and a DNS CNAME record
  `daedalus.raykao.io -> raykao.github.io.` at the registrar.

- **Phase 6 sub-task 6.1 - trace propagation audit, gap-fill, and 100-task concurrent integration test.** Walked every hop of the task pipeline (orchestrator publish, NATS consume, proxy.Handle, ACP `session/new`, ACP `session/prompt`, ACP `session/update` stream, result publish, collector receive) and identified six gaps. Closed all six with the smallest possible code change: `internal/queue/nats.go` now injects W3C `traceparent` into NATS message headers on publish and extracts on consume (each emits a bounded-name `nats.publish` / `nats.consume` span; the subject lives on the canonical `messaging.destination.name` attribute, not in the span name, so dashboard aggregation by operation name stays tractable as task volume grows); `internal/proxy/handler.go` now starts a `proxy.handle` span and wraps ACP calls in `acp.session.new` / `acp.session.prompt` client-kind spans, plus stamps `trace_id` into `Task.Metadata` for downstream consumers that cannot read NATS headers, and reattaches `req.Message.Metadata["trace_id"]` as a remote parent on the proxy side when NATS headers are absent; `internal/orchestrator/collector.go` extracts trace context from result/status NATS headers and additionally falls back to `Task.Metadata["trace_id"]` (as a remote parent, not just an attribute) on the result path when headers are missing - NATS headers always win when present because they carry both trace_id and the publisher's span_id. Status path uses NATS headers only (`a2a.TaskStatus` has no `Metadata` field). Both collector paths emit `collector.receive.result` / `collector.receive.status` consumer spans that re-attach to the publisher's trace. New integration test `test/integration/trace-propagation/` (build tag `integration`) publishes 100 tasks concurrently, drives them through real NATS JetStream + a fake ACP TCP server, and asserts per-task that there is exactly one root span, that the expected core span set is present, that NATS spans carry the right `messaging.destination.name`, that span kinds match the documented tree (Producer/Consumer/Client/Internal), that no span has an unknown parent, and that all spans share one `trace_id`. Aggregate assertions verify exactly 100 distinct trace IDs and 100 roots. The test uses `tracetest.InMemoryExporter` with a `SimpleSpanProcessor` (rationale documented in the test README). Run with `make test-trace-propagation` or `go test -tags=integration ./test/integration/trace-propagation/...`. Files touched outside `internal/telemetry/`: `internal/queue/nats.go`, `internal/proxy/handler.go`, `internal/orchestrator/collector.go`, plus the new `test/integration/trace-propagation/` directory and a Makefile target.
- **Phase 6.3 - structural alert rules and null Alertmanager receiver**:
  - `deploy/helm/daedalus/templates/prometheusrule.yaml`: 7 structural
    alerts (`WorkerImagePullBackOff`, `WorkerCrashLoopBackOff`,
    `NATSConsumerLagUnbounded`, `KEDAScalerError`, `OTelCollectorDown`,
    `OrchestratorDown`, `NATSStreamUnhealthy`), grouped into
    `daedalus.platform` and `daedalus.dependencies`. Each alert carries
    `severity` (`page` or `warn`), a `daedalus_component` label, and a
    `runbook_url` pointing at `docs/runbook.md` anchors authored in
    sub-task 6.4. Per `docs/observability.md` § 3, no SLO threshold
    alerts in Pass 1 by design.
  - `deploy/helm/daedalus/templates/alertmanagerconfig.yaml`: default
    `null` receiver named `daedalus-default` so a fresh deploy provisions
    the alerts without paging anyone. The receiver type is switchable to
    `mattermost`, `github`, or `pagerduty` via a three-line values
    override (see `docs/runbook.md` § "Alertmanager receiver override").
  - `deploy/helm/daedalus/values.yaml`: new top-level `alerting:` section
    (`enabled`, `prometheusOperator.releaseLabel`,
    `alertmanagerConfig.{enabled,receiver.{type,config}}`). Both the
    `PrometheusRule` and the `AlertmanagerConfig` are individually gated.
  - `deploy/helm/daedalus/tests/alerts_test.sh`: shell-based Helm chart
    test that asserts (a) all 7 alerts and runbook anchors render under
    default values, (b) `alerting.enabled=false` produces zero
    `PrometheusRule` / `AlertmanagerConfig` resources, (c) switching the
    receiver to `mattermost` or `pagerduty` renders the matching
    receiver block. 22/22 assertions green.
  - **Pass 1 review fixes** (fresh-eyes cross-check of the rendered
    rule against the spec table): corrected four PromQL defects that
    were also present in the spec table in `docs/observability.md` § 3
    and are now fixed in both places. (1) `WorkerCrashLoopBackOff`
    threshold lowered from `> 0.3` (unreachable, equivalent to 180
    restarts in 10m) to `> 0` held for 10m. (2) `NATSConsumerLagUnbounded`
    rewritten to use gauges nats-surveyor actually exposes
    (`delta(num_pending[10m]) > 0 and num_pending > 100`); the original
    counter `nats_jetstream_consumer_acks_total` does not exist.
    (3) `NATSStreamUnhealthy` capacity ratio guarded by `max_bytes > 0`
    to prevent `+Inf > 0.9` false positives on unlimited streams, and
    annotated with explicit `on(account, stream_name)` vector matching.
    (4) `OrchestratorDown` OR'd with `absent(...)` so the alert fires
    when the orchestrator deployment is missing entirely (today's
    state) and not only when it exists with zero available replicas.
    `docs/observability.md` § 3 gains a "Spec corrections from Pass 1
    implementation" subsection recording these. Also: the GitHub
    receiver test URL switched from `https://api.github.com/...` (which
    contradicts the runbook) to the in-cluster translator pattern
    `http://alertmanager-github-receiver.monitoring.svc.cluster.local:8080/v1/webhook`,
    and a new test asserts that
    `alerting.alertmanagerConfig.enabled=false` leaves the
    `PrometheusRule` rendered while suppressing the `AlertmanagerConfig`.
    Test count is now 32/32 green. Pass 2 review hygiene fixes:
    `NATSConsumerLagUnbounded` description no longer claims "zero acks"
    (counter doesn't exist; expr uses gauges); `OrchestratorDown`
    description rewritten so it renders correctly when fired via the
    `absent(...)` branch (no `$labels.namespace` reference); spec table
    row for `NATSStreamUnhealthy` in `docs/observability.md` § 3 now
    includes the explicit `and on(account, stream_name)` join that the
    rule already carried.
  - **Cross-check 2 fixes** (second fresh-eyes pass against nats-
    surveyor's actual metric exposition and AlertmanagerConfig sub-
    route routing semantics, six corrections): (CC2-1, HIGH / merge
    blocker) `OrchestratorDown` `absent()` leg gains a `namespace`
    label matcher and the alert carries a static `namespace` label,
    so the AMC sub-route's namespace matcher matches and the alert
    no longer escapes to the global Alertmanager default. (CC2-2)
    `NATSConsumerLagUnbounded` switched from the non-existent
    `nats_jetstream_consumer_num_pending` to `nats_consumer_num_pending`
    and from `$labels.consumer` to `$labels.consumer_name`, matching
    nats-surveyor's JSZ exposition. (CC2-3) `NATSStreamUnhealthy`
    rewritten to use surveyor-real metrics: the original used three
    fictional series (`nats_jetstream_stream_messages_lost_total`,
    `nats_jetstream_stream_max_bytes`,
    `nats_jetstream_stream_storage_bytes`); none exist. Replaced with
    `nats_stream_consumer_count == 0 and nats_stream_total_messages > 0`
    for 10m, which catches the drainage-failure mode the lost-messages
    leg was meant to cover. The capacity-ratio leg is dropped because
    surveyor exposes no per-stream storage cap. (CC2-4)
    `OTelCollectorDown` gains an `absent()` leg and a static
    `namespace` label so the alert fires when the collector
    deployment is missing entirely and routes through the AMC
    regardless of the source `up{}` series' label set. (CC2-5)
    `KEDAScalerError` scoped to the chart's release namespace
    (`keda_scaler_errors_total{namespace="<release-ns>"}`) so other
    teams' ScaledObject errors do not page Daedalus. (CC2-6)
    PagerDuty example secret name unified to `pagerduty-routing-key`
    across `values.yaml`, `docs/runbook.md`, and the chart test (was
    three different names). Verification source: nats-io/nats-surveyor
    `surveyor/collector_statz.go` @ commit 725f52d (no version
    pinning in the chart; assumption recorded in rule comments).
    `docs/observability.md` § 3 gains a "Spec corrections from
    Cross-check 2" subsection. Test count is now 42/42 green.
  - **Cross-check 2 in-loop follow-up**: same CC2-1 class of bug
    (alert series lacks release-namespace label, escapes AMC sub-
    route's `namespace=<release-ns>` matcher, falls through to global
    Alertmanager default) was missed on both NATS alerts during the
    cycle 8 fix. `NATSConsumerLagUnbounded` and `NATSStreamUnhealthy`
    fire on `nats_consumer_*` / `nats_stream_*` series from nats-
    surveyor (which typically runs in its own namespace, not the
    daedalus release namespace), so the source series' `namespace`
    label does not match. Both alerts now carry a static
    `namespace: "{{ .Release.Namespace }}"` rule label. Defense-in-
    depth: the same static label is also applied to
    `WorkerImagePullBackOff` / `WorkerCrashLoopBackOff` to harden
    against future ServiceMonitor relabeling drift. A new
    `alerts_test.sh` assertion regression-guards every rendered alert
    carrying a static `namespace` label. Test count is now 43/43
    green.
  - **Cross-check 3 fixes** (third fresh-eyes pass; one High deferred,
    four verified true-positives applied): (CC3-1, Medium)
    `OTelCollectorDown` expr selector dropped its `namespace=` matcher
    on `up{}`. The kube-prometheus-stack overlay (PR #30) deploys the
    OTel collector in `monitoring`, not the chart's release namespace,
    so the previous `namespace=<release-ns>` matcher would never match
    and `absent()` would be permanently true. The static
    `namespace="<release-ns>"` rule label is retained for AMC routing.
    (CC3-2, Medium) `NATSConsumerLagUnbounded` and `NATSStreamUnhealthy`
    expr selectors now filter by `stream="<keda.natsStream>"`
    (default `"AGENT_TASKS"`) so a shared nats-surveyor watching
    multiple streams cannot fire either alert on an unrelated team's
    stream and mis-attribute it to `daedalus_component=nats`.
    (CC3-3, Low) `WorkerCrashLoopBackOff` description corrected from
    "last 10 minutes" to "across the last ~20 minutes (rate window
    10m, held 10m)"; the previous wording understated the effective
    window by 10 minutes since `rate[10m]` is held `for: 10m`.
    (CC3-4, Low) `docs/observability.md` Pass 1 Finding 5 entry gains
    a forward note clarifying that the `on(account, stream)` clause
    described an intermediate division expression that CC2-3 later
    replaced with `nats_stream_consumer_count == 0 and
    nats_stream_total_messages > 0` (no explicit matching clause
    needed; both metrics carry the same label set). The `stream_name`
    typo in that historical finding text is corrected to `stream`
    to match nats-surveyor's actual label name. The CC3 High finding
    on the runbook's PagerDuty secret namespace guidance is deferred
    to a separate PR: the chart's default receiver is `null`,
    PagerDuty is not wired or testable without real PagerDuty
    credentials, and the runbook fix is best authored alongside an
    actual PagerDuty configuration. Two new `alerts_test.sh`
    assertions regression-guard the `stream=` filter on each NATS
    alert; the OTel test was strengthened to negatively assert the
    expr selector contains no `namespace=` matcher. Test count is
    now 46/46 green.
  - `docs/runbook.md`: new "Alertmanager receiver override" section with
    copy-pasteable Mattermost / GitHub / PagerDuty values blocks. Per-
    alert runbook entries (means / reproduce / diagnose / mitigate) are
    deferred to sub-task 6.4 by design.
- `docs/observability.md`: Pass 1 implementation spec for Phase 6 / Option C observability deepening. Covers trace ID end-to-end audit and gap-fill, per-agent-type fleet dashboards (cold-start, queue depth, throughput, error rate, task-to-artifact latency, top-slow tasks linked to Tempo), structural alert rules (no SLO thresholds in Pass 1), runbook entries, and a Phase 5 acceptance-criteria coverage appendix that maps AC1/AC2/AC5 to specific signals. Strategic rationale lives in `raykao/dark-factory:research/daedalus-observability.md`. Implementation handoff doc only - no platform code or behavior change.
- `deploy/helm/daedalus/dashboards/.gitkeep`: landing zone for Pass 1 Grafana dashboard JSON.
- `docs/phase6-options.md`: scoping/options menu for the next phase. Enumerates seven candidate directions (session-resurrection validation, second agent type, observability deepening, multi-replica orchestrator, production hardening, K8s operator, out-of-band) with maturity gates, scope, and trade-offs. No phase is committed; the doc is a decision input for the human to pick from.
- **Phase 5.6 - documentation rewrite for the IaC path**: rewrote
  `docs/runbook.md` and `docs/aks-deployment.md` for the Phase 5
  `make deploy-aks-test` flow. The runbook covers the prereq checklist,
  one-time bootstrap, tfvars, `GITHUB_TOKEN` export, idempotent deploy
  (~25 min cold), TTL contract (re-apply slides `expires-at = now +
  ttl_hours`), secret rotation (re-export `GITHUB_TOKEN` and re-deploy;
  ACR pull is via kubelet identity AcrPull so no SP credential to
  rotate; Key Vault CSI mount deferred), the `nightly-cleanup.yml`
  workflow (cron every 30 min, `dry_run` and `prefix` inputs, empty-
  prefix safety net), and teardown via `make destroy-aks-test` or
  TTL expiry. The deployment guide adds an updated architecture
  diagram, a 14-step expansion of `deploy-aks.sh`, a configuration
  table for `values-aks-test.yaml`, and a troubleshooting appendix
  covering Terraform state lock, AcrPull missing on the kubelet
  identity, Key Vault access denied (workload identity / namespace
  label / SA annotation), OIDC issuer not enabled
  (`AADSTS70021`), and GitHub Actions OIDC subject mismatch
  (`AADSTS70021` against the GHA UAMI's federated subjects). The Phase
  4 hand-driven flow is preserved verbatim in a clearly-marked
  superseded appendix. `deploy/terraform/README.md` gained a top-of-file
  callback ("most engineers should not run terraform directly") and a
  Cross-references section pointing at the runbook and deployment
  guide.
- **Phase 5.5 - TTL cleanup**: `scripts/aks-cleanup.sh` and
  `.github/workflows/nightly-cleanup.yml` for scheduled destruction of
  expired AKS test resource groups. The script lists RGs tagged
  `auto-destroy=true` and deletes any whose `expires-at` (RFC 3339 UTC) is
  in the past; missing or unparseable tags are skipped with a warning,
  never destroyed on parse error. Required `--prefix` safety net (or
  explicit `--all-prefixes`) prevents reaping unrelated tagged RGs. The
  workflow runs every 30 minutes plus on-demand `workflow_dispatch` (with
  optional `dry_run` and `prefix` overrides). Cleanup auth uses the
  existing GHA UAMI with a new subscription-scoped Contributor role
  assignment (`var.enable_cleanup_role`, default true; documented
  trade-off vs a future custom-role hardening). New Make targets
  `cleanup-aks-test` and `test-cleanup-script`. Bash unit tests cover the
  parse/decision/dispatch logic via a PATH-shimmed `az` (no Azure access
  required).
- **Phase 5.5 operational details**: `az group delete` stderr (e.g.,
  `AuthorizationFailed`) is surfaced in the `[WARN]` log line so failures
  are diagnosable from workflow logs alone. The cleanup job is capped at
  `timeout-minutes: 15` to prevent a hung `az` call from holding the GHA
  concurrency slot. Dispatching the workflow with an empty `prefix` input
  deliberately triggers the script's safety net (exit 2) rather than
  being silently substituted with a default; operators must supply a
  non-empty `--prefix` or use `--all-prefixes`.
- E2E test harness for AKS deployments (Phase 5.4): `test/e2e/aks/aks_e2e_test.go` gated by `aks_e2e` build tag, Makefile target `test-aks-e2e`, `aks.env.example`. Random sentinel-based artifact assertion in the AKS e2e harness to prevent false-pass on prompt-echoing agents (the prompt and the asserted artifact body both embed a per-run `crypto/rand` 16-hex-char sentinel, so an LLM that echoes its prompt back as conversational text cannot satisfy the assertion without actually surfacing the sentinel via a side-effect that the proxy serializes as artifact content).
- `KEEP_CLUSTER` opt-out for debugging.
- **Phase 5.3 - Automated AKS deployment**: one-command `make deploy-aks-test` (and matching `make destroy-aks-test`) that runs the full sequence - prerequisites check, Azure auth, `terraform apply`, `az aks get-credentials`, namespace upsert, KEDA install (pinned to 2.14.0), workload-identity wiring, `GITHUB_TOKEN` secret, and `helm upgrade --install` of the Daedalus chart - in idempotent steps safe to re-run. New scripts `deploy/scripts/deploy-aks.sh` and `deploy/scripts/destroy-aks.sh` are the source of truth; new Make targets `deploy-aks-test`, `destroy-aks-test`, `aks-credentials`, and `aks-status` are thin wrappers. New chart overlay `deploy/helm/daedalus/values-aks-test.yaml` parameterizes image repositories so each engineer's per-deployment ACR is supplied via `--set` (no hardcoded ACR hostname). Existing `helm-aks-*` Make targets and `deploy/helm/values-aks.yaml` are preserved for backward compatibility and will be cleaned up in Phase 5.6.
- Terraform: GitHub Actions managed identity (`gha-identity` module) with federated credentials for `repo:raykao/daedalus:ref:refs/heads/main`, `repo:raykao/daedalus:pull_request`, and `repo:raykao/daedalus:environment:test`, granted AcrPush on the ACR. Outputs `gha_client_id`, `gha_tenant_id`, `gha_principal_id`, `gha_oidc_subjects`, and `subscription_id` for wiring into GitHub repo variables.
- GitHub Actions workflow `.github/workflows/build-and-publish.yml` (`workflow_dispatch`) that builds proxy, mock-acp, and echo-a2a as multi-arch (linux/amd64, linux/arm64) images, publishes to GHCR, optionally mirrors identical digests to ACR via OIDC (no static secrets), runs Trivy scans (warn HIGH, block CRITICAL), and emits build provenance attestations pushed to the registry.
- Phase 5 planning: docs/plan.md updated with Phase 5 section, docs/phase5-plan.md detailed sub-task breakdown, docs/phase5-epic-draft.md issue body template
- Phase 5.1 Terraform module under deploy/terraform/ for AKS + ACR + Key Vault + Workload Identity + RG with TTL tags
  - Modular layout: modules/{rg,aks,acr,keyvault,identity}
  - Bootstrap script (deploy/terraform/bootstrap/bootstrap.sh) for one-time remote-state storage account creation
  - Example tfvars (deploy/terraform/envs/test.tfvars.example), README, and bootstrap docs
  - 4-hour TTL via auto-destroy + expires-at tags (cleanup script comes in Phase 5.5)
- AKS configuration:
  - OIDC issuer + Workload Identity enabled
  - Federated credential pre-bound to system:serviceaccount:daedalus:daedalus-proxy
  - Default 2x Standard_D2s_v5 system pool, 75 GB managed OS disk
  - upgrade_settings { max_surge = "1" } for predictable upgrade behavior
- ACR Standard SKU with AcrPull role auto-granted to AKS kubelet identity
- Key Vault with RBAC authorization, Secrets User role for the workload identity, Secrets Officer for the deployer
- Resource group with TTL tags (auto-destroy=true, expires-at=ISO8601) for downstream cleanup tooling
- `*conn.WriteRequestAwaitResponse` and bidirectional request/response support in `mock-acp-server` to model the v1 permission flow.
- Unit tests covering the permission flow (`TestPermissionRequest` asserts the request shape; `TestPermissionDenied` asserts deny path; `TestConn_WriteRequestAwaitResponse_UnblocksOnClose` asserts disconnect cleanup).

### Changed

- **Phase 5.6**: `helm-aks-logs` Make target renamed to `aks-logs` for
  consistency with the other Phase 5 `aks-*` targets. No functional
  change.
- AKS e2e harness preflight no longer calls `CreateOrUpdateStream` - it now creates streams only when missing, preserving any operator-tuned config on persistent clusters.
- Terraform: `github_oidc_subjects` (root) and `subjects` (gha-identity module) now require every entry to start with `repo:<github_owner>/<github_repo>:`, refusing cross-repo OIDC trust at validation time instead of silently federating a UAMI with AcrPush to another repo's workflows. Bumped root `required_version` to `>= 1.9.0` so cross-variable references are available in `validation` blocks.
- Terraform: `gha-identity` module now suffixes each federated credential resource name with a 6-char hash of the full subject (e.g. `gha-fic-main-3f9a2b`) so subjects whose human-readable parts collapse to the same short name (e.g. `refs/heads/feat-foo` vs `refs/heads/feat/foo`) no longer collide on Azure mid-apply.
- Terraform `acr` module accepts `additional_push_principal_ids` for granting AcrPush to non-AKS principals (used by the GHA identity).
- Terraform README documents the post-apply GitHub repo variable wiring (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `ACR_NAME`, `ACR_LOGIN_SERVER`) and how to verify federated credential subjects against the workflow's triggers.
- `Dockerfile.proxy` now cross-compiles via `--platform=$BUILDPLATFORM` + `GOARCH=$TARGETARCH` (replacing the hard-coded `GOARCH=amd64`) so multi-arch buildx runs produce correct linux/arm64 binaries.
- Replaced monolithic deploy/terraform/*.tf (Phase 4) with modular Phase 5 layout
- `mock-acp-server` now speaks ACP protocol v1 to match `internal/acp/client.go` and the real `@github/copilot@1.0.36`:
  - `protocolVersion`: integer `1` (was string `"2025-01-01"`)
  - `session/new` field renamed to `cwd` (was `workDir`)
  - `session/prompt.prompt` is an array of `{type, text}` content parts (was string)
  - Streaming notification: `session/update` with `sessionUpdate: agent_message_chunk` (was `assistant.message_delta`)
  - `session/request_permission` is now a JSON-RPC request awaiting `{outcome:{optionId:"allow_once"}}` (was a fire-and-forget notification); the mock validates the returned `optionId` and rejects deny/empty/malformed responses

### Removed

- **Phase 5.6**: removed the back-compat Phase 4 Make targets
  `helm-aks-deploy`, `helm-aks-teardown`, and `helm-aks-status`
  (superseded by Phase 5.3's `make deploy-aks-test`,
  `make destroy-aks-test`, and `make aks-status`). Removed
  `deploy/helm/values-aks.yaml` (superseded by
  `deploy/helm/daedalus/values-aks-test.yaml`, overridden by the deploy
  script via `--set proxy.image.repository` /
  `workers[0].image.repository` to point at each engineer's
  per-deployment ACR).

### Fixed

- AKS e2e harness now publishes tasks to the worker's queue subject (default `agent.tasks.copilot`, override via `WORKER_SUBJECT`), not a per-task subject - fixes silent timeout against AKS deployments where the proxy uses an exact-match consumer filter. Caught by independent second-opinion review.
- `build-and-publish` workflow: Trivy now authenticates to GHCR (via `TRIVY_USERNAME`/`TRIVY_PASSWORD` env) and scans both published platforms (linux/amd64 and linux/arm64) instead of only the host platform. Without auth, scans of new (private-by-default) GHCR packages failed with `unauthorized`; without per-platform scans, arm64-only CVEs silently bypassed the gate.
- (Phase 5.1 review fixes, all in implementation commits before merge): KV name truncation now preserves uniqueness suffix; ACR has deterministic global-uniqueness suffix; bootstrap script uses account-key auth and grants required RBAC; node_vm_size validated against D-series allowlist; OS disk type set to Managed (Ephemeral incompatible with Dsv4/Dsv5 cache-less SKUs)
- `waitForNATS` use-after-close bug: `nc.Close()` was called before `js.AccountInfo(ctx)` so the readiness probe always failed and `TestEndToEnd_CompletedTask` timed out at the 60s deadline. Refactored into a `probeNATS()` helper with `defer nc.Close()`.
- `OrderedConsumer` late-bind race in `compose_test.go::TestEndToEnd_CompletedTask`: a fast `working` status update could arrive before the consumer was server-side bound, causing a flaky failure. Switched to `CreateOrUpdateConsumer` with `DeliverByStartTimePolicy` so the start point is set before the publish.
- Goroutine leak when a client disconnects mid-permission-roundtrip: pending awaiters now wake immediately on `*conn.Close()` via a per-conn `done` channel, instead of waiting for the 30s `WriteRequestAwaitResponse` timeout.

## [0.2.0] - 2026-05-01

### Changed

- Renamed project from Agent Forge to Daedalus to avoid naming collision with unrelated Microsoft project
- Go module: github.com/raykao/daedalus
- CRD API group: daedalus.dev
- Helm chart: deploy/helm/daedalus/
- Docker images: ghcr.io/raykao/daedalus-proxy
- Operator labels/identifiers: daedalus-operator

### Added

- `docs/runbook.md` - Deployment runbook: step-by-step guide for deploying Daedalus to AKS in under 30 minutes (Phase 4.5)
- `deploy/helm/values-example-production.yaml` - Annotated example values overlay for production deployments (Phase 4.5)
- Copilot CLI Dockerfile for ACP server mode (Phase 4.1)
- Docker Compose stack for real Copilot CLI validation
- Validation script for end-to-end proxy + Copilot CLI testing
- End-to-end smoke test suite for Copilot CLI validation with latency measurement (Phase 4.2)
- Timing instrumentation in validation script with portable macOS support (Phase 4.2)
- Makefile targets for integration and smoke tests (Phase 4.2)
- Smoke test documentation with setup, troubleshooting, and debugging guide (Phase 4.2)
- `deploy/terraform/` - Terraform module for test AKS cluster (Phase 4.3): provisions resource group, AKS cluster, ACR, and KEDA operator
- `deploy/helm/values-aks.yaml` - AKS-specific Helm values overlay with KEDA enabled and ACR image references (Phase 4.4)
- Makefile targets for AKS Helm deployment: `helm-aks-deploy`, `helm-aks-teardown`, `helm-aks-status`, `helm-aks-logs` (Phase 4.4)
- `test/scripts/validate-aks-deployment.sh` - validation script for KEDA ScaledJob triggers, scale-to-zero, cold start latency, and SIGTERM graceful shutdown on AKS (Phase 4.4)
- `docs/aks-deployment.md` - AKS deployment guide with configuration reference and troubleshooting (Phase 4.4)

### Fixed

- `go.mod` requires Go 1.25; update all Dockerfile builder images from `golang:1.24-alpine` to `golang:1.25-alpine`
- Copilot CLI Docker image: correct npm package from `@github/copilot-cli` (does not exist) to `@github/copilot@1.0.36`
- NATS box image tag updated from `0.14` (non-existent) to `nats-box:0.19.5`
- ACP protocol v1 breaking changes in `@github/copilot@1.0.36`: `protocolVersion` is integer `1` (not string), `session/new` uses `cwd` field (not `workDir`), `session/prompt` prompt is an array of content parts
- Docker networking: Copilot CLI binds ACP listener to loopback only; fix via `network_mode: "service:copilot-cli"` so proxy shares the network namespace
- ACP client `readLoop` context lifetime: decoupled from caller's connect context (`context.Background()`) to prevent premature goroutine termination
- ACP `session/request_permission` is a server-to-client JSON-RPC request (not a notification); proxy now sends a proper response `{"outcome":{"optionId":"allow_once"}}` (confirmed from CLI source inspection)
- ACP content streaming: CLI v1.0.36 sends `session/update` with `sessionUpdate:"agent_message_chunk"` for user-visible text; replaced defunct `assistant.message_delta` handler
- Unhandled ACP server-to-client requests now send JSON-RPC method-not-found error (-32601) instead of being silently dropped, preventing server-side hangs
- `smoke.env` credential template renamed to `smoke.env.example` (safe to commit); `smoke.env` remains gitignored to prevent accidental token commits

## Phase 3 - Pluggable Runtime and Operator

### Added

- Runtime Contract v1 with pluggable runtime interface and conformance test suite (Phase 3.1, PR #20)
- Kubebuilder operator with 5 CRDs and reconcilers for agent lifecycle management (Phase 3.2, PR #21)
- Context management with R18 resurrection strategy (Phase 3.3, PR #22)
  - Resurrection strategy and context usage status added to AgentRuntime CRD
  - Session tracker with R18 resurrection strategy
  - Context management config injection into proxy environment variables
  - Proxy integration for context usage metrics
- Multi-runtime integration test suite (Phase 3.4, PR #23)
  - Echo A2A server for multi-runtime testing
  - Docker Compose stack for multi-runtime environments
  - End-to-end integration tests across runtimes

### Fixed

- Review findings for context management (PR #22)
- Repo root path traversal in multi-runtime integration test (PR #23)

## Phase 2 - Multi-Agent and Fan-Out

### Added

- Fan-out task dispatch for parallel agent coordination (Phase 2.1, PR #15)
- Dynamic AgentCard registry with NATS KV for runtime service discovery (Phase 2.4, PR #16)
- Dependency-ordered DAG scheduler for task orchestration (Phase 2.2)
- Phase 2 design decisions and task deliverables documentation (PR #14)

## Phase 1 - Deployment and Scaling

### Added

- Helm chart for Kubernetes deployment (Phase 1.1)
- KEDA ScaledJob for scale-to-zero workers (Phase 1.2)
- OpenTelemetry observability stack with tracing and metrics (Phase 1.3)
- Contract tests with JSON Schema validation (Phase 1.4)

## Phase 0 - Foundation and Validation

### Added

- Queue-to-ACP Proxy prototype (Phase 0.1)
- ACP validation harness and mock server (Phase 0.2)
- Docker Compose integration test stack (Phase 0.3)
- SIGTERM graceful shutdown with ACP session cancel (Phase 0.4)
- Structured branch naming for worker sessions (Phase 0.5)
- Static AgentCard registry with scored routing (Phase 0.6)
- Initial repo setup with research and implementation plan
- Pluggable runtime architecture and Phase 3 operator plan
- ACP as intra-pod protocol with four-layer architecture model
- kagent comparison and CRD schema analysis
- A2A protocol decision: data model yes, transport no
