#!/usr/bin/env bash
# destroy-aks.sh - Tear down everything `deploy-aks.sh` created.
#
# This is the mirror of deploy-aks.sh and the workhorse behind
# `make destroy-aks-test`. Steps:
#
#   1. helm uninstall daedalus (best-effort, --ignore-not-found via fallback).
#   2. kubectl delete namespace daedalus.
#   3. terraform destroy.
#   4. Verify the resource group is gone.
#
# All steps are idempotent (safe to re-run after partial failure).
#
# Usage:
#   ./deploy/scripts/destroy-aks.sh [--debug]
#
# Environment variables:
#   KEEP_CLUSTER   Set to 1 to refuse destruction (escape hatch for shared
#                  clusters or accidental invocation).
#   RELEASE_NAME   Helm release name (default: daedalus).
#   NAMESPACE      Namespace (default: daedalus).
#   TFVARS_FILE    Tfvars path (default: deploy/terraform/envs/test.tfvars).
#   SKIP_TERRAFORM Set to 1 to skip the terraform destroy step.

set -euo pipefail

RELEASE_NAME="${RELEASE_NAME:-daedalus}"
NAMESPACE="${NAMESPACE:-daedalus}"
TFVARS_FILE="${TFVARS_FILE:-deploy/terraform/envs/test.tfvars}"
SKIP_TERRAFORM="${SKIP_TERRAFORM:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

DEBUG=0
for arg in "$@"; do
    case "${arg}" in
        --debug) DEBUG=1 ;;
        --help|-h)
            sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "Unknown argument: ${arg}" >&2; exit 2 ;;
    esac
done
[[ "${DEBUG}" -eq 1 ]] && set -x

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()   { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()   { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error()  { echo -e "${RED}[ERROR]${NC} $*" >&2; }
pass()   { echo -e "${GREEN}[PASS]${NC}  $*"; }
header() { echo -e "${BLUE}=== $* ===${NC}"; }

if [[ "${KEEP_CLUSTER:-0}" -eq 1 ]]; then
    error "KEEP_CLUSTER=1 is set - refusing to destroy."
    error "Unset KEEP_CLUSTER (or pass KEEP_CLUSTER=0) to proceed."
    exit 1
fi

# ---------------------------------------------------------------------------
# Step 1: Helm uninstall
# ---------------------------------------------------------------------------
header "Step 1: helm uninstall ${RELEASE_NAME}"
if command -v helm >/dev/null 2>&1 && command -v kubectl >/dev/null 2>&1 \
    && kubectl get namespace "${NAMESPACE}" >/dev/null 2>&1; then
    if helm status "${RELEASE_NAME}" --namespace "${NAMESPACE}" >/dev/null 2>&1; then
        helm uninstall "${RELEASE_NAME}" --namespace "${NAMESPACE}" --ignore-not-found || true
        pass "Helm release uninstalled"
    else
        info "Helm release ${RELEASE_NAME} not found - skipping"
    fi
    # helm uninstall does not reap StatefulSet PVCs (volumeClaimTemplates).
    # Bitnami nats with persistence creates them; if 'kubectl delete namespace'
    # below times out, the PVCs would be left behind and terraform destroy
    # would orphan their managed disks in the MC_* RG. Reap proactively.
    info "Reaping PVCs in ${NAMESPACE} (preventing orphaned managed disks)..."
    kubectl delete pvc --all --namespace "${NAMESPACE}" \
        --ignore-not-found --wait=true --timeout=120s \
        >/dev/null 2>&1 || warn "PVC reap timed out or unreachable - check MC_* RG for leaks"
else
    warn "kubectl context not pointing at a live cluster (or helm/kubectl missing) - skipping helm uninstall"
fi

# ---------------------------------------------------------------------------
# Step 2: Delete namespace
# ---------------------------------------------------------------------------
header "Step 2: kubectl delete namespace ${NAMESPACE}"
if command -v kubectl >/dev/null 2>&1 && kubectl get namespace "${NAMESPACE}" >/dev/null 2>&1; then
    kubectl delete namespace "${NAMESPACE}" --ignore-not-found --timeout=120s || \
        warn "Namespace deletion timed out or failed - terraform destroy will reclaim it with the cluster"
    pass "Namespace deletion requested"
else
    info "Namespace ${NAMESPACE} not present - skipping"
fi

# ---------------------------------------------------------------------------
# Step 3: terraform destroy
# ---------------------------------------------------------------------------
header "Step 3: terraform destroy"
if [[ "${SKIP_TERRAFORM}" -eq 1 ]]; then
    warn "SKIP_TERRAFORM=1 - leaving Azure resources in place"
else
    if [[ ! -f "${TFVARS_FILE}" ]]; then
        error "Tfvars file not found: ${TFVARS_FILE}"
        error "Cannot terraform destroy without the same vars used to apply."
        exit 1
    fi
    terraform -chdir=deploy/terraform init -input=false >/dev/null
    terraform -chdir=deploy/terraform destroy \
        -var-file="envs/$(basename "${TFVARS_FILE}")" \
        -auto-approve -input=false
    pass "Terraform state destroyed"
fi

# ---------------------------------------------------------------------------
# Step 4: Verify resource group is gone
# ---------------------------------------------------------------------------
header "Step 4: Verifying resource group cleanup"
if [[ "${SKIP_TERRAFORM}" -ne 1 ]] && command -v az >/dev/null 2>&1; then
    # Try to recover the RG name from the (now-empty) terraform state. If
    # state is gone we can only trust that destroy succeeded - that's fine.
    RG_NAME="$(terraform -chdir=deploy/terraform output -raw resource_group_name 2>/dev/null || true)"
    if [[ -n "${RG_NAME}" ]]; then
        if [[ "$(az group exists -n "${RG_NAME}" 2>/dev/null || echo false)" == "true" ]]; then
            warn "Resource group ${RG_NAME} still exists - check for orphaned resources"
        else
            pass "Resource group ${RG_NAME} deleted"
        fi
    else
        info "No RG output remaining (terraform state cleaned). Assuming destroy succeeded."
    fi
fi

echo ""
info "Destroy complete. Reminder: Azure may take a few minutes to fully reclaim"
info "the resource group; cost meters stop when the RG is fully deleted."
