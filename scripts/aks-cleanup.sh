#!/usr/bin/env bash
# aks-cleanup.sh - TTL-based destruction of tagged Azure resource groups.
#
# Lists resource groups tagged `auto-destroy=true` and destroys those whose
# `expires-at` (RFC 3339/ISO 8601 UTC) is in the past. This is the workhorse
# behind `.github/workflows/nightly-cleanup.yml` (daily at 09:17 UTC) and the
# `make cleanup-aks-test` Make target.
#
# The expected tag schema (enforced by deploy/terraform/locals.tf for any RG
# created by Phase 5 Terraform):
#
#   auto-destroy=true            opt-in marker. Absent or any other value
#                                means "do not touch".
#   expires-at=<RFC 3339 UTC>    destruction eligibility cutoff, e.g.
#                                "2026-05-01T18:42:00Z".
#
# RGs that are tagged `auto-destroy=true` but have a missing or unparseable
# `expires-at` are skipped with a warning - never destroyed on parse error.
#
# `expires-at` MUST be RFC 3339 UTC of the exact form YYYY-MM-DDTHH:MM:SSZ
# (matching Terraform's `timestamp()` output). Anything else - including
# values GNU `date -d` would happily parse like "yesterday", "now",
# "1 hour ago", or a bare epoch "0" - is treated as unparseable and the RG
# is skipped. This regex gate runs BEFORE `date -u -d` so permissive
# natural-language parsing cannot trigger a destruction.
#
# Behaviour:
#   1. (optional) az account set --subscription <id>
#   2. az group list --tag auto-destroy=true (server-side filter)
#   3. For each candidate RG:
#        - filter by --prefix (or --all-prefixes)
#        - parse expires-at
#        - if expired and not --dry-run: az group delete --no-wait
#        - if expired and --dry-run: log [DRY-RUN] would destroy
#        - if not expired or unparseable: log and continue
#   4. Print a summary line.
#
# Exit codes:
#   0   success (zero matches is success).
#   1   one or more individual `az group delete` calls returned non-zero.
#   2   missing required CLI tools, or invalid/missing arguments.
#   3   az auth/transport failure (e.g. `az group list` itself failed).
#
# Usage:
#   ./scripts/aks-cleanup.sh --prefix rg-daedalus-
#   ./scripts/aks-cleanup.sh --prefix rg-daedalus- --dry-run
#   ./scripts/aks-cleanup.sh --all-prefixes --dry-run
#   ./scripts/aks-cleanup.sh --subscription <id> --prefix rg-daedalus-
#
# Flags:
#   --prefix <p>      Only consider RGs whose name starts with <p>. Required
#                     unless --all-prefixes is given. Recommended:
#                     "rg-daedalus-".
#   --all-prefixes    Override the prefix safety net (operate on every
#                     matching RG). Prints a prominent warning.
#   --dry-run         Print intent only; do not call `az group delete`.
#                     Equivalent to DRY_RUN=1.
#   --subscription <id>  Override `az account` context. Equivalent to
#                        SUBSCRIPTION=<id>.
#   --debug           set -x mode.
#   --help, -h        Print this header and exit.
#
# Environment variables:
#   DRY_RUN=1         Same as --dry-run.
#   SUBSCRIPTION=<id> Same as --subscription <id>.
#
# Operator notes:
#   - This script is INDEPENDENT of `KEEP_CLUSTER` (used by destroy-aks.sh).
#     If a debugging engineer wants to keep an RG past its TTL, they must
#     update the tag manually:
#       az tag update --resource-id <rg-id> --operation merge \
#           --tags expires-at=2099-01-01T00:00:00Z
#   - `az group delete --no-wait` returns immediately. Re-running this
#     script while a deletion is still in-flight is harmless (Azure
#     de-duplicates) - we log such cases as
#     "[INFO] <name>: deletion already in progress" but still issue the
#     delete (which is a no-op).
#   - Required tools: az, jq, GNU date. Missing tools fail fast with exit 2.

set -euo pipefail

# ---------------------------------------------------------------------------
# Flag / env parsing
# ---------------------------------------------------------------------------
DRY_RUN="${DRY_RUN:-0}"
SUBSCRIPTION="${SUBSCRIPTION:-}"
PREFIX=""
ALL_PREFIXES=0
DEBUG=0

print_help() {
    sed -n '2,70p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)        DRY_RUN=1; shift ;;
        --all-prefixes)   ALL_PREFIXES=1; shift ;;
        --debug)          DEBUG=1; shift ;;
        --help|-h)        print_help; exit 0 ;;
        --prefix)
            [[ $# -ge 2 ]] || { echo "ERROR: --prefix requires a value" >&2; exit 2; }
            PREFIX="$2"; shift 2 ;;
        --prefix=*)       PREFIX="${1#*=}"; shift ;;
        --subscription)
            [[ $# -ge 2 ]] || { echo "ERROR: --subscription requires a value" >&2; exit 2; }
            SUBSCRIPTION="$2"; shift 2 ;;
        --subscription=*) SUBSCRIPTION="${1#*=}"; shift ;;
        *) echo "ERROR: unknown argument: $1" >&2; echo "Run with --help for usage." >&2; exit 2 ;;
    esac
