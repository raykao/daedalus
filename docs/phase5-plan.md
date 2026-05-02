# Phase 5 - Pre-Prod Infrastructure and E2E Validation - Detailed Plan

This document expands the Phase 5 sub-tasks defined in [plan.md](plan.md). It is the working spec the implementation branches will reference.

## Summary

Phase 5 turns the manual Phase 4 AKS path into an idempotent, TTL-bounded, single-command operation backed by Terraform, plus an automated end-to-end test that drives a real task through NATS on a freshly provisioned cluster. Scope is intentionally narrow: one subscription, one region, one public test cluster, manual `workflow_dispatch` trigger.

### Anchored Decisions

These are settled. The implementation branches do not re-litigate them.

| Item | Value |
|------|-------|
| Subscription | `<sub-name-redacted>` (`00000000-0000-0000-0000-000000000000`) |
| Region | `eastus` |
| IaC | Terraform, remote state in Azure Storage, no state in git |
| Cluster TTL | 4 hours via RG tags `auto-destroy=true`, `expires-at=<ISO8601>` |
| Registries | GHCR (public) + ACR (AKS-pull) |
| Secrets | Workload Identity to Key Vault for `GITHUB_TOKEN` |
| CI trigger | `workflow_dispatch` only |
| AKS sizing | 2x `Standard_D2s_v5` (parameterized) |
| Network | Public cluster |
| NATS | PR #30 production overlay, ephemeral storage |
| TF location | `deploy/terraform/` |
| Observability | PR #30 overlay reused as-is |

---

## 5.1 Terraform Module: AKS + ACR + Key Vault + Workload Identity

### Deliverables

```
deploy/terraform/
  README.md                       # prerequisites, bootstrap, plan, apply, destroy
  bootstrap.sh                    # one-time: creates RG + storage account + container for tfstate
  versions.tf                     # required_providers (azurerm ~> 4.x, azuread ~> 3.x), required_version
  backend.tf                      # azurerm backend block; values come from -backend-config
  providers.tf                    # azurerm provider with subscription_id, features
  variables.tf                    # all inputs with descriptions and validation blocks
  locals.tf                       # naming, tag composition (including TTL tags)
  main.tf                         # composes the submodules
  outputs.tf                      # kubeconfig fetch hints, ACR login server, KV URI, identity client_id
  envs/
    test.tfvars.example           # committed sample
    test.tfvars                   # gitignored
  modules/
    rg/                           # azurerm_resource_group with auto-destroy and expires-at tags
    aks/                          # azurerm_kubernetes_cluster, system nodepool, OIDC issuer enabled
    acr/                          # azurerm_container_registry, AcrPull role assignment to AKS kubelet identity
    keyvault/                     # azurerm_key_vault, RBAC mode, network ACL allow-list
    identity/                     # azurerm_user_assigned_identity + federated credential for the workload SA
```

### Architecture

- **rg**: Resource group named `rg-daedalus-test-<suffix>`. Tags: `auto-destroy=true`, `expires-at=<computed ISO8601>`, `owner`, `phase=5`, `managed-by=terraform`. The `expires-at` tag is computed via `timeadd(timestamp(), var.ttl_hours * 3600s)` at apply time.
- **aks**: AKS with `oidc_issuer_enabled = true`, `workload_identity_enabled = true`, system node pool only, kubenet or Azure CNI overlay (decided in implementation), `local_account_disabled = true`, AAD/RBAC integration. KEDA installed via Helm, not the AKS addon, for version control parity with PR #30.
- **acr**: Standard SKU, admin disabled. Role assignment grants the AKS kubelet identity `AcrPull` on this ACR.
- **keyvault**: RBAC-mode, purge protection enabled, soft-delete 7 days. Secret `github-token` is created out-of-band by the workflow (not by Terraform) so the secret never touches state.
- **identity**: A user-assigned managed identity. Federated credential binds it to the K8s service account `daedalus-worker` in the `daedalus` namespace via the AKS OIDC issuer URL. RBAC: `Key Vault Secrets User` on the KV scope.

