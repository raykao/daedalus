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

## Why is the state RG separate from the workload RG?

The workload RG (`rg-daedalus-test`) is tagged `auto-destroy=true` and is
reaped by the Phase 5.5 cleanup workflow. The state RG must survive that
reaping; otherwise we'd lose state every TTL window. They are intentionally
distinct resource groups with distinct lifecycles.
