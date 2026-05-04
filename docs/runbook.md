# Daedalus Deployment Runbook

> The single source of truth for "how do I stand up Daedalus on AKS" via the
> Phase 5 IaC path. For the deeper reference and troubleshooting, see
> [AKS Deployment Guide](aks-deployment.md).

---

## 1. Goal and prerequisites

Stand up a working Daedalus deployment on a fresh AKS test cluster in roughly
**25 minutes cold** using one Make target. Everything is idempotent: re-running
the target reconciles the cluster state and slides the TTL forward.

**Prereq checklist:**

- [ ] `az` CLI 2.50+ (`az --version`)
- [ ] `kubectl` 1.28+ (`kubectl version --client`)
- [ ] `helm` 3.12+ (`helm version`)
- [ ] `terraform` 1.9+ (`terraform --version`)
- [ ] `docker` (only needed if you fall back to local image builds)
- [ ] `jq` (`jq --version`)
- [ ] A GitHub token with Copilot access exported as `GITHUB_TOKEN`
- [ ] Azure subscription where you have **Contributor** at the subscription
      scope (the stack creates resource groups, role assignments, and a
      federated identity)
- [ ] Cloned repo at the project root

---

## 2. Provisioning a Pre-Prod Cluster

The happy path. About 25 minutes from a cold start: ~1 min Terraform plan,
~10 min AKS provisioning, ~3 min KEDA + helm install, ~10 min image pull and
NATS rollout.

### 2.1 One-time bootstrap (idempotent)

The Terraform stack uses an Azure Storage backend for remote state. Bootstrap
it once per subscription:

```bash
cd deploy/terraform/
./bootstrap/bootstrap.sh --subscription <SUBSCRIPTION_ID>
```

Paste the printed `backend.tf` snippet into `deploy/terraform/backend.tf`,
replacing the commented-out template. This step is fully idempotent: re-running
against the same subscription is a no-op.

### 2.2 Configure your tfvars

```bash
cp envs/test.tfvars.example envs/test.tfvars
$EDITOR envs/test.tfvars
```

At minimum, set `subscription_id`. Optional knobs:

| Variable              | Default               | Notes                                                       |
| --------------------- | --------------------- | ----------------------------------------------------------- |
| `ttl_hours`           | `4`                   | Cluster auto-destroys at `now + ttl_hours`.                 |
| `node_count`          | `2`                   | AKS system pool size.                                       |
| `node_vm_size`        | `Standard_D2s_v5`     | Validated against a D-series allowlist.                     |
| `github_owner`        | `raykao`              | Owner of the repo whose Actions can mint OIDC tokens.       |
| `github_repo`         | `daedalus`            | Repo name. Federated subjects require this to match.        |
| `enable_cleanup_role` | `true`                | Grants the GHA UAMI subscription-scoped Contributor for the cleanup workflow. Set false to disable. |

### 2.3 Export your GitHub token

```bash
export GITHUB_TOKEN="ghp_..."
```

The deploy script writes this into the K8s `copilot-secret` so worker pods can
authenticate to Copilot. If `GITHUB_TOKEN` is unset, the script falls back to
`smoke.env` at the repo root (gitignored).

### 2.4 Deploy

From the repo root:

```bash
make deploy-aks-test
```

This is the one command. It is **fully idempotent**: safe to re-run after a
partial failure or to slide the TTL window. Each `terraform apply` recomputes
`expires-at = now + ttl_hours` and re-tags the resource group, so re-running
the target also extends the cluster's lifetime.

What it does (high level):

1. Verifies prerequisites and Azure auth.
2. `terraform init` + `terraform apply` for the AKS / ACR / Key Vault / identity stack.
3. Pulls kubeconfig via `az aks get-credentials`.
4. Installs KEDA 2.14.0 into the `keda` namespace.
5. Wires Azure Workload Identity (namespace label, SA annotation).
6. Upserts `copilot-secret` from `$GITHUB_TOKEN`.
7. Runs `helm upgrade --install` with image overrides pointing at the
   per-deployment ACR.
