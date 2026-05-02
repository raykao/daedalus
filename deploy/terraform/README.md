# Phase 5 Terraform: AKS + ACR + Key Vault + Workload Identity

Provisions the Phase 5 test infrastructure in a single subscription, single
region, with a 4-hour TTL enforced by tags + the Phase 5.5 cleanup workflow.

## Layout

```
deploy/terraform/
  versions.tf      # provider/version pins
  backend.tf       # commented-out azurerm backend template
  providers.tf     # azurerm + azuread + data.azurerm_client_config.current
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
- The target subscription (`00000000-0000-0000-0000-000000000000` for the
  shared test sub) is selectable: `az account set --subscription <id>`
- Permissions to create role assignments at the resource-group scope (the
  modules grant AcrPull, KV Secrets User, KV Secrets Officer)

## First-time setup

1. Bootstrap remote state:

   ```
   ./bootstrap/bootstrap.sh --subscription 00000000-0000-0000-0000-000000000000
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