### Variables

```hcl
variable "subscription_id"  { type = string }
variable "location"         { type = string  default = "eastus" }
variable "name_suffix"      { type = string  description = "short suffix; appended to all resource names" }
variable "ttl_hours"        { type = number  default = 4 }
variable "node_count"       { type = number  default = 2 }
variable "node_vm_size"     { type = string  default = "Standard_D2s_v5" }
variable "k8s_version"      { type = string }
variable "tags"             { type = map(string)  default = {} }
```

Each variable has a `validation` block where applicable (e.g. `ttl_hours > 0 && ttl_hours <= 24`).

### Outputs

`resource_group_name`, `aks_name`, `acr_login_server`, `keyvault_uri`, `workload_identity_client_id`, `oidc_issuer_url`, `kubeconfig_command` (a string the Makefile can `eval` to fetch credentials).

### Bootstrap (chicken/egg)

`deploy/terraform/bootstrap.sh`:

1. `az account set --subscription <id>`
2. Create `rg-daedalus-tfstate` (no TTL; this is permanent)
3. Create storage account `stdaedalustfstate<suffix>` with versioning and soft-delete
4. Create container `tfstate`
5. Print the `-backend-config=...` flags for `terraform init`

Idempotent: re-runs are safe.

### Validation / Acceptance

- `terraform fmt -recursive -check` clean
- `tflint` clean (config in `deploy/terraform/.tflint.hcl`)
- `terraform validate` clean
- `terraform plan` produces a non-empty diff against an empty state and zero diff after apply
- `terraform destroy` removes everything in the RG cleanly
- No `.tfstate`, no real `.tfvars`, and no secrets are present in `git ls-files`

### Gitignore additions

```
deploy/terraform/**/*.tfstate
deploy/terraform/**/*.tfstate.*
deploy/terraform/.terraform/
deploy/terraform/**/.terraform.lock.hcl  # decision: pin per-module or commit; default: commit
deploy/terraform/envs/*.tfvars
!deploy/terraform/envs/*.tfvars.example
deploy/terraform/**/crash.log
```

(Whether to commit `.terraform.lock.hcl` is decided during 5.1 implementation; the safe default is to commit.)

### Dependencies

None within Phase 5. Must complete before 5.3 and 5.4.

---

## 5.2 Image Build and Publish Workflow

### Deliverables

- `.github/workflows/build-and-publish.yml` (GHA, `workflow_dispatch` only for Phase 5)
- Updated `Dockerfile`s if needed for multi-arch builds

### Behavior

- **Trigger**: `workflow_dispatch` with inputs `tag` (default `dev-<sha>`), `push_to_acr` (bool, default true)
- **Matrix**: `[proxy, mock-acp, echo-a2a]` x `[linux/amd64, linux/arm64]`
- **Steps per image**:
  1. Checkout
  2. Set up QEMU + Buildx
  3. Login to GHCR via `GITHUB_TOKEN`
  4. Login to ACR via `azure/login@v2` with OIDC (federated credential bound to the workflow), then `az acr login`
  5. `docker buildx build --platform linux/amd64,linux/arm64 --push -t ghcr.io/raykao/daedalus/<image>:<tag> -t <acr>.azurecr.io/daedalus/<image>:<tag>`
  6. Trivy scan; non-blocking warn for HIGH, blocking for CRITICAL (configurable)
  7. Generate provenance attestation via `actions/attest-build-provenance`

### Credentials

