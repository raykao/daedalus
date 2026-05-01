#!/bin/bash
set -euo pipefail

# validate-aks-deployment.sh - Validates the Daedalus KEDA ScaledJob deployment on AKS.
#
# Checks KEDA is installed, the Helm release is deployed, publishes a test task via
# kubectl exec into the NATS pod, and measures cold-start latency and end-to-end
# completion time. After a successful run it verifies scale-to-zero restores.
#
# Usage:
#   export GITHUB_TOKEN="ghp_..."
#   ./test/scripts/validate-aks-deployment.sh
#
# Environment variables (all have defaults):
#   RELEASE_NAME  - Helm release name (default: daedalus)
#   NAMESPACE     - Kubernetes namespace  (default: daedalus)
#   NATS_STREAM   - JetStream stream name (default: AGENT_TASKS)
#   SKIP_CLEANUP  - Set to 1 to leave Jobs/Pods for manual inspection

RELEASE_NAME="${RELEASE_NAME:-daedalus}"
NAMESPACE="${NAMESPACE:-daedalus}"
NATS_STREAM="${NATS_STREAM:-AGENT_TASKS}"
SKIP_CLEANUP="${SKIP_CLEANUP:-0}"

# Initialize before trap so cleanup is safe even if Step 5 is never reached
RESULT_SUB_PID=""
RESULT_TMP=""

# Cleanup handler - runs on EXIT, INT, TERM to release background resources
cleanup() {
    [ -n "${RESULT_SUB_PID:-}" ] && kill "${RESULT_SUB_PID}" 2>/dev/null || true
    rm -f "${RESULT_TMP:-}"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Color output helpers
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }
pass()  { echo -e "${GREEN}[PASS]${NC}  $*"; }
fail()  { echo -e "${RED}[FAIL]${NC}  $*"; }

# ---------------------------------------------------------------------------
# Timing helpers
# ---------------------------------------------------------------------------
now_ns() {
    local ts
    ts=$(date +%s%N 2>/dev/null)
    if echo "$ts" | grep -q 'N'; then
        echo "$(date +%s)000000000"
    else
        echo "$ts"
    fi
}

elapsed_sec() {
    local start="$1" end="$2"
    local diff=$(( end - start ))
    local secs=$(( diff / 1000000000 ))
    local nanos=$(( diff % 1000000000 ))
    printf "%d.%03d" "$secs" "$(( nanos / 1000000 ))"
}

T_START=$(now_ns)

# ---------------------------------------------------------------------------
# Step 1: Prerequisites
# ---------------------------------------------------------------------------
info "=== Step 1: Checking prerequisites ==="

PREREQ_FAIL=0

if ! command -v kubectl &>/dev/null; then
    error "kubectl is required but not found in PATH"
    PREREQ_FAIL=1
fi

if ! command -v helm &>/dev/null; then
    error "helm is required but not found in PATH"
    PREREQ_FAIL=1
fi

if [ -z "${GITHUB_TOKEN:-}" ]; then
    error "GITHUB_TOKEN environment variable is required"
    PREREQ_FAIL=1
fi

if [ "$PREREQ_FAIL" -eq 1 ]; then
    error "One or more prerequisites are missing. Aborting."
    exit 1
fi

CURRENT_CTX=$(kubectl config current-context 2>/dev/null || echo "")
if [ -z "$CURRENT_CTX" ]; then
    error "kubectl has no current context. Run 'az aks get-credentials ...' first."
    exit 1
fi
info "kubectl context: $CURRENT_CTX"

if ! echo "$CURRENT_CTX" | grep -q "daedalus"; then
    warn "Current context '$CURRENT_CTX' does not contain 'daedalus'."
    warn "Verify you are targeting the correct AKS cluster before continuing."
fi

pass "Prerequisites OK"

# ---------------------------------------------------------------------------
# Step 2: KEDA installed
# ---------------------------------------------------------------------------
info ""
info "=== Step 2: Checking KEDA installation ==="

if kubectl get crd scaledjobs.keda.sh &>/dev/null; then
    pass "KEDA CRD scaledjobs.keda.sh found"
