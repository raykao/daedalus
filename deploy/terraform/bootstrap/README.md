# Bootstrap

Terraform stores state. We want that state in Azure Storage, not on a laptop.
That creates a chicken-and-egg problem: Terraform manages Azure, but it needs
an Azure storage account to exist *before* it can run.

`bootstrap.sh` resolves it. It creates (idempotently):

- a permanent resource group `rg-daedalus-tfstate` (no TTL tags - this one stays)
- a storage account `stdaedalustfstate<random>` with versioning + 14-day soft-delete
- a container `tfstate`

Then it prints the exact `backend.tf` snippet to paste into
`deploy/terraform/backend.tf`.

## Usage

```
./bootstrap/bootstrap.sh --subscription <your-subscription-id>
```

Run once per subscription. Re-running is safe: it reuses the existing storage
account if it finds one matching the prefix.

After running, copy the printed snippet into `backend.tf` (replacing the
commented-out template) and run:

```
cd deploy/terraform
terraform init -migrate-state
```

## Why account-key auth for the container, then RBAC for daily use?

`az storage container create --auth-mode login` requires the caller to hold
`Storage Blob Data Contributor` (or higher) at the storage account scope.
Subscription Contributor does not grant blob-plane data access, and role
propagation is not instant. To keep the one-time bootstrap deterministic,
the script reads the storage account key (the user always has Owner-equivalent
access on a SA they just created) and uses key auth for the single
`container create` call.

For the daily flow, Terraform uses `use_azuread_auth = true`. That requires
the current user (or service principal) to hold `Storage Blob Data Contributor`
on the storage account. The bootstrap script grants that role to the signed-in
user automatically as the last step. RBAC propagation can take a few minutes,
so if the very first `terraform init` errors with a 403, wait and retry.

To grant the role manually for another principal:

```
SA_ID=$(az storage account show -g rg-daedalus-tfstate -n <sa-name> --query id -o tsv)
az role assignment create \
    --assignee <upn-or-object-id> \
    --role "Storage Blob Data Contributor" \
    --scope "$SA_ID"
```

## Why is the state RG separate from the workload RG?

The workload RG (`rg-daedalus-test`) is tagged `auto-destroy=true` and is
reaped by the Phase 5.5 cleanup workflow. The state RG must survive that
reaping; otherwise we'd lose state every TTL window. They are intentionally
distinct resource groups with distinct lifecycles.
