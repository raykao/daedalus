#!/usr/bin/env bash
# Bootstrap the Terraform remote state backend.
#
# Idempotent: re-running is safe. Creates (if missing):
#   - resource group rg-daedalus-tfstate (no TTL; this is permanent)
#   - storage account stdaedalustfstate<6-char-random> with versioning + soft-delete
#   - container `tfstate`
#
# Then prints the backend.tf snippet to copy into deploy/terraform/backend.tf.

set -euo pipefail

log()  { printf '[bootstrap] %s\n' "$*"; }
fail() { printf '[bootstrap] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $(basename "$0") --subscription <SUBSCRIPTION_ID> [--region <REGION>]

Creates the Azure resources needed for Terraform remote state and prints the
backend.tf snippet to enable.

Options:
  --subscription <id>   Azure subscription ID (UUID). Required.
  --region <region>     Azure region for the state account. Default: eastus.
  --rg <name>           Resource group name. Default: rg-daedalus-tfstate.
  --prefix <prefix>     Storage account name prefix. Default: stdaedalustfstate.
  -h, --help            Show this help.
EOF
}

SUBSCRIPTION=""
REGION="eastus"
RG="rg-daedalus-tfstate"
PREFIX="stdaedalustfstate"
CONTAINER="tfstate"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --subscription) SUBSCRIPTION="${2:?}"; shift 2 ;;
    --region)       REGION="${2:?}"; shift 2 ;;
    --rg)           RG="${2:?}"; shift 2 ;;
    --prefix)       PREFIX="${2:?}"; shift 2 ;;
    -h|--help)      usage; exit 0 ;;
    *) fail "unknown arg: $1 (try --help)" ;;
  esac
done

[[ -n "$SUBSCRIPTION" ]] || { usage; fail "--subscription is required"; }

command -v az >/dev/null 2>&1 || fail "az CLI not found in PATH"

if ! az account show >/dev/null 2>&1; then
  fail "az is not logged in; run 'az login' first"
fi

log "setting active subscription to $SUBSCRIPTION"
az account set --subscription "$SUBSCRIPTION"

log "ensuring resource group $RG exists in $REGION"
if ! az group show --name "$RG" >/dev/null 2>&1; then
  az group create --name "$RG" --location "$REGION" --tags managed_by=bootstrap purpose=tfstate >/dev/null
  log "created RG $RG"
else
  log "RG $RG already exists"
fi

# Locate or create the storage account. Pattern: <prefix><6-char-random>.
log "looking for an existing state storage account in $RG"
EXISTING_SA="$(az storage account list \
  --resource-group "$RG" \
  --query "[?starts_with(name, '$PREFIX')] | [0].name" \
  -o tsv 2>/dev/null || true)"

if [[ -n "$EXISTING_SA" && "$EXISTING_SA" != "None" ]]; then
  SA_NAME="$EXISTING_SA"
  log "reusing storage account $SA_NAME"
else
  RAND="$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 6 || true)"
  SA_NAME="${PREFIX}${RAND}"
  # Storage account names must be 3-24 alphanumeric.
  if [[ ${#SA_NAME} -gt 24 ]]; then
    SA_NAME="${SA_NAME:0:24}"
  fi
  log "creating storage account $SA_NAME"
  az storage account create \
    --name "$SA_NAME" \
    --resource-group "$RG" \
    --location "$REGION" \
    --sku Standard_LRS \
    --kind StorageV2 \
    --min-tls-version TLS1_2 \
    --allow-blob-public-access false \
    --tags managed_by=bootstrap purpose=tfstate >/dev/null

  # Enable blob versioning and soft-delete for accidental-overwrite protection.
  az storage account blob-service-properties update \
    --account-name "$SA_NAME" \
    --resource-group "$RG" \
    --enable-versioning true \
    --enable-delete-retention true \
    --delete-retention-days 14 >/dev/null
  log "configured versioning + soft-delete on $SA_NAME"
fi

log "fetching storage account key for container creation"
SA_KEY=$(az storage account keys list \
    --account-name "$SA_NAME" \
    --resource-group "$RG" \
    --query '[0].value' -o tsv)

log "ensuring container $CONTAINER exists (using account key for one-time bootstrap)"
az storage container create \
  --name "$CONTAINER" \
  --account-name "$SA_NAME" \
  --account-key "$SA_KEY" >/dev/null
log "container $CONTAINER ready"

log "granting current user Storage Blob Data Contributor on the storage account (for backend access)"
CURRENT_USER_OID=$(az ad signed-in-user show --query id -o tsv)
SA_ID=$(az storage account show -g "$RG" -n "$SA_NAME" --query id -o tsv)
RBAC_ERR="$(mktemp)"
trap 'rm -f "$RBAC_ERR"' EXIT
if ! az role assignment create \
    --assignee-object-id "$CURRENT_USER_OID" \
    --assignee-principal-type User \
    --role "Storage Blob Data Contributor" \
    --scope "$SA_ID" >/dev/null 2>"$RBAC_ERR"; then
  if grep -qiE "RoleAssignmentExists|already exists" "$RBAC_ERR"; then
    log "Storage Blob Data Contributor role already assigned to current user (continuing)"
  else
    log "ERROR: role assignment failed:"
    cat "$RBAC_ERR" >&2
    fail "Failed to grant Storage Blob Data Contributor. Verify you have Microsoft.Authorization/roleAssignments/write on the storage account scope."
  fi
fi
log "RBAC for daily terraform use is in place"
log "RBAC propagation can take up to 5 minutes; if 'terraform init' fails with auth errors, wait and retry."

cat <<EOF

[bootstrap] Done.

Add the following to deploy/terraform/backend.tf (uncommented), then run:
  cd deploy/terraform
  terraform init -migrate-state

----- begin backend.tf -----
terraform {
  backend "azurerm" {
    resource_group_name  = "$RG"
    storage_account_name = "$SA_NAME"
    container_name       = "$CONTAINER"
    key                  = "phase5.terraform.tfstate"
    use_azuread_auth     = true
  }
}
-----  end backend.tf  -----
EOF
