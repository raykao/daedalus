# [EPIC] Phase 5 - Pre-Prod Infrastructure and E2E Validation

## Overview

Phase 4 closed with v0.2.0 and a working AKS deployment, but the path to that cluster was a hand-driven sequence of `az`, `terraform`, `helm`, and `kubectl` commands held together by tribal knowledge. Phase 5 promotes that work into repeatable infrastructure: one Terraform stack, one Make target, one e2e test, one TTL-driven cleanup workflow.

The deliverable is confidence. Any commit on `main` can be validated against a real AKS deployment by triggering one workflow. Engineers can stand up a fresh cluster, run a real task end-to-end through NATS, watch the agent execute, assert the artifact, and tear the cluster back down without asking anyone for the runbook.

Scope is intentionally narrow. One subscription, one region, one public test cluster, manual `workflow_dispatch` trigger. Multi-region, HA, private clusters, and per-PR ephemeral environments are explicitly deferred.

Reference: [Implementation Plan](https://github.com/raykao/daedalus/blob/main/docs/plan.md), [Phase 5 Detailed Plan](https://github.com/raykao/daedalus/blob/main/docs/phase5-plan.md)

### What we are building on

- **Phase 0-3** delivered the platform: proxy, NATS consumer, Helm, KEDA, fan-out orchestrator, dynamic registry, runtime contract, operator CRDs
- **Phase 4** delivered real-world validation: Copilot CLI in ACP mode, end-to-end smoke test, manual AKS deployment, deployment runbook, v0.2.0 tag

Phase 5 takes the manual AKS path from Phase 4 and makes it a single command.

## Anchored Decisions

These are settled. Sub-tasks do not re-litigate.

| Decision | Choice |
|----------|--------|
| IaC tool | Terraform with remote state in Azure Storage; state file never committed |
| Subscription | `<sub-name-redacted>` (UUID configured per-deployment via `subscription_id` tfvar) |
| Region | East US |
| Cluster lifecycle | TTL via RG tags `auto-destroy=true`, `expires-at=<ISO8601>`; 4 hour default |
| Image registry | GHCR (public/dev) and ACR (AKS-pull) |
| Secrets | Workload Identity to Key Vault for `GITHUB_TOKEN` |
| CI trigger | `workflow_dispatch` only |
| AKS sizing | 2x `Standard_D2s_v5` system pool, parameterized |
| Network | Public cluster |
| NATS | PR #30 production overlay, ephemeral storage |
| TF location | `deploy/terraform/` |
| Cleanup | `scripts/aks-cleanup.sh` driven by scheduled GHA |
| E2E driver | Go test + shell-out to `kubectl`/`helm` |
| Observability | Reuse PR #30 overlay (Prometheus, Grafana, Tempo) |

## Phase 5 Tasks

- [ ] **5.1 Terraform module: AKS + ACR + Key Vault + Workload Identity** - Compose `deploy/terraform/` with submodules for RG (TTL-tagged), AKS (OIDC issuer + workload identity), ACR (with AcrPull role), Key Vault (RBAC mode), and a user-assigned managed identity federated to the worker SA. Includes one-time `bootstrap.sh` for the remote state backend. Inputs in `*.tfvars`; only `*.tfvars.example` committed.

- [ ] **5.2 Image build and publish workflow** - `workflow_dispatch` GHA building proxy, mock-acp, and echo-a2a as multi-arch images, pushing to GHCR and mirroring identical digests to ACR. Trivy scan, build provenance attestation, OIDC-based ACR auth (no static secrets).

- [ ] **5.3 Deployment automation** - `make deploy-aks-test` chaining Terraform apply, kubeconfig fetch, KEDA install, NATS install, Helm upgrade install with the AKS values overlay, and readiness wait. Idempotent. Mirror `make destroy-aks-test`.

- [ ] **5.4 E2E test harness** - Go test under `test/e2e/aks/` gated by `//go:build aks_e2e`. Reads `aks.env`, publishes A2A task to NATS, observes status transitions, asserts the artifact, logs end-to-end latency. `KEEP_CLUSTER` opt-out for debugging.

- [ ] **5.5 TTL cleanup** - `scripts/aks-cleanup.sh` listing RGs by tag, parsing `expires-at`, destroying expired groups. Scheduled GHA `nightly-cleanup.yml` runs every 30 minutes plus `workflow_dispatch`. Dry-run mode.

- [ ] **5.6 Documentation and runbook** - Update `docs/aks-deployment.md` and `docs/runbook.md` for the IaC path, bootstrap process, TTL contract, secret rotation, and troubleshooting. Manual Phase 4 instructions preserved but marked superseded.

## Acceptance Criteria

- [ ] An engineer with subscription access and standard tooling can run `make deploy-aks-test` from a clean clone and reach a fully functional Daedalus on AKS in under 25 minutes
- [ ] `make test-aks-e2e` against the provisioned cluster publishes a task to NATS, observes the agent execute, and asserts the artifact within the test timeout
- [ ] `terraform destroy` (or the TTL cleanup workflow) leaves zero residual Azure resources tagged `auto-destroy=true` past their `expires-at`
- [ ] No state file, no real `*.tfvars`, and no secrets are present in `git ls-files`
- [ ] `terraform fmt -check -recursive` and `tflint` are clean and enforced (CI or pre-commit)
- [ ] The `workflow_dispatch` build-and-publish workflow produces matching digests in GHCR and ACR
- [ ] The scheduled cleanup workflow runs successfully and is observable in the Actions tab
- [ ] Documentation in `docs/phase5-plan.md` and updated `docs/aks-deployment.md` is sufficient to bootstrap, plan, apply, deploy, e2e-test, and tear down without tribal knowledge

## Dependencies

- Phase 4 (v0.2.0) - the Helm chart, production overlay (PR #30), and deployment runbook are the foundation
- Azure subscription access in the target Azure subscription (configured per-deployment)
- GitHub repository admin access to configure workflow identities and OIDC federated credentials

## Risks

| Risk | Mitigation |
|------|-----------|
| Cost overrun from forgotten clusters | TTL tags on every RG plus scheduled cleanup workflow; documented manual `az group list` audit |
| Secret exposure (GITHUB_TOKEN in cluster) | Workload Identity to Key Vault; no static secrets in Helm values, env, or state |
| State file leakage | Remote state in Azure Storage with RBAC; `*.tfstate*` and real `*.tfvars` gitignored |
| Bootstrap chicken-and-egg (state backend before TF) | One-time, idempotent `bootstrap.sh` documented in `deploy/terraform/README.md` |
| GHCR/ACR digest drift | Single workflow pushes both registries from one buildx invocation |
| AKS provisioning flakes | Retry policy in `make deploy-aks-test`; clear failure surface in CI logs |
| TTL race during active investigation | `KEEP_CLUSTER=1` opt-out; cleanup script supports `--dry-run` |

## Non-Goals

- Multi-region or multi-cluster topologies
- High availability (single system nodepool is acceptable)
- Private cluster, VPN, or hub-and-spoke networking
- Per-PR ephemeral environments
- NATS persistence across cluster recreation
- Production hardening beyond what PR #30 provides
- New observability stack work (PR #30 overlay reused as-is)

## References

- [Phase 5 Detailed Plan](https://github.com/raykao/daedalus/blob/main/docs/phase5-plan.md)
- [Phase 4 Epic](https://github.com/raykao/daedalus/issues/24)
- [Deployment Runbook](https://github.com/raykao/daedalus/blob/main/docs/runbook.md)
- [AKS Deployment Guide](https://github.com/raykao/daedalus/blob/main/docs/aks-deployment.md)
