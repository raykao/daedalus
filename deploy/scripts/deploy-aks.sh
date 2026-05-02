#!/usr/bin/env bash
# deploy-aks.sh - One-command, idempotent deployment of Daedalus to a test AKS cluster.
#
# This script is the workhorse behind `make deploy-aks-test`. It can be invoked
# directly, but the Make target is the supported entrypoint. The whole flow is
# safe to re-run: every step uses upsert semantics (terraform apply, helm
# upgrade --install, kubectl apply --dry-run=client | apply, etc.).
#
# Steps:
#   1. Prerequisites check (az, terraform, kubectl, helm, jq).
#   2. Azure auth (device-code login if needed).
#   3. Terraform init + apply.
#   4. Read TF outputs into shell variables.
#   5. Kubeconfig via az aks get-credentials.
#   6. Namespace upsert.
#   7. AcrPull binding sanity (warn-only).
#   8. KEDA install (helm).
#   9. Workload Identity wiring (annotate ServiceAccount, label namespace).
#  10. GITHUB_TOKEN secret upsert.
#  11. ACR pull secret strategy (rely on TF-provisioned kubelet AcrPull).
#  12. Daedalus helm upgrade --install with per-deployment ACR overrides.
#  13. Smoke poll (wait for KEDA, NATS, ScaledJob).
#  14. Print final summary.
#
# Usage:
#   ./deploy/scripts/deploy-aks.sh [--debug]
#
# Environment variables:
#   GITHUB_TOKEN     Required. Falls back to smoke.env if present and unset.
#   IMAGE_TAG        Optional. Image tag to deploy (default: "latest").
#   RELEASE_NAME     Optional. Helm release name (default: "daedalus").
#   NAMESPACE        Optional. Target namespace (default: "daedalus").
#   TFVARS_FILE      Optional. Tfvars path (default: deploy/terraform/envs/test.tfvars).
#   KEDA_VERSION     Optional. KEDA chart version (default pinned below).
#   SKIP_KEDA        Optional. Set to 1 to skip the KEDA install step.
#   SKIP_TERRAFORM   Optional. Set to 1 to skip terraform apply (uses existing state).
#
# This script intentionally does NOT bring up Key Vault CSI mounts. Phase 5.3
# only WIRES workload identity (annotation + namespace label). Consuming
# secrets from Key Vault via the CSI driver is deferred.

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults / configuration
# ---------------------------------------------------------------------------
RELEASE_NAME="${RELEASE_NAME:-daedalus}"
NAMESPACE="${NAMESPACE:-daedalus}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
TFVARS_FILE="${TFVARS_FILE:-deploy/terraform/envs/test.tfvars}"

# KEDA 2.14.0 - first stable release that supports the eventing.keda.sh/v1
# CloudEventSource API the project plans to use in Phase 5.5+. 2.14.x has
# been GA since April 2024 and is well-tested with AKS 1.28-1.30.
KEDA_VERSION="${KEDA_VERSION:-2.14.0}"

SKIP_KEDA="${SKIP_KEDA:-0}"
SKIP_TERRAFORM="${SKIP_TERRAFORM:-0}"

# Resolve repo root - this script lives at deploy/scripts/deploy-aks.sh and
# must run from the repo root so all relative paths (deploy/terraform,
# deploy/helm/daedalus, etc.) resolve correctly.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
DEBUG=0
for arg in "$@"; do
    case "${arg}" in
        --debug) DEBUG=1 ;;
        --help|-h)
            sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "Unknown argument: ${arg}" >&2; exit 2 ;;
    esac
done
if [[ "${DEBUG}" -eq 1 ]]; then
    set -x
fi

# ---------------------------------------------------------------------------
# Color output helpers (matches test/scripts/validate-aks-deployment.sh style)
# ---------------------------------------------------------------------------
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

CURRENT_STEP=""
on_err() {
    local rc=$?
    error "Step failed: ${CURRENT_STEP:-<unknown>} (exit ${rc})"
    error "Re-run with --debug for verbose tracing, or fix the underlying issue and re-invoke 'make deploy-aks-test'."
    exit "${rc}"
}
trap on_err ERR

step() { CURRENT_STEP="$1"; header "$1"; }

# ---------------------------------------------------------------------------
# Step 1: Prerequisites
# ---------------------------------------------------------------------------
step "Step 1: Checking prerequisites"