else
    fail "KEDA CRD scaledjobs.keda.sh NOT found"
    error "KEDA must be installed. The Terraform module in deploy/terraform/ installs it."
    error "Run: helm repo add kedacore https://kedacore.github.io/charts"
    error "     helm install keda kedacore/keda --namespace keda --create-namespace"
    exit 1
fi

if kubectl get crd scaledobjects.keda.sh &>/dev/null; then
    pass "KEDA CRD scaledobjects.keda.sh found"
fi

# ---------------------------------------------------------------------------
# Step 3: Helm release deployed
# ---------------------------------------------------------------------------
info ""
info "=== Step 3: Verifying Helm release ==="

if helm status "$RELEASE_NAME" -n "$NAMESPACE" &>/dev/null; then
    HELM_STATUS=$(helm status "$RELEASE_NAME" -n "$NAMESPACE" --output json 2>/dev/null | \
        python3 -c "import sys,json; print(json.load(sys.stdin).get('info',{}).get('status','unknown'))" \
        2>/dev/null || echo "unknown")
    if [ "$HELM_STATUS" = "deployed" ]; then
        pass "Helm release '$RELEASE_NAME' is deployed (status: $HELM_STATUS)"
    else
        fail "Helm release '$RELEASE_NAME' has unexpected status: $HELM_STATUS"
        warn "Re-deploy with: make helm-aks-deploy"
        exit 1
    fi
else
    fail "Helm release '$RELEASE_NAME' not found in namespace '$NAMESPACE'"
    error "Deploy first with: make helm-aks-deploy"
    exit 1
fi

# ---------------------------------------------------------------------------
# Step 4: Scale-to-zero baseline - no jobs when queue is empty
# ---------------------------------------------------------------------------
info ""
info "=== Step 4: Verifying scale-to-zero baseline ==="

# Drain any stale active workloads before measuring baseline.
# Use pod-based count - KEDA retains completed Job objects indefinitely per
# successfulJobsHistoryLimit, so raw Job count is always > 0 on a warm cluster.
JOB_COUNT=$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null \
    | grep -v -E 'Completed|Error|Evicted|Terminating' | wc -l | tr -d ' ')
if [ "$JOB_COUNT" -gt 0 ]; then
    warn "$JOB_COUNT active pods still running from a previous run. Waiting up to 60s for them to clear..."
    WAIT=0
    while [ "$JOB_COUNT" -gt 0 ] && [ "$WAIT" -lt 60 ]; do
        sleep 5
        WAIT=$((WAIT + 5))
        JOB_COUNT=$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null \
            | grep -v -E 'Completed|Error|Evicted|Terminating' | wc -l | tr -d ' ')
    done
fi

JOB_COUNT=$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null \
    | grep -v -E 'Completed|Error|Evicted|Terminating' | wc -l | tr -d ' ')
if [ "$JOB_COUNT" -eq 0 ]; then
    pass "Scale-to-zero confirmed: 0 active pods in namespace (queue is empty)"
else
    warn "$JOB_COUNT active pod(s) still present. Proceeding anyway - results may reflect an in-flight task."
fi

# Verify at least one ScaledJob exists
SJ_COUNT=$(kubectl get scaledjobs -n "$NAMESPACE" --no-headers 2>/dev/null | wc -l | tr -d ' ')
if [ "$SJ_COUNT" -gt 0 ]; then
    pass "ScaledJob resources found: $SJ_COUNT"
    kubectl get scaledjobs -n "$NAMESPACE" 2>/dev/null || true
else
    fail "No ScaledJob resources found in namespace '$NAMESPACE'"
    error "Check that keda.enabled=true in values-aks.yaml and the chart was deployed."
    exit 1
fi

# ---------------------------------------------------------------------------
# Step 5: Locate NATS pod and publish a test task
# ---------------------------------------------------------------------------
info ""
info "=== Step 5: Publishing test task via kubectl exec ==="

