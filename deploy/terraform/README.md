# Phase 5 Terraform: AKS + ACR + Key Vault + Workload Identity

> Most engineers should not run `terraform` directly - use `make
> deploy-aks-test`. This README is for the cases where you do.

Provisions the Phase 5 test infrastructure in a single subscription, single
region, with a 4-hour TTL enforced by tags + the Phase 5.5 cleanup workflow.

## Cross-references

- [`docs/runbook.md`](../../docs/runbook.md) - the happy path. Start here.
- [`docs/aks-deployment.md`](../../docs/aks-deployment.md) - architecture
  reference and the workload-side troubleshooting appendix (AcrPull,
  workload identity, GHA OIDC subject mismatch, KEDA, secrets).

The troubleshooting items in this README are scoped to `terraform`-shaped
problems (state lock, federated credential listing, cleanup role
verification, TTL drift). Workload-side failures (image pulls, pod crash
loops, KEDA triggers) live in `docs/aks-deployment.md`.

## Layout

```
deploy/terraform/
  versions.tf      # provider/version pins
  backend.tf       # commented-out azurerm backend template
  providers.tf     # azurerm + data.azurerm_client_config.current
  variables.tf     # inputs with validation
  locals.tf        # naming, tag composition (incl. expires-at)
  main.tf          # module composition
  outputs.tf       # kubeconfig, ACR login server, KV uri, identity client_id, etc.
  modules/
    rg/            # resource group with TTL tags
    aks/           # AKS w/ OIDC issuer + workload identity enabled
    acr/           # Standard ACR + AcrPull role to AKS kubelet identity
    keyvault/      # RBAC-mode KV + Secrets User role to workload identity
    identity/      # user-assigned MI + federated credential for daedalus-proxy SA
    gha-identity/  # user-assigned MI + federated credentials for GitHub Actions OIDC (publishes to ACR)
  envs/
    test.tfvars.example   # committed sample
    test.tfvars           # gitignored, real values
  bootstrap/
    bootstrap.sh   # idempotent one-time setup of the remote-state storage
    README.md
```

## Prerequisites

- Terraform `>= 1.7.0`
- Azure CLI, logged in (`az login`)
- The target subscription is selectable: `az account set --subscription <your-subscription-id>`
- Permissions to create role assignments at the resource-group scope (the
  modules grant AcrPull, KV Secrets User, KV Secrets Officer)

## First-time setup

1. Bootstrap remote state:

   ```
   ./bootstrap/bootstrap.sh --subscription <your-subscription-id>
   ```

   Copy the printed `backend.tf` snippet into `backend.tf` (replacing the
   commented-out template).

2. Create your tfvars:

   ```
   cp envs/test.tfvars.example envs/test.tfvars
   $EDITOR envs/test.tfvars
   ```

3. Init + plan + apply:

   ```
   terraform init
   terraform plan  -var-file=envs/test.tfvars
   terraform apply -var-file=envs/test.tfvars
   ```

## Daily flow

```
terraform plan  -var-file=envs/test.tfvars
terraform apply -var-file=envs/test.tfvars
```

Re-running `apply` is the canonical way to extend the cluster TTL: every apply
recomputes `expires-at = now + ttl_hours`.

## Outputs

Useful queries:

```
# Drop kubeconfig for kubectl/helm
terraform output -raw kubeconfig > /tmp/kubeconfig
export KUBECONFIG=/tmp/kubeconfig

# ACR for image push
terraform output -raw acr_login_server

# Key Vault URI for CSI driver / az cli secret writes
terraform output -raw keyvault_uri

# Workload identity client_id - annotate the K8s SA with this
terraform output -raw workload_identity_client_id

# When the cluster expires
terraform output -raw expires_at
```

The Helm chart that runs the daedalus proxy must create a ServiceAccount in
namespace `daedalus` named `daedalus-proxy` with the annotation:

```
azure.workload.identity/client-id: <terraform output -raw workload_identity_client_id>
```

The federated credential is bound to that exact namespace + name.

## Wiring GitHub Actions to ACR (Phase 5.2)