done

[[ "${DEBUG}" -eq 1 ]] && set -x

# ---------------------------------------------------------------------------
# Colour helpers (mirroring deploy/scripts/destroy-aks.sh)
# ---------------------------------------------------------------------------
if [[ -t 1 ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; NC=''
fi

info()   { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()   { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error()  { echo -e "${RED}[ERROR]${NC} $*" >&2; }
pass()   { echo -e "${GREEN}[PASS]${NC}  $*"; }
header() { echo -e "${BLUE}=== $* ===${NC}"; }

# ---------------------------------------------------------------------------
# Tool / argument validation
# ---------------------------------------------------------------------------
require_tool() {
    if ! command -v "$1" >/dev/null 2>&1; then
        error "required tool '$1' not found in PATH"
        exit 2
    fi
}
require_tool az
require_tool jq
require_tool date

# Confirm GNU date (BSD date does not understand `-d`).
if ! date -u -d "1970-01-01T00:00:00Z" +%s >/dev/null 2>&1; then
    error "GNU date is required (BSD date does not support -d <iso8601>)"
    error "On macOS: 'brew install coreutils' and use 'gdate' (or rerun in Linux/CI)."
    exit 2
fi

if [[ -z "${PREFIX}" && "${ALL_PREFIXES}" -ne 1 ]]; then
    error "--prefix is required (or pass --all-prefixes to operate on every"
    error "RG with auto-destroy=true regardless of name). This safety net"
    error "exists to prevent accidentally reaping unrelated tagged RGs."
    error ""
    error "Recommended for Phase 5: --prefix rg-daedalus-"
    exit 2
fi

if [[ "${ALL_PREFIXES}" -eq 1 ]]; then
    warn "--all-prefixes is set: every RG with auto-destroy=true and an"
    warn "expired expires-at tag will be deleted, regardless of name."
fi

# ---------------------------------------------------------------------------
# Decision logic (factored out for testability)
# ---------------------------------------------------------------------------
# decide_rg_action <name> <expires_at_value> <now_epoch>
# Echoes one of: DESTROY <delta>, NOT_EXPIRED <delta>, SKIP_MISSING, SKIP_UNPARSEABLE
# Where <delta> is seconds (positive = how long ago expired, negative = until expiry).
decide_rg_action() {
    local name="$1"
    local expires_at="$2"
    local now_epoch="$3"

    if [[ -z "${expires_at}" || "${expires_at}" == "null" ]]; then
        echo "SKIP_MISSING"
        return 0
    fi

    # Strict RFC 3339 UTC format gate. GNU `date -d` accepts permissive
    # natural-language inputs ("yesterday", "now", "1 hour ago", "0") which
    # would silently make an RG eligible for destruction. Reject anything
    # that does not match Terraform's `timestamp()` output exactly.
    if ! [[ "${expires_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then
        echo "SKIP_UNPARSEABLE"
        return 0
    fi

    local exp_epoch
    if ! exp_epoch=$(date -u -d "${expires_at}" +%s 2>/dev/null); then
        echo "SKIP_UNPARSEABLE"
        return 0
    fi

    local delta=$(( now_epoch - exp_epoch ))
    if (( delta >= 0 )); then
        echo "DESTROY ${delta}"
    else
        echo "NOT_EXPIRED ${delta}"
    fi
}

# ---------------------------------------------------------------------------
# Subscription context
# ---------------------------------------------------------------------------
header "aks-cleanup.sh starting"
info "dry-run=${DRY_RUN}  prefix='${PREFIX}'  all-prefixes=${ALL_PREFIXES}"

if [[ -n "${SUBSCRIPTION}" ]]; then
    info "Setting az subscription context: ${SUBSCRIPTION}"
    if ! az account set --subscription "${SUBSCRIPTION}" >/dev/null 2>&1; then
        error "az account set --subscription '${SUBSCRIPTION}' failed."
        error "Check 'az login' and that the subscription is accessible."
        exit 3
    fi
fi

# ---------------------------------------------------------------------------
# List candidate RGs
# ---------------------------------------------------------------------------
header "Listing RGs with tag auto-destroy=true"
RG_LIST_JSON=""
if ! RG_LIST_JSON=$(az group list --tag auto-destroy=true --output json 2>/dev/null); then
    # Fall back to client-side filtering if --tag is unsupported by the
    # provider/CLI version. Both filters should yield identical results.
    warn "az group list --tag auto-destroy=true failed; falling back to client-side filter"
    if ! RG_LIST_JSON=$(az group list --output json 2>/dev/null); then
        error "az group list failed - check az auth and network connectivity."
        exit 3
    fi
    RG_LIST_JSON=$(echo "${RG_LIST_JSON}" | jq '[.[] | select(.tags["auto-destroy"] == "true")]')
fi

# Validate RG_LIST_JSON is a JSON array before the loop. A malformed value here
# (non-JSON banner from older az CLI, MSAL consent text, etc.) would silently
# yield zero loop iterations because process substitution exit codes are
# invisible to set -e in the parent shell. Fail loudly instead.
if ! echo "${RG_LIST_JSON}" | jq -e 'type == "array"' >/dev/null 2>&1; then
    error "az group list returned non-array JSON or invalid output."
    error "First 200 chars of raw value: ${RG_LIST_JSON:0:200}"
    exit 3
fi

NOW_EPOCH=$(date -u +%s)
SCANNED=0
EXPIRED=0
DESTROYED=0
HAD_FAILURES=0

# jq emits one JSON object per line for streaming.
while IFS= read -r rg_json; do
    [[ -z "${rg_json}" ]] && continue
    SCANNED=$((SCANNED + 1))

    NAME=$(echo "${rg_json}" | jq -r '.name')
    EXPIRES_AT=$(echo "${rg_json}" | jq -r '.tags["expires-at"] // empty')
    PROV_STATE=$(echo "${rg_json}" | jq -r '.properties.provisioningState // .provisioningState // ""')

    # Prefix filter
    if [[ "${ALL_PREFIXES}" -ne 1 ]]; then
        if [[ "${NAME}" != "${PREFIX}"* ]]; then
            info "${NAME}: skipping (does not match prefix '${PREFIX}')"
            continue
        fi
    fi

    # Already deleting? Log informatively but continue - delete is idempotent.
    ALREADY_DELETING=0
    if [[ "${PROV_STATE}" == "Deleting" ]]; then
        ALREADY_DELETING=1
        info "${NAME}: deletion already in progress"
    fi

    DECISION=$(decide_rg_action "${NAME}" "${EXPIRES_AT}" "${NOW_EPOCH}")
    ACTION=${DECISION%% *}
    DELTA=${DECISION#* }

    case "${ACTION}" in
        SKIP_MISSING)
            warn "skipping ${NAME}: missing expires-at tag"
            ;;
        SKIP_UNPARSEABLE)
            warn "skipping ${NAME}: unparseable expires-at='${EXPIRES_AT}'"
            ;;
        NOT_EXPIRED)
            # delta is negative seconds (now - exp); convert to "in Ns".
            local_until=$(( -DELTA ))
            info "${NAME}: not yet expired (expires-at=${EXPIRES_AT}, in ${local_until}s)"
            ;;
        DESTROY)
            EXPIRED=$((EXPIRED + 1))
            if [[ "${DRY_RUN}" -eq 1 ]]; then
                info "[DRY-RUN] would destroy ${NAME} (expired ${DELTA}s ago)"
                DESTROYED=$((DESTROYED + 1))
            else
                if [[ "${ALREADY_DELETING}" -eq 1 ]]; then
                    info "${NAME}: re-issuing delete (already in Deleting state, harmless)"
                fi
                info "Destroying ${NAME} (expired ${DELTA}s ago)..."
                # Capture stderr so AuthorizationFailed and similar messages
                # surface in the warning instead of being swallowed.
                DELETE_ERR=$(az group delete --name "${NAME}" --yes --no-wait 2>&1 >/dev/null) && DELETE_RC=0 || DELETE_RC=$?
                if [[ "${DELETE_RC}" -eq 0 ]]; then
                    pass "${NAME}: delete requested (--no-wait)"
                    DESTROYED=$((DESTROYED + 1))
                else
                    warn "${NAME}: az group delete returned non-zero - continuing (stderr: ${DELETE_ERR})"
                    HAD_FAILURES=1
                fi
            fi
            ;;
        *)
            warn "${NAME}: internal error - unknown decision '${DECISION}'"
            HAD_FAILURES=1
            ;;
    esac
done < <(echo "${RG_LIST_JSON}" | jq -c '.[]')

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
if [[ "${DRY_RUN}" -eq 1 ]]; then
    info "Scanned ${SCANNED} RGs, ${EXPIRED} expired, ${DESTROYED} would-destroy (dry-run)"
else
    info "Scanned ${SCANNED} RGs, ${EXPIRED} expired, ${DESTROYED} destroyed"
fi

if [[ "${HAD_FAILURES}" -eq 1 ]]; then
    error "One or more individual deletes failed - exiting 1."
    exit 1
fi

exit 0