REQUIRED_TOOLS=(az terraform kubectl helm jq)
MISSING=()
for tool in "${REQUIRED_TOOLS[@]}"; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        MISSING+=("${tool}")
    fi
done
if [[ "${#MISSING[@]}" -gt 0 ]]; then
    error "Missing required tools: ${MISSING[*]}"
    error "Install instructions:"
    for t in "${MISSING[@]}"; do
        case "${t}" in
            az)        error "  az:        https://learn.microsoft.com/cli/azure/install-azure-cli" ;;
            terraform) error "  terraform: https://developer.hashicorp.com/terraform/downloads" ;;
            kubectl)   error "  kubectl:   https://kubernetes.io/docs/tasks/tools/" ;;
            helm)      error "  helm:      https://helm.sh/docs/intro/install/" ;;
            jq)        error "  jq:        https://jqlang.github.io/jq/download/" ;;
        esac
    done
    exit 1
fi

info "az        : $(az version --output tsv --query '\"azure-cli\"' 2>/dev/null || az --version | head -1)"
info "terraform : $(terraform version | head -1)"
info "kubectl   : $(kubectl version --client -o json 2>/dev/null | jq -r '.clientVersion.gitVersion' 2>/dev/null || kubectl version --client --short 2>/dev/null)"
info "helm      : $(helm version --short)"
info "jq        : $(jq --version)"
pass "All required tools present"

# ---------------------------------------------------------------------------
# Step 2: Azure auth + subscription
# ---------------------------------------------------------------------------
step "Step 2: Azure authentication"

if [[ ! -f "${TFVARS_FILE}" ]]; then
    error "Tfvars file not found: ${TFVARS_FILE}"
    error "Copy deploy/terraform/envs/test.tfvars.example to ${TFVARS_FILE} and fill in your subscription_id."
    exit 1
fi

# Extract subscription_id from HCL tfvars without leaking it anywhere.
# Matches: subscription_id = "..."  (whitespace tolerant, ignores comments).
SUBSCRIPTION_ID="$(grep -E '^[[:space:]]*subscription_id[[:space:]]*=' "${TFVARS_FILE}" \
    | head -1 \
    | sed -E 's/^[[:space:]]*subscription_id[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/')"
if [[ -z "${SUBSCRIPTION_ID}" || "${SUBSCRIPTION_ID}" == "00000000-0000-0000-0000-000000000000" ]]; then
    error "subscription_id not set (or still placeholder) in ${TFVARS_FILE}"
    exit 1
