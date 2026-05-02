# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- **Phase 5.3 - Automated AKS deployment**: one-command `make deploy-aks-test` (and matching `make destroy-aks-test`) that runs the full sequence - prerequisites check, Azure auth, `terraform apply`, `az aks get-credentials`, namespace upsert, KEDA install (pinned to 2.14.0), workload-identity wiring, `GITHUB_TOKEN` secret, and `helm upgrade --install` of the Daedalus chart - in idempotent steps safe to re-run. New scripts `deploy/scripts/deploy-aks.sh` and `deploy/scripts/destroy-aks.sh` are the source of truth; new Make targets `deploy-aks-test`, `destroy-aks-test`, `aks-credentials`, and `aks-status` are thin wrappers. New chart overlay `deploy/helm/daedalus/values-aks-test.yaml` parameterizes image repositories so each engineer's per-deployment ACR is supplied via `--set` (no hardcoded ACR hostname). Existing `helm-aks-*` Make targets and `deploy/helm/values-aks.yaml` are preserved for backward compatibility and will be cleaned up in Phase 5.6.
- Terraform: GitHub Actions managed identity (`gha-identity` module) with federated credentials for `repo:raykao/daedalus:ref:refs/heads/main`, `repo:raykao/daedalus:pull_request`, and `repo:raykao/daedalus:environment:test`, granted AcrPush on the ACR. Outputs `gha_client_id`, `gha_tenant_id`, `gha_principal_id`, `gha_oidc_subjects`, and `subscription_id` for wiring into GitHub repo variables.
- GitHub Actions workflow `.github/workflows/build-and-publish.yml` (`workflow_dispatch`) that builds proxy, mock-acp, and echo-a2a as multi-arch (linux/amd64, linux/arm64) images, publishes to GHCR, optionally mirrors identical digests to ACR via OIDC (no static secrets), runs Trivy scans (warn HIGH, block CRITICAL), and emits build provenance attestations pushed to the registry.

### Changed

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

### Added

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

### Fixed

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