- GHCR: built-in `GITHUB_TOKEN` (already available)
- ACR: federated credential on a workflow-scoped managed identity. Stored as repo variables: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`. No client secrets.

### Validation / Acceptance

- A successful run produces identical digests in GHCR and ACR
- Workflow re-run is idempotent (same digest if source unchanged)
- `kubectl run --image=<acr>.azurecr.io/daedalus/proxy:<tag>` succeeds on the AKS cluster (validates AcrPull role)

### Dependencies

- 5.1 must have produced ACR and the workflow identity federated credential

---

## 5.3 Deployment Automation

### Deliverables

- `Makefile` targets: `deploy-aks-test`, `destroy-aks-test`, `aks-credentials`, `aks-status`
- `deploy/scripts/deploy-aks.sh` (shellable, the Make target wraps it)
- `deploy/helm/daedalus/values-aks-test.yaml` (extends the PR #30 production overlay; pins to ACR images)

### `make deploy-aks-test` flow

1. Verify prerequisites (`az`, `terraform`, `kubectl`, `helm`, `jq`)
2. `az login --use-device-code` if not already authenticated; set subscription
3. `terraform -chdir=deploy/terraform init -backend-config=...`
4. `terraform -chdir=deploy/terraform apply -var-file=envs/test.tfvars -auto-approve`
5. Fetch kubeconfig via `az aks get-credentials`
6. Create namespace, install KEDA, install NATS (PR #30 overlay), wait for readiness
7. `helm upgrade --install daedalus deploy/helm/daedalus -f values-aks-test.yaml --set image.tag=<resolved>`
8. Bind workload identity SA, mount KV CSI driver if used
9. Smoke poll: every component Ready or fail with full logs

Idempotent: re-running on an existing cluster is a no-op or rolls forward.

### Validation / Acceptance

- Wall-clock from clean clone to "Ready" under the threshold defined in plan.md acceptance criteria (target 25 minutes)
- Re-running the target on an existing cluster exits 0 with no destructive changes
- `make destroy-aks-test` returns the subscription to its prior state

### Dependencies

- 5.1 (Terraform), 5.2 (images in ACR)

---

## 5.4 E2E Test Harness

### Deliverables

- `test/e2e/aks/aks_e2e_test.go` with build tag `//go:build aks_e2e`
- `test/e2e/aks/fixtures/` (sample task payload, expected artifact assertions)
- `aks.env.example` at repo root (mirrors `smoke.env.example`)
- `Makefile` target `test-aks-e2e`

### Test flow

1. Read `aks.env` for `NATS_URL` (port-forward target or LB), `KUBE_CONTEXT`, `NAMESPACE`, `TASK_TIMEOUT`, `KEEP_CLUSTER`
2. Verify cluster reachability (`kubectl get nodes`)
3. Verify Helm release Ready
4. Verify NATS streams and consumers exist; create durable consumer if missing (matches Phase 4.4 pattern)
5. Publish an A2A `SendMessageRequest` with a deterministic prompt ("create file `hello.txt` containing `hello phase5`")
6. Subscribe to `agent.status.*` and `agent.results.*`; record state transitions
7. Assert: result received within `TASK_TIMEOUT`, status sequence valid, artifact present and correct
8. Log: end-to-end latency, per-stage latency, NATS message counts
9. Cleanup: delete the test task's session resources; if `KEEP_CLUSTER` unset and the test owns the cluster, optionally call `make destroy-aks-test`

### What it asserts

- AKS API server reachable
- Helm release `daedalus` in `STATUS: deployed`
- NATS JetStream streams `AGENT_TASKS` and `AGENT_STATUS` exist
- Task transitions through `submitted` to `working` to `completed`
- Final artifact matches expected content
- End-to-end RTT logged (regression baseline established but not gated)

### Invocation

```sh
cp aks.env.example aks.env
# edit values
make test-aks-e2e
```

The Make target runs `go test -tags aks_e2e ./test/e2e/aks/... -v -timeout 30m`.

### Cleanup behavior

- Success: cluster left up if `KEEP_CLUSTER=1`, otherwise destroyed
- Failure: cluster always left up; the TTL cleanup will reap it later. Test logs include the RG name and `expires-at` so the engineer can extend or destroy manually.

### Dependencies