8. Waits for KEDA, NATS, and the `daedalus-copilot` ScaledJob to be ready.

For the full step-by-step expansion, see
[`docs/aks-deployment.md` - What `make deploy-aks-test` does](aks-deployment.md#what-make-deploy-aks-test-does).

### 2.5 Sanity-check the cluster

```bash
make aks-status
```

Prints Terraform outputs, current kube context, nodes, helm release status,
KEDA operator state, ScaledJobs, Jobs, and Pods. If the kube context does not
match the cluster recorded in Terraform state, the target warns and points you
at `make aks-credentials`.

---

## 3. Building and publishing images

The supported path is the GitHub Actions workflow `build-and-publish.yml`,
which uses OIDC to publish identical multi-arch digests to **both** GHCR and
ACR with no static secrets.

### 3.1 Trigger the workflow

```bash
gh workflow run build-and-publish.yml
```

Or use the Actions UI: **Actions -> Build and Publish -> Run workflow**.

The workflow:

- Builds `daedalus-proxy`, `mock-acp`, and `echo-a2a` for `linux/amd64` and `linux/arm64`.
- Publishes to `ghcr.io/<owner>/<image>:<tag>` (always).
- Mirrors identical digests to `<acr_login_server>/<image>:<tag>` via OIDC
  (federated to the GHA managed identity provisioned by Terraform).
- Runs Trivy scans (warns on HIGH, blocks CRITICAL) against both platforms.
- Pushes build-provenance attestations alongside each image.

### 3.2 Verify both registries got matching digests

```bash
ACR_LOGIN_SERVER=$(terraform -chdir=deploy/terraform output -raw acr_login_server)

GHCR_DIGEST=$(docker buildx imagetools inspect ghcr.io/raykao/daedalus-proxy:latest --format '{{.Manifest.Digest}}')
ACR_DIGEST=$(docker buildx imagetools inspect "${ACR_LOGIN_SERVER}/daedalus-proxy:latest" --format '{{.Manifest.Digest}}')

[ "$GHCR_DIGEST" = "$ACR_DIGEST" ] && echo "OK: digests match ($GHCR_DIGEST)" || echo "MISMATCH"
```

### 3.3 Local fallback (debug only)

If you need to iterate locally without going through CI, build and push by
hand. This is a debug path, not a supported workflow:

```bash
ACR_LOGIN_SERVER=$(terraform -chdir=deploy/terraform output -raw acr_login_server)
ACR_NAME=$(terraform -chdir=deploy/terraform output -raw acr_name)

az acr login --name "${ACR_NAME}"
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -f deploy/docker/Dockerfile.proxy \
    -t "${ACR_LOGIN_SERVER}/daedalus-proxy:latest" \
    --push .
```

After pushing, re-run `make deploy-aks-test` (helm picks up `Always` pull
policy, but you may need to delete pods to force re-pull on existing nodes).

---

## 4. Running the E2E harness

The Phase 5.4 harness drives a real task end-to-end against the live cluster.

### 4.1 Configure

```bash
cp aks.env.example aks.env
$EDITOR aks.env
```

Set at minimum:

- `KUBE_CONTEXT` - the kubectl context that points at the AKS cluster.
- `NATS_URL` - typically `nats://localhost:4222` after a port-forward (below).

### 4.2 Port-forward NATS (local runs)

```bash
kubectl port-forward -n daedalus svc/daedalus-nats 4222:4222 &
```

### 4.3 Run

```bash
make test-aks-e2e
```

The test is gated by the `aks_e2e` build tag, so it is skipped by default in
`go test ./...`. Set `KEEP_CLUSTER=1` in the environment (or in `aks.env`) to
log the resource group and `expires-at` on test exit; the test never destroys
the cluster itself.

---

## 5. TTL contract

The cluster has an `expires-at` tag computed at every `terraform apply` as
`now + ttl_hours` (default 4 hours). The Phase 5.5 cleanup workflow runs every
30 minutes and deletes any resource group with `auto-destroy=true` whose
`expires-at` is in the past.

| Operation                  | Command                              |
| -------------------------- | ------------------------------------ |
| Slide the TTL window       | `make deploy-aks-test` (idempotent)  |
| Manually extend (TF only)  | `terraform -chdir=deploy/terraform apply -var-file=envs/test.tfvars` |
| Manual destroy now         | `make destroy-aks-test`              |
| Wait for auto-destroy      | (no-op; cleanup workflow runs ~every 30 min) |

`terraform output -raw expires_at` prints the current expiry. `make aks-status`
also surfaces it via the Terraform outputs block.

---

## 6. Secret rotation

### 6.1 GITHUB_TOKEN

The K8s `copilot-secret` is created by `deploy/scripts/deploy-aks.sh` from the
`$GITHUB_TOKEN` environment variable.

To rotate:

```bash
export GITHUB_TOKEN="<new token>"
make deploy-aks-test
```

The deploy script upserts the secret (`kubectl apply --dry-run=client | apply`)
and the helm upgrade rolls the workload as needed.

### 6.2 ACR pull credentials

There are no ACR pull credentials to rotate. The AKS kubelet managed identity
holds `AcrPull` on the ACR (granted by the Terraform `acr` module). The kubelet
authenticates to ACR with its own identity, so no `acr-pull-secret` exists.

### 6.3 Key Vault-backed secrets

Out of scope for Phase 5. The workload identity is wired (the `daedalus`
namespace is labeled `azure.workload.identity/use=true` and the workload SA is
annotated with the UAMI client ID), but the CSI driver and Key Vault secret
mounts are deferred. Today, all runtime secrets live in K8s.

---

## 7. Cleanup workflow

`.github/workflows/nightly-cleanup.yml` runs every 30 minutes via cron and
also exposes `workflow_dispatch` with two inputs:

- `dry_run` - when `true`, prints the deletion plan without acting.
- `prefix` - resource group name prefix to scope deletions. Required.
  An empty prefix is a hard error (exit 2). To explicitly opt into reaping
  every tagged RG in the subscription, the script accepts `--all-prefixes`,
  but the workflow input does not expose it.

### 7.1 Dispatch a dry run

From the CLI:

```bash
gh workflow run nightly-cleanup.yml -f dry_run=true -f prefix=rg-daedalus-
```

Or in the Actions UI: **Actions -> Nightly Cleanup -> Run workflow**, set
`dry_run=true`, leave `prefix=rg-daedalus-`.

### 7.2 Local invocation

The same script powers `make cleanup-aks-test`:

```bash
make cleanup-aks-test                  # real run
make cleanup-aks-test DRY_RUN=1        # plan only
```

### 7.3 Required role

The workflow auths via the GHA managed identity. Subscription-scoped
`Contributor` is granted by Terraform when `enable_cleanup_role = true`
(default). With it disabled, the workflow authenticates fine but every
`az group delete` returns `AuthorizationFailed`.

---

## 8. Teardown

Two paths:

### 8.1 Immediate destruction

```bash
make destroy-aks-test
```

Mirror of `deploy-aks-test`: helm uninstall, namespace delete, `terraform
destroy`, RG-gone verification. Idempotent.

### 8.2 Wait for the TTL cleanup workflow

Do nothing. The next time `expires-at` is in the past and the cleanup workflow
runs (cron every 30 min), the RG is deleted. Use this when you simply walk away
from a test cluster.

### 8.3 Safety escape hatch

`KEEP_CLUSTER=1 make destroy-aks-test` is a no-op. The destroy script refuses
when this variable is set; useful as a guard in CI debugging.

---

## 9. Quick reference

### Make targets

| Target                  | Purpose                                                           |
| ----------------------- | ----------------------------------------------------------------- |
| `make deploy-aks-test`  | One-command idempotent deploy (~25 min cold). Slides TTL forward. |
| `make destroy-aks-test` | One-command teardown. Respects `KEEP_CLUSTER=1`.                  |
| `make aks-credentials`  | Refresh kubeconfig from current Terraform state.                  |
| `make aks-status`       | Health snapshot: TF outputs, kube context, KEDA, helm, jobs.      |
| `make aks-logs`         | Tail proxy + agent logs for the most recent pod (`WORKER=copilot` by default). |
| `make test-aks-e2e`     | Run the live AKS end-to-end harness (build tag `aks_e2e`).        |
| `make cleanup-aks-test` | Manually invoke `scripts/aks-cleanup.sh` (set `DRY_RUN=1` to plan). |

### Useful Terraform outputs

| Output                          | What it is                                              |
| ------------------------------- | ------------------------------------------------------- |
| `aks_name`                      | AKS cluster name (also the kube context name).          |
| `resource_group_name`           | Workload RG (the unit of TTL).                          |
| `acr_login_server`              | ACR hostname for image pushes.                          |
| `keyvault_uri`                  | KV URI (wired but not yet consumed).                    |
| `workload_identity_client_id`   | UAMI client ID; annotates the workload SA.              |
| `oidc_issuer_url`               | AKS OIDC issuer.                                        |
| `expires_at`                    | RFC 3339 timestamp the cleanup workflow checks against. |
| `gha_oidc_subjects`             | Federated subjects accepted on the GHA UAMI.            |
| `cleanup_role_assignment_id`    | Role-assignment ID for the GHA UAMI's Contributor.      |

### Key scripts

| Script                                       | Purpose                                       |
| -------------------------------------------- | --------------------------------------------- |
| `deploy/scripts/deploy-aks.sh`               | Source of truth for `make deploy-aks-test`.   |
| `deploy/scripts/destroy-aks.sh`              | Source of truth for `make destroy-aks-test`.  |
| `deploy/terraform/bootstrap/bootstrap.sh`    | One-time remote-state bootstrap.              |
| `scripts/aks-cleanup.sh`                     | TTL-driven RG reaper; runs every 30 min in CI. |
| `test/scripts/validate-aks-deployment.sh`    | 10-step KEDA / cold-start / SIGTERM validator.|

---

> Deeper architecture, configuration reference, and troubleshooting:
> [AKS Deployment Guide](aks-deployment.md).

---

## Alertmanager receiver override

The chart ships with `alerting.alertmanagerConfig.receiver.type: "null"` so a
fresh deploy provisions the seven structural alerts without paging anyone.
Pick one of the override blocks below and add it to your `values.yaml` (or
`-f` overlay) to switch the default receiver.

Per-alert routing rules (per-component, severity-based escalation, etc.) are
out of scope for this release - sub-task 6.4 will add them once the per-alert
runbook entries land.

### Mattermost

```yaml
alerting:
  alertmanagerConfig:
    receiver:
      type: mattermost
      config:
        webhook: https://mattermost.example.com/hooks/REPLACE_ME
```

The webhook URL is the incoming-webhook URL of the channel that should
receive alerts. Create it via Mattermost → Integrations → Incoming Webhooks.

### GitHub

```yaml
alerting:
  alertmanagerConfig:
    receiver:
      type: github
      config:
        webhook: https://api.github.com/repos/<org>/<repo>/dispatches
```

This drives `repository_dispatch` events. A workflow listening on
`on: repository_dispatch: { types: [alertmanager] }` (or whatever
`event_type` your Alertmanager template sends) opens the issue or runs the
mitigation playbook. Pair with a fine-grained PAT or GitHub App token mounted
as a secret if your Alertmanager uses HTTP basic auth.

### PagerDuty

```yaml
alerting:
  alertmanagerConfig:
    receiver:
      type: pagerduty
      config:
        routingKeySecret:
          name: pagerduty-routing-key
          key: routing-key
```

The secret must already exist in the namespace where Alertmanager runs and
contain the Events API v2 routing key. Create it once with:

```bash
kubectl -n monitoring create secret generic pagerduty-routing-key \
  --from-literal=routing-key=<your-pagerduty-integration-key>
```