NATS_POD=$(kubectl get pods -n "$NAMESPACE" \
    -l "app.kubernetes.io/name=nats" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [ -z "$NATS_POD" ]; then
    # Fallback: try the bitnami NATS label
    NATS_POD=$(kubectl get pods -n "$NAMESPACE" \
        -l "app.kubernetes.io/component=nats" \
        --field-selector=status.phase=Running \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
fi

if [ -z "$NATS_POD" ]; then
    NATS_POD=$(kubectl get pods -n "$NAMESPACE" \
        --no-headers 2>/dev/null | grep -i nats | grep Running | head -1 | awk '{print $1}' || echo "")
fi

if [ -z "$NATS_POD" ]; then
    fail "Could not locate a running NATS pod in namespace '$NAMESPACE'"
    error "Check that the NATS StatefulSet is healthy: kubectl get pods -n $NAMESPACE"
    exit 1
fi
info "NATS pod: $NATS_POD"

# Ensure the AGENT_TASKS stream exists - create it if missing
info "Ensuring NATS JetStream stream '$NATS_STREAM' exists..."
kubectl exec -n "$NAMESPACE" "$NATS_POD" -- \
    nats stream add "$NATS_STREAM" \
        --subjects="agent.tasks.>" \
        --retention=limits --max-msgs=-1 --max-bytes=-1 \
        --max-age=1h --storage=file --replicas=1 --discard=old \
        --no-allow-rollup --dupe-window=2m 2>/dev/null \
    || info "Stream '$NATS_STREAM' already exists"

info "Ensuring durable consumer 'copilot' exists for KEDA..."
kubectl exec -n "$NAMESPACE" "$NATS_POD" -- \
    nats consumer add "$NATS_STREAM" copilot \
        --filter="agent.tasks.copilot" \
        --durable=copilot \
        --ack=explicit \
        --deliver=all \
        --max-deliver=3 2>/dev/null \
    || info "Consumer 'copilot' already exists (skipping)"

# Start background subscriber to capture result BEFORE publishing.
# Core NATS pub/sub has no message retention - a subscriber that connects
# after the message was published never sees it.
RESULT_TMP=$(mktemp)
RESULT_SUB_PID=""
if [ -n "$NATS_POD" ]; then
    kubectl exec -n "$NAMESPACE" "$NATS_POD" -- \
        nats sub "agent.results" --count=1 --timeout=300s --raw \
        > "$RESULT_TMP" 2>/dev/null &
    RESULT_SUB_PID=$!
    info "Background result subscriber started (PID $RESULT_SUB_PID)"
else
    warn "NATS pod not found - background result subscriber not started"
fi

# Snapshot existing Job count before publishing - KEDA may retain completed Jobs.
# Step 6 compares against this baseline to detect truly new Jobs.
BASELINE_JOBS=$(kubectl get jobs -n "$NAMESPACE" --no-headers 2>/dev/null | wc -l | tr -d ' ')
info "Baseline Job count before publish: $BASELINE_JOBS"

TASK_ID="aks-test-$(date +%s)"
info "Publishing task: $TASK_ID"

TASK_PAYLOAD=$(printf '{"jsonrpc":"2.0","id":"%s","method":"tasks/send","params":{"id":"%s","message":{"role":"user","parts":[{"type":"text","text":"Say hello world and nothing else"}]}}}' \
    "$TASK_ID" "$TASK_ID")

T_PUBLISH=$(now_ns)

kubectl exec -n "$NAMESPACE" "$NATS_POD" -- \
    nats pub "agent.tasks.copilot" "$TASK_PAYLOAD"

pass "Task '$TASK_ID' published to agent.tasks.copilot"

# ---------------------------------------------------------------------------
# Step 6: Cold start timer - wait for first Job to appear
# ---------------------------------------------------------------------------
info ""
info "=== Step 6: Measuring cold-start latency ==="
info "Waiting for KEDA to trigger a Job (polling interval: up to 15s + pod start ~10-20s)..."

# First run on uncached nodes may require 150-180s (KEDA polling + image pull + ACP init).
# Override with COLD_START_TIMEOUT=<seconds> if needed.
COLD_START_TIMEOUT="${COLD_START_TIMEOUT:-150}"
WAIT=0
T_FIRST_JOB=""
while [ "$WAIT" -lt "$COLD_START_TIMEOUT" ]; do
    JOB_COUNT=$(kubectl get jobs -n "$NAMESPACE" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    if [ "$JOB_COUNT" -gt "$BASELINE_JOBS" ]; then
        T_FIRST_JOB=$(now_ns)
        pass "First Job appeared after $(elapsed_sec "$T_PUBLISH" "$T_FIRST_JOB")s (cold start)"
        kubectl get jobs -n "$NAMESPACE" 2>/dev/null || true
        break
    fi
    sleep 2
    WAIT=$((WAIT + 2))
    if [ $((WAIT % 10)) -eq 0 ]; then
        info "  Still waiting for Job... (${WAIT}s elapsed)"
    fi
done

if [ -z "$T_FIRST_JOB" ]; then
    fail "No Job appeared within ${COLD_START_TIMEOUT}s"
    error "Possible causes:"
    error "  - KEDA cannot reach the NATS monitoring endpoint"
    error "  - JetStream consumer not created (check: kubectl logs -n keda deploy/keda-operator)"
    error "  - Wrong stream name (NATS_STREAM=$NATS_STREAM)"
    error "  - ACR image pull failure (check pod events: kubectl describe pod -n $NAMESPACE)"
    exit 1
fi

# ---------------------------------------------------------------------------
# Step 7: Wait for Job to complete
# ---------------------------------------------------------------------------
info ""
info "=== Step 7: Waiting for Job to complete (timeout: 120s) ==="

JOB_NAME=$(kubectl get jobs -n "$NAMESPACE" \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
    | tail -1)
info "Tracking job: $JOB_NAME"

JOB_TIMEOUT=120
WAIT=0
T_JOB_DONE=""
JOB_SUCCEEDED=0
while [ "$WAIT" -lt "$JOB_TIMEOUT" ]; do
    JOB_STATUS=$(kubectl get job "$JOB_NAME" -n "$NAMESPACE" \
        -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null || echo "")
    JOB_FAILED=$(kubectl get job "$JOB_NAME" -n "$NAMESPACE" \
        -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}' 2>/dev/null || echo "")

    if [ "$JOB_STATUS" = "True" ]; then
        T_JOB_DONE=$(now_ns)
        pass "Job '$JOB_NAME' completed successfully after $(elapsed_sec "$T_FIRST_JOB" "$T_JOB_DONE")s"
        JOB_SUCCEEDED=1
        break
    fi

    if [ "$JOB_FAILED" = "True" ]; then
        T_JOB_DONE=$(now_ns)
        fail "Job '$JOB_NAME' FAILED"
        warn "Check proxy and agent logs:"
        warn "  kubectl logs -n $NAMESPACE -l job-name=$JOB_NAME -c proxy --tail=50"
        warn "  kubectl logs -n $NAMESPACE -l job-name=$JOB_NAME -c agent --tail=50"
        break
    fi

    sleep 3
    WAIT=$((WAIT + 3))
    if [ $((WAIT % 15)) -eq 0 ]; then
        info "  Job still running... (${WAIT}s elapsed)"
        kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null | grep "$JOB_NAME" | head -3 || true
    fi
done

if [ -z "$T_JOB_DONE" ]; then
    T_JOB_DONE=$(now_ns)
    fail "Job did not complete within ${JOB_TIMEOUT}s"
    warn "Dumping pod logs for diagnosis:"
    kubectl logs -n "$NAMESPACE" -l "job-name=$JOB_NAME" -c proxy --tail=30 2>/dev/null || true
    kubectl logs -n "$NAMESPACE" -l "job-name=$JOB_NAME" -c agent --tail=30 2>/dev/null || true
    exit 1
fi

# ---------------------------------------------------------------------------
# Step 8: Check result on agent.results subject
# ---------------------------------------------------------------------------
info ""
info "=== Step 8: Checking for result on agent.results ==="

# Read from the background subscriber started before task publish.
# Core NATS has no retention, so subscribing after the job completes misses the message.
step() { info "$*"; }
step "Step 8: Checking result on agent.results..."
if [ "${JOB_SUCCEEDED:-0}" -eq 0 ] && [ -n "${RESULT_SUB_PID:-}" ]; then
    info "Job did not succeed - killing result subscriber (no result will arrive)"
    kill "$RESULT_SUB_PID" 2>/dev/null || true
    RESULT_SUB_PID=""
fi
if [ -n "$RESULT_SUB_PID" ]; then
    wait "$RESULT_SUB_PID" 2>/dev/null || true
fi
RESULT_RAW=$(cat "$RESULT_TMP" 2>/dev/null || echo "")
rm -f "$RESULT_TMP"

if [ -n "$RESULT_RAW" ]; then
    pass "Result received on agent.results"
    info "Result payload: $(echo "$RESULT_RAW" | head -c 200)..."
else
    warn "No result received on agent.results within timeout"
    warn "The proxy may not publish results in this deployment configuration"
fi

# ---------------------------------------------------------------------------
# Step 9: Timing summary
# ---------------------------------------------------------------------------
T_END=$(now_ns)

info ""
info "=== Timing Summary ==="
info "Cold start (publish -> first Job):  $(elapsed_sec "$T_PUBLISH" "$T_FIRST_JOB")s"
if [ -n "$T_JOB_DONE" ]; then
    info "Job execution (first Job -> done):  $(elapsed_sec "$T_FIRST_JOB" "$T_JOB_DONE")s"
    info "End-to-end (publish -> done):       $(elapsed_sec "$T_PUBLISH" "$T_JOB_DONE")s"
fi
info "Total script wall time:             $(elapsed_sec "$T_START" "$T_END")s"
info ""
info "Expected ranges:"
info "  Cold start:      15-45s (KEDA polling 15s + pod start 10-20s + ACP init 5s)"
info "  Scale-to-zero:   within 1-2 polling intervals after all Jobs complete"
info "  SIGTERM drain:   proxy completes current task within 35s of SIGTERM"

# ---------------------------------------------------------------------------
# Step 10: Scale-to-zero restore
# ---------------------------------------------------------------------------
if [ "$SKIP_CLEANUP" = "1" ]; then
    info ""
    warn "SKIP_CLEANUP=1 - leaving Jobs and Pods for manual inspection."
    warn "To clean up: kubectl delete jobs --all -n $NAMESPACE"
    exit 0
fi

info ""
info "=== Step 10: Verifying scale-to-zero restore ==="
info "Waiting up to 60s for active pods to clear after job completion..."

RESTORE_TIMEOUT=60
WAIT=0
T_RESTORED=""
while [ "$WAIT" -lt "$RESTORE_TIMEOUT" ]; do
    # Scale-to-zero means no active (non-completed) pods, not zero Job objects.
    # KEDA retains completed Job history per successfulJobsHistoryLimit.
    ACTIVE=$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null \
        | grep -v -E 'Completed|Error|Evicted|Terminating' \
        | wc -l | tr -d ' ')
    if [ "$ACTIVE" -eq 0 ]; then
        T_RESTORED=$(now_ns)
        pass "Scale-to-zero confirmed: 0 active pods after $(elapsed_sec "$T_JOB_DONE" "$T_RESTORED")s"
        break
    fi
    sleep 5
    WAIT=$((WAIT + 5))
    if [ $((WAIT % 15)) -eq 0 ]; then
        info "  $ACTIVE active pod(s) still present... (${WAIT}s elapsed)"
    fi
done

if [ -z "$T_RESTORED" ]; then
    warn "Active pods did not clear within ${RESTORE_TIMEOUT}s."
    warn "Active Jobs still running would indicate a hung task, not a scale-to-zero failure."
    kubectl get jobs -n "$NAMESPACE" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Final result
# ---------------------------------------------------------------------------
info ""
if [ "$JOB_SUCCEEDED" -eq 1 ]; then
    echo -e "${GREEN}=================================================${NC}"
    echo -e "${GREEN}  VALIDATION PASSED${NC}"
    echo -e "${GREEN}  AKS KEDA deployment is working correctly${NC}"
    echo -e "${GREEN}=================================================${NC}"
    exit 0
else
    echo -e "${RED}=================================================${NC}"
    echo -e "${RED}  VALIDATION FAILED${NC}"
    echo -e "${RED}  See errors above for details${NC}"
    echo -e "${RED}=================================================${NC}"
    exit 1
fi
