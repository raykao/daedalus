# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- Renamed project from Agent Forge to Daedalus to avoid naming collision with unrelated Microsoft project
- Go module: github.com/raykao/daedalus
- CRD API group: daedalus.dev
- Helm chart: deploy/helm/daedalus/
- Docker images: ghcr.io/raykao/daedalus-proxy
- Operator labels/identifiers: daedalus-operator

### Added

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