The `gha-identity` module provisions a separate user-assigned managed
identity (`id-<prefix>-<env>-gha`) with federated credentials for GitHub
Actions OIDC tokens, and grants it `AcrPush` on the ACR (via the
`acr` module's `additional_push_principal_ids`). No client secrets are
ever created.

After `terraform apply`, set these as **GitHub Actions repository variables**
(Settings -> Secrets and variables -> Actions -> Variables):

| GitHub repo variable      | Source                                              |
| ------------------------- | --------------------------------------------------- |
| `AZURE_CLIENT_ID`         | `terraform output -raw gha_client_id`               |
| `AZURE_TENANT_ID`         | `terraform output -raw gha_tenant_id`               |
| `AZURE_SUBSCRIPTION_ID`   | `terraform output -raw subscription_id`             |
| `ACR_NAME`                | `terraform output -raw acr_name`                    |
| `ACR_LOGIN_SERVER`        | `terraform output -raw acr_login_server`            |

These are **variables, not secrets** - none of them are sensitive on their
own. The federated credential subjects are what gate access.

### Verifying the federated credential matches the workflow

The federated credential subjects must match the OIDC token's `sub` claim,
which GitHub Actions sets based on the trigger:

- `push`/`workflow_dispatch` on main: `repo:<owner>/<repo>:ref:refs/heads/main`
- `pull_request` (any branch): `repo:<owner>/<repo>:pull_request`
- A run inside a deployment environment named `test`: `repo:<owner>/<repo>:environment:test`

To list what was actually registered:

```
terraform output -json gha_oidc_subjects
```

If you want to add subjects (e.g. another branch, a tag, a different
environment), set `github_oidc_subjects` in your tfvars to a list that
includes everything you want - the variable replaces the default list, it
does not append.

If a workflow run fails with `AADSTS70021` ("No matching federated identity
record found"), the `sub` claim on the token did not match any subject on
the identity. Compare the workflow's actual `sub` (visible in the OIDC
token's JWT payload, or by inspecting the `azure/login` action's debug
output) against `terraform output gha_oidc_subjects`.

## Phase 5.5 cleanup

The Phase 5.5 cleanup workflow (`.github/workflows/nightly-cleanup.yml`)
runs every 30 minutes, lists resource groups tagged `auto-destroy=true`,
and deletes any whose `expires-at` tag is in the past. It authenticates
via the existing GHA managed identity (`module.gha_identity`).

To grant that identity permission to delete RGs, this stack assigns it
**subscription-scoped `Contributor`** by default (`var.enable_cleanup_role
= true`). Set `enable_cleanup_role = false` in tfvars to disable the role
assignment - the workflow will still authenticate but every `az group
delete` call will return `AuthorizationFailed`.

### Trade-off: broad role vs operational simplicity

`Contributor` is more authority than the cleanup workflow strictly needs.
The cleanup script only calls `az group list` and `az group delete`. A
custom role limited to
`Microsoft.Resources/subscriptions/resourceGroups/{read,delete}` would be
tighter, but it requires a `azurerm_role_definition` resource, lifecycle
management, and per-subscription propagation delays.

For Phase 5 - a dedicated test subscription with no production workload
sharing the blast radius - `Contributor` is acceptable. Phase 6 hardening
will replace this with a custom role.

### Verifying the role assignment

After `terraform apply`:

```
terraform output -raw cleanup_role_assignment_id
```

is a non-empty resource ID when `enable_cleanup_role = true`. Cross-check
with:

```
az role assignment show --ids "$(terraform output -raw cleanup_role_assignment_id)"
```

## TTL behavior

- Every resource in the workload RG inherits `auto-destroy=true` and
  `expires-at=<ISO8601 timestamp>` tags via `local.common_tags`.
- The Phase 5.5 cleanup workflow deletes any RG with `auto-destroy=true` whose
  `expires-at` is in the past.
- Re-running `terraform apply` resets `expires-at` to `now + ttl_hours`.

### Known limitation: `timestamp()` re-evaluates every plan

Terraform's `timestamp()` function runs at plan time on every invocation. That
means:

- Every `terraform plan` shows a tag drift (the `expires-at` value changes).
- Every `terraform apply` writes new tag values, even if nothing else changed.

For a low-frequency test cluster this is acceptable - applies are rare and the
"drift" is the desired behavior (sliding TTL window). If you find this
annoying, set `ttl_hours` lower so applies happen on purpose.

## Cost note

The default `Standard_D2s_v5` x 2 nodes runs roughly $0.10-0.15/hour for
compute, plus negligible AKS control plane (free tier), ACR Standard
($0.167/day), and Key Vault Standard (essentially free at our scale).

The TTL exists for a reason. **Destroy when not in use.**

## Destroy

```
terraform destroy -var-file=envs/test.tfvars
```

Or wait for the Phase 5.5 cleanup workflow to do it automatically when
`expires-at` is in the past.

## Things this module does NOT do

- Does not push container images (Phase 5.2)
- Does not deploy Helm releases (Phase 5.3)
- Does not write secrets to Key Vault (the workflow does that out-of-band so
  secret values never touch Terraform state)
- Does not create the Kubernetes ServiceAccount (the Helm chart does)