fi
# Reject anything that isn't a UUID. Without this, an unquoted or otherwise
# unparseable HCL value falls through and lands in 'az account set' as garbage.
if ! [[ "${SUBSCRIPTION_ID}" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]; then
    error "subscription_id in ${TFVARS_FILE} could not be parsed. Expected format: subscription_id = \"<uuid>\""
    exit 1
fi

if az account show >/dev/null 2>&1; then
    info "Already signed in to Azure"
else
    warn "Not signed in to Azure - launching device-code login"
    az login --use-device-code >/dev/null
fi

az account set --subscription "${SUBSCRIPTION_ID}"
info "Subscription set: $(az account show --query name -o tsv) ($(az account show --query id -o tsv))"
pass "Azure authentication ready"

# ---------------------------------------------------------------------------
# Step 3: Terraform init + apply
# ---------------------------------------------------------------------------
step "Step 3: Terraform init + apply"

if [[ "${SKIP_TERRAFORM}" -eq 1 ]]; then
    warn "SKIP_TERRAFORM=1 - assuming existing terraform state is current"
else
    info "terraform init"
    terraform -chdir=deploy/terraform init -input=false
    info "terraform apply (this can take 8-12 minutes on a cold deploy)"
    terraform -chdir=deploy/terraform apply -var-file="envs/$(basename "${TFVARS_FILE}")" -auto-approve -input=false
fi
pass "Terraform state converged"

# ---------------------------------------------------------------------------
# Step 4: Read TF outputs (avoid printing sensitive values)
# ---------------------------------------------------------------------------
step "Step 4: Reading Terraform outputs"

TF_OUTPUTS_JSON="$(terraform -chdir=deploy/terraform output -json)"
get_tf() { jq -r --arg k "$1" '.[$k].value // empty' <<<"${TF_OUTPUTS_JSON}"; }

AKS_NAME="$(get_tf aks_name)"
RG_NAME="$(get_tf resource_group_name)"
ACR_LOGIN_SERVER="$(get_tf acr_login_server)"
ACR_NAME="$(get_tf acr_name)"
KV_NAME="$(get_tf keyvault_name)"
# KV_URI captured for the summary banner; not yet wired into Helm values
# because Phase 5.3 only stages workload-identity, not Key Vault CSI mounts.
# shellcheck disable=SC2034
KV_URI="$(get_tf keyvault_uri)"
WI_CLIENT_ID="$(get_tf workload_identity_client_id)"
OIDC_ISSUER="$(get_tf oidc_issuer_url)"
EXPIRES_AT="$(get_tf expires_at || true)"

for var in AKS_NAME RG_NAME ACR_LOGIN_SERVER ACR_NAME WI_CLIENT_ID; do
    if [[ -z "${!var}" ]]; then
        error "Required terraform output is empty: ${var}"
        exit 1
    fi
done

info "AKS cluster      : ${AKS_NAME}"
info "Resource group   : ${RG_NAME}"
info "ACR              : ${ACR_NAME} (${ACR_LOGIN_SERVER})"
info "Key Vault        : ${KV_NAME:-<none>}"
info "Workload identity: ${WI_CLIENT_ID}"
info "OIDC issuer      : ${OIDC_ISSUER}"
[[ -n "${EXPIRES_AT}" ]] && info "Cluster expires  : ${EXPIRES_AT}"
pass "Terraform outputs captured"

# ---------------------------------------------------------------------------
# Step 5: Kubeconfig
# ---------------------------------------------------------------------------
step "Step 5: Fetching kubeconfig"

# --admin=false uses the AAD-integrated kubeconfig (Entra auth via az login).
# --overwrite-existing ensures re-runs don't fail with "context already exists".
az aks get-credentials \
    --resource-group "${RG_NAME}" \
    --name "${AKS_NAME}" \
    --overwrite-existing \
    --admin=false \
    >/dev/null

info "Verifying kubectl can reach the cluster..."
DEADLINE=$(( $(date +%s) + 120 ))
while (( $(date +%s) < DEADLINE )); do
    if kubectl get nodes >/dev/null 2>&1; then
        break
    fi
    sleep 5
done
kubectl get nodes >/dev/null

# Wait for at least one Ready node
DEADLINE=$(( $(date +%s) + 300 ))
READY=0
while (( $(date +%s) < DEADLINE )); do
    READY="$(kubectl get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l)"
    if (( READY > 0 )); then
        break
    fi
    info "Waiting for nodes to become Ready..."
    sleep 10
done
if (( READY == 0 )); then
    error "No Ready nodes after 5 minutes"
    kubectl get nodes -o wide || true
    exit 1
fi
info "$(kubectl get nodes --no-headers | wc -l) node(s), ${READY} Ready"
pass "Kubeconfig active"

# ---------------------------------------------------------------------------
# Step 6: Namespace
# ---------------------------------------------------------------------------
step "Step 6: Ensuring namespace ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
pass "Namespace ready"

# ---------------------------------------------------------------------------
# Step 7: AcrPull binding sanity (warn-only)
# ---------------------------------------------------------------------------
step "Step 7: Verifying AKS kubelet identity has AcrPull on ACR"

KUBELET_OBJ_ID="$(az aks show -g "${RG_NAME}" -n "${AKS_NAME}" \
    --query "identityProfile.kubeletidentity.objectId" -o tsv 2>/dev/null || true)"
ACR_ID="$(az acr show -n "${ACR_NAME}" --query id -o tsv 2>/dev/null || true)"

if [[ -n "${KUBELET_OBJ_ID}" && -n "${ACR_ID}" ]]; then
    HAS_ACR_PULL="$(az role assignment list \
        --assignee "${KUBELET_OBJ_ID}" \
        --scope "${ACR_ID}" \
        --query "[?roleDefinitionName=='AcrPull'] | length(@)" \
        -o tsv 2>/dev/null || echo 0)"
    if [[ "${HAS_ACR_PULL}" -ge 1 ]]; then
        pass "Kubelet identity has AcrPull on ACR"
    else
        warn "Kubelet identity DOES NOT have AcrPull on ACR (Terraform should provision this)."
        warn "If image pulls fail, run: az aks update -g ${RG_NAME} -n ${AKS_NAME} --attach-acr ${ACR_NAME}"
    fi
else
    warn "Could not resolve kubelet identity / ACR ID - skipping role check"
fi

# ---------------------------------------------------------------------------
# Step 8: KEDA install
# ---------------------------------------------------------------------------
step "Step 8: Installing KEDA ${KEDA_VERSION}"

if [[ "${SKIP_KEDA}" -eq 1 ]]; then
    warn "SKIP_KEDA=1 - skipping KEDA install"
else
    if ! helm repo list -o json 2>/dev/null | jq -e '.[] | select(.name=="kedacore")' >/dev/null; then
        helm repo add kedacore https://kedacore.github.io/charts >/dev/null
    fi
    helm repo update kedacore >/dev/null
    helm upgrade --install keda kedacore/keda \
        --namespace keda \
        --create-namespace \
        --version "${KEDA_VERSION}" \
        --wait --timeout 5m
    pass "KEDA ${KEDA_VERSION} installed"
fi

# ---------------------------------------------------------------------------
# Step 9: Workload Identity wiring
# ---------------------------------------------------------------------------
step "Step 9: Wiring Azure Workload Identity"

# Phase 5.3 only WIRES the identity. Actually mounting Key Vault secrets via
# the CSI driver is out of scope and deferred to a later phase. The label
# below is harmless if the CSI driver is not enabled.
kubectl label namespace "${NAMESPACE}" azure.workload.identity/use=true --overwrite >/dev/null
info "Labeled namespace ${NAMESPACE} with azure.workload.identity/use=true"

# The Helm chart creates ServiceAccounts named "${RELEASE_NAME}-<worker>".
# We annotate them post-helm-install (Step 12). Record the desired annotation
# here so Step 12 can reference WI_CLIENT_ID via the env it already has.
info "Workload identity client ID will be applied to worker ServiceAccounts in Step 12"
pass "Workload identity prerequisites set"

# ---------------------------------------------------------------------------
# Step 10: GITHUB_TOKEN secret
# ---------------------------------------------------------------------------
step "Step 10: Ensuring copilot-secret (GITHUB_TOKEN)"

# Mirrors the smoke.env fallback used by `make test-smoke`.
if [[ -z "${GITHUB_TOKEN:-}" ]] && [[ -f smoke.env ]]; then
    info "GITHUB_TOKEN not set - sourcing smoke.env"
    set -a
    # shellcheck disable=SC1091
    . ./smoke.env
    set +a
fi
if [[ -z "${GITHUB_TOKEN:-}" ]]; then
    error "GITHUB_TOKEN is not set."
    error "Either export GITHUB_TOKEN, or copy smoke.env.example to smoke.env and fill in your token."
    exit 1
fi

printf '%s' "${GITHUB_TOKEN}" \
  | kubectl create secret generic copilot-secret \
      --namespace "${NAMESPACE}" \
      --from-file=github-token=/dev/stdin \
      --dry-run=client -o yaml \
  | kubectl apply -f - >/dev/null
pass "copilot-secret upserted"

# ---------------------------------------------------------------------------
# Step 11: ACR pull secret strategy
# ---------------------------------------------------------------------------
step "Step 11: ACR pull strategy"

# Preferred path: rely on the Terraform-provisioned kubelet identity AcrPull
# role assignment. No imagePullSecret is needed when AKS has direct AcrPull
# rights (the kubelet pulls images using its own managed identity).
#
# values-aks-test.yaml leaves imagePullSecrets empty for this reason. If the
# Step 7 sanity check warned that AcrPull is missing, surface it again here
# so the engineer can decide whether to fail or continue.
if [[ "${HAS_ACR_PULL:-0}" -ge 1 ]]; then
    info "Using kubelet identity AcrPull (no docker-registry secret needed)"
else
    warn "AcrPull on kubelet identity not detected. If pods fail with ImagePullBackOff:"
    warn "  az aks update -g ${RG_NAME} -n ${AKS_NAME} --attach-acr ${ACR_NAME}"
    warn "Re-run this script after the role assignment propagates (~30s)."
fi

# ---------------------------------------------------------------------------
# Step 12: Helm install Daedalus
# ---------------------------------------------------------------------------
step "Step 12: Helm upgrade --install daedalus"

helm upgrade --install "${RELEASE_NAME}" deploy/helm/daedalus/ \
    -f deploy/helm/daedalus/values-aks-test.yaml \
    --namespace "${NAMESPACE}" \
    --create-namespace \
    --set "proxy.image.repository=${ACR_LOGIN_SERVER}/daedalus-proxy" \
    --set "proxy.image.tag=${IMAGE_TAG}" \
    --set "workers[0].image.repository=${ACR_LOGIN_SERVER}/copilot-bridge" \
    --set "workers[0].image.tag=${IMAGE_TAG}" \
    --wait --timeout 10m

# Annotate worker ServiceAccounts for workload identity. The Helm chart does
# not currently template SA annotations from values, so we patch them here.
# This is idempotent (annotate --overwrite).
SAS=$(kubectl get serviceaccounts -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${RELEASE_NAME}" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
if [[ -n "${SAS}" ]]; then
    while IFS= read -r sa; do
        [[ -z "${sa}" ]] && continue
        kubectl annotate serviceaccount "${sa}" \
            -n "${NAMESPACE}" \
            "azure.workload.identity/client-id=${WI_CLIENT_ID}" \
            --overwrite >/dev/null
        info "Annotated ServiceAccount ${sa}"
    done <<<"${SAS}"
fi
pass "Helm release ${RELEASE_NAME} converged"

# ---------------------------------------------------------------------------
# Step 13: Smoke poll
# ---------------------------------------------------------------------------
step "Step 13: Verifying core components"

# KEDA operator
if [[ "${SKIP_KEDA}" -ne 1 ]]; then
    if ! kubectl wait --for=condition=available deployment/keda-operator \
        -n keda --timeout=300s >/dev/null 2>&1; then
        error "KEDA operator did not become available"
        kubectl describe deployment/keda-operator -n keda || true
        exit 1
    fi
    pass "KEDA operator Ready"
fi

# NATS StatefulSet (created by subchart)
NATS_STS="$(kubectl get statefulset -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${RELEASE_NAME}" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
if [[ -n "${NATS_STS}" ]]; then
    if ! kubectl rollout status "statefulset/${NATS_STS}" -n "${NAMESPACE}" --timeout=300s; then
        error "NATS StatefulSet ${NATS_STS} did not roll out"
        kubectl describe "statefulset/${NATS_STS}" -n "${NAMESPACE}" || true
        exit 1
    fi
    pass "NATS StatefulSet ${NATS_STS} Ready"
else
    warn "No NATS StatefulSet found (nats.enabled=false?)"
fi

# ScaledJob registered
if kubectl api-resources 2>/dev/null | grep -q '^scaledjobs'; then
    SCALEDJOBS="$(kubectl get scaledjobs -n "${NAMESPACE}" --no-headers 2>/dev/null | wc -l)"
    if (( SCALEDJOBS > 0 )); then
        pass "${SCALEDJOBS} ScaledJob(s) registered"
    else
        warn "No ScaledJobs found in namespace ${NAMESPACE}"
    fi
fi

# ---------------------------------------------------------------------------
# Step 14: Summary
# ---------------------------------------------------------------------------
step "Step 14: Summary"

echo ""
echo "  Cluster        : ${AKS_NAME}"
echo "  Resource group : ${RG_NAME}"
echo "  ACR            : ${ACR_LOGIN_SERVER}"
echo "  Namespace      : ${NAMESPACE}"
echo "  Release        : ${RELEASE_NAME}"
echo "  Image tag      : ${IMAGE_TAG}"
[[ -n "${EXPIRES_AT}" ]] && echo "  Expires at     : ${EXPIRES_AT}"
echo ""
echo "  Helm release status:"
helm status "${RELEASE_NAME}" --namespace "${NAMESPACE}" | sed 's/^/    /'
echo ""
echo "Next steps:"
echo "  - Reload kubeconfig anytime with: make aks-credentials"
echo "  - Inspect status with:            make aks-status"
echo "  - Run smoke validation with:      ./test/scripts/validate-aks-deployment.sh"
echo "  - Tear it all down with:          make destroy-aks-test"
echo ""
pass "Daedalus deployed to AKS"