- 5.3 (deployment automation must produce a known-good cluster state)

---

## 5.5 TTL Cleanup

### Deliverables

- `scripts/aks-cleanup.sh`
- `.github/workflows/nightly-cleanup.yml` (misnomer; runs every 30 minutes)

### Tag schema

| Tag | Value | Purpose |
|-----|-------|---------|
| `auto-destroy` | `true` | Opt-in marker; absent or `false` means "do not touch" |
| `expires-at` | ISO 8601 UTC timestamp | When the RG becomes eligible for destruction |
| `owner` | GH username or workflow name | Forensics |
| `phase` | `5` | Source attribution |
| `managed-by` | `terraform` or `manual` | Hint for cleanup behavior |

### `scripts/aks-cleanup.sh` algorithm

```
1. az account set --subscription $SUB
2. az group list --query "[?tags.\"auto-destroy\"=='true']" -o json
3. For each RG:
   a. parse tags."expires-at" as ISO 8601
   b. if parse fails: log and skip (do not destroy on parse error)
   c. if expires-at <= now (UTC):
      - if DRY_RUN=1: print "would destroy <name>"
      - else: az group delete -n <name> --yes --no-wait
4. Exit 0 even if no RGs matched; non-zero only on auth or transport errors
```

Flags: `--dry-run`, `--subscription`, `--prefix` (limit to RGs whose name starts with a given prefix for safety).

### GHA workflow

`.github/workflows/nightly-cleanup.yml`:

- Trigger: `schedule: '*/30 * * * *'` plus `workflow_dispatch`
- Auth: `azure/login@v2` with the cleanup workload identity
- Steps: checkout, run `scripts/aks-cleanup.sh --prefix rg-daedalus-`
- Failure handling: on non-zero exit, post a workflow notice (no Slack/email integration in Phase 5)

### Validation / Acceptance

- Dry-run on an RG with `expires-at` in the past prints destroy intent and changes nothing
- Real run on the same RG removes it
- Run with no matching RGs exits 0 and is a no-op
- Workflow runs on schedule and is observable in the Actions tab

### Dependencies

- 5.1 (RG must be tagged correctly by Terraform)

---

## 5.6 Documentation and Runbook

### Deliverables

- Updated `docs/aks-deployment.md` with the IaC path replacing manual `az` commands
- New section in `docs/runbook.md`: "Provisioning a Pre-Prod Cluster"
- `deploy/terraform/README.md` (referenced from the runbook)
- A short troubleshooting appendix covering the common failures (state lock, AcrPull missing, KV access denied, OIDC issuer not enabled)

### Acceptance

- A new engineer with subscription access can follow the runbook end-to-end without asking questions in chat
- Manual instructions from Phase 4 are preserved but clearly marked superseded
- All documented commands are copy-pasteable and idempotent

### Dependencies

- 5.1 through 5.5 stable enough that the docs don't lie

---

## Cross-cutting Concerns

### Secrets

`GITHUB_TOKEN` lives only in Key Vault. The pod pulls it via the Workload Identity SDK or via the Secrets Store CSI driver mounting it as a file. It is never in Helm values, never in env vars set by Terraform, and never in CI logs.

### State management

Terraform remote state is in `stdaedalustfstate<suffix>` -> container `tfstate` -> key `daedalus/test.tfstate`. State locking via the storage account's lease mechanism. RBAC restricted to the workflow identity and named human operators.

### Cost guardrails

- TTL is the primary defense
- Cleanup workflow runs every 30 minutes
- An optional Azure budget alert at the subscription level is documented in 5.6 but not provisioned by Terraform in Phase 5

### Out of scope

- Multi-region failover
- HA control plane (single AKS cluster, single nodepool)
- Private cluster, VPN, hub-and-spoke
- Per-PR ephemeral environments
- NATS persistence across cluster recreation
- Production hardening (PSA, NetworkPolicy beyond defaults)
- New observability stack work
