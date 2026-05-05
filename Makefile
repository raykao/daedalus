.PHONY: test-contract test-conformance test test-integration test-smoke \
        test-trace-propagation \
        test-aks-e2e \
        aks-logs \
        deploy-aks-test destroy-aks-test aks-credentials aks-status \
        cleanup-aks-test test-cleanup-script

# AKS deployment defaults - override on the command line as needed.
RELEASE_NAME ?= daedalus
NAMESPACE    ?= daedalus

# check-aks-context - abort when kubectl is not pointing at an AKS cluster
# whose context name contains "daedalus".  This prevents accidental deploys
# to the wrong cluster.
.PHONY: check-aks-context
check-aks-context:
	@CURRENT_CTX=$$(kubectl config current-context 2>/dev/null || echo ""); \
	if [ -z "$$CURRENT_CTX" ]; then \
	    echo "ERROR: kubectl has no current context. Run 'az aks get-credentials ...' first."; \
	    exit 1; \
	fi; \
	if ! echo "$$CURRENT_CTX" | grep -q "daedalus"; then \
	    echo "ERROR: current kube context '$$CURRENT_CTX' does not contain 'daedalus'."; \
	    echo "       Point kubectl at the correct AKS cluster before deploying."; \
	    exit 1; \
	fi; \
	echo "Kube context OK: $$CURRENT_CTX"

test-contract:
	go test ./test/contract/... -v -count=1

test-conformance:
	go test ./test/conformance/... -v -count=1

test:
	go test ./... -v -count=1

test-integration:
	docker compose -f deploy/docker/docker-compose.yml build --quiet
	go test ./test/integration/... -tags=integration -v -count=1 -timeout=120s

# test-trace-propagation - Phase 6.1 integration test that asserts W3C
# TraceContext propagates across every hop of the daedalus task pipeline
# (publish -> consume -> proxy.handle -> ACP -> result publish ->
# collector). Runs 100 concurrent tasks and verifies the per-task span
# tree. Pure in-process: embeds NATS and a fake ACP server, no Docker.
test-trace-propagation:
	go test ./test/integration/trace-propagation/... -tags=integration -v -count=1 -timeout=120s

test-smoke:
	@echo "Requires GITHUB_TOKEN with Copilot access"
	@if [ -z "$$GITHUB_TOKEN" ] && [ -f smoke.env ]; then \
		echo "Loading credentials from smoke.env..."; \
		set -a && . ./smoke.env && set +a && \
		test -n "$$GITHUB_TOKEN" || { echo "ERROR: GITHUB_TOKEN not set in smoke.env"; exit 1; } && \
		go test ./test/integration/... -tags=smoke -v -count=1 -timeout=600s; \
	elif [ -n "$$GITHUB_TOKEN" ]; then \
		go test ./test/integration/... -tags=smoke -v -count=1 -timeout=600s; \
	else \
		echo "ERROR: GITHUB_TOKEN not set. Set it in the environment or copy smoke.env.example to smoke.env and fill in your token."; \
		exit 1; \
	fi

# test-aks-e2e - run the Phase 5.4 end-to-end harness against a live AKS
# cluster. Pre-conditions:
#   1. `make deploy-aks-test` has succeeded (cluster + Helm release up)
#   2. NATS is reachable at $$NATS_URL. For local runs, port-forward first:
#        kubectl port-forward -n daedalus svc/daedalus-nats 4222:4222 &
#   3. aks.env exists at the repo root (copy from aks.env.example), or the
#      required env vars are exported in the shell.
test-aks-e2e:
	@if [ -z "$$NATS_URL" ] && [ -f aks.env ]; then \
		echo "Loading config from aks.env..."; \
		set -a && . ./aks.env && set +a && \
		test -n "$$NATS_URL" || { echo "ERROR: NATS_URL not set in aks.env"; exit 1; } && \
		go test -tags aks_e2e ./test/e2e/aks/... -v -count=1 -timeout 30m; \
	elif [ -n "$$NATS_URL" ]; then \
		go test -tags aks_e2e ./test/e2e/aks/... -v -count=1 -timeout 30m; \
	else \
		echo "ERROR: NATS_URL not set and no aks.env file found."; \
		echo "       Copy aks.env.example to aks.env and fill in values, or export NATS_URL,"; \
		echo "       KUBE_CONTEXT and friends in the environment before re-running."; \
		echo "       Typical local setup also requires:"; \
		echo "         kubectl port-forward -n daedalus svc/daedalus-nats 4222:4222 &"; \
		exit 1; \
	fi

# ---------------------------------------------------------------------------
# AKS Helm deployment targets
# ---------------------------------------------------------------------------

# aks-logs - tail logs for proxy and agent containers of a worker.
# Defaults to the most recently created pod for WORKER.
# Streams both the proxy sidecar and the agent container side by side.
#
# Usage:
#   make aks-logs               # defaults to WORKER=copilot
#   make aks-logs WORKER=claude
WORKER ?= copilot
aks-logs: check-aks-context
	@POD=$$(kubectl get pods -n $(NAMESPACE) \
	    -l "app.kubernetes.io/component=$(WORKER)" \
	    --sort-by=.metadata.creationTimestamp \
	    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
	    | tail -1); \
	if [ -z "$$POD" ]; then \
	    echo "No pods found for worker '$(WORKER)' in namespace '$(NAMESPACE)'"; \
	    echo "If the queue is empty, KEDA will not create any Jobs (scale-to-zero is active)."; \
	    exit 0; \
	fi; \
	echo "=== Logs for pod $$POD (proxy + agent) ==="; \
	kubectl logs -n $(NAMESPACE) "$$POD" -c proxy --tail=100 --prefix=true & \
	kubectl logs -n $(NAMESPACE) "$$POD" -c agent --tail=100 --prefix=true & \
	wait

# ---------------------------------------------------------------------------
# Phase 5.3: Automated AKS test deployment
# ---------------------------------------------------------------------------
# These targets are thin wrappers over deploy/scripts/deploy-aks.sh and
# deploy/scripts/destroy-aks.sh. The scripts are the source of truth for the
# deploy/destroy flow; treat the Makefile as the user-facing entrypoint only.
#
# Usage:
#   make deploy-aks-test       # one-command idempotent deploy (~25 min cold)
#   make destroy-aks-test      # mirror teardown (respects KEEP_CLUSTER=1)
#   make aks-credentials       # refresh local kubeconfig from terraform state
#   make aks-status            # cluster + helm + KEDA + Job summary

# deploy-aks-test - run deploy/scripts/deploy-aks.sh end-to-end. Safe to re-run.
# Honors GITHUB_TOKEN, IMAGE_TAG, RELEASE_NAME, NAMESPACE, KEDA_VERSION env vars.
deploy-aks-test:
	@./deploy/scripts/deploy-aks.sh

# destroy-aks-test - run deploy/scripts/destroy-aks.sh. Refuses if KEEP_CLUSTER=1.
destroy-aks-test:
	@./deploy/scripts/destroy-aks.sh

# aks-credentials - reload kubeconfig for the cluster recorded in terraform state.
# Useful when context was overwritten or the entry expired.
aks-credentials:
	@RG=$$(terraform -chdir=deploy/terraform output -raw resource_group_name 2>/dev/null); \
	AKS=$$(terraform -chdir=deploy/terraform output -raw aks_name 2>/dev/null); \
	if [ -z "$$RG" ] || [ -z "$$AKS" ]; then \
	    echo "ERROR: terraform state has no resource_group_name / aks_name."; \
	    echo "       Run 'make deploy-aks-test' first or check deploy/terraform/."; \
	    exit 1; \
	fi; \
	echo "Fetching credentials for AKS '$$AKS' in RG '$$RG'..."; \
	az aks get-credentials --resource-group "$$RG" --name "$$AKS" \
	    --overwrite-existing --admin=false

# aks-status - high-level health snapshot: TF outputs, helm release, KEDA, Jobs.
# Warns if current kube context does not match the cluster recorded in terraform.
aks-status:
	@CURRENT_CTX=$$(kubectl config current-context 2>/dev/null || echo "<none>"); \
	EXPECTED=$$(terraform -chdir=deploy/terraform output -raw aks_name 2>/dev/null || echo ""); \
	if [ -n "$$EXPECTED" ] && [ "$$CURRENT_CTX" != "$$EXPECTED" ]; then \
	    echo "WARNING: kube context '$$CURRENT_CTX' != terraform aks_name '$$EXPECTED'."; \
	    echo "         Run 'make aks-credentials' to realign before trusting this status."; \
	fi
	@echo "=== Terraform outputs ==="; \
	terraform -chdir=deploy/terraform output 2>/dev/null \
	    | grep -v -i 'kubeconfig\|sensitive' || true
	@echo ""
	@echo "=== Current kube context ==="
	@kubectl config current-context 2>/dev/null || echo "<no context>"
	@echo ""
	@echo "=== Nodes ==="
	@kubectl get nodes -o wide 2>/dev/null || echo "<unreachable>"
	@echo ""
	@echo "=== Helm release ($(RELEASE_NAME) / $(NAMESPACE)) ==="
	@helm status $(RELEASE_NAME) --namespace $(NAMESPACE) 2>/dev/null \
	    || echo "<release not installed>"
	@echo ""
	@echo "=== KEDA operator ==="
	@kubectl get deployment/keda-operator -n keda 2>/dev/null \
	    || echo "<KEDA not installed>"
	@echo ""
	@echo "=== ScaledJobs ==="
	@kubectl get scaledjobs -n $(NAMESPACE) -o wide 2>/dev/null || true
	@echo ""
	@echo "=== Jobs ==="
	@kubectl get jobs -n $(NAMESPACE) -o wide 2>/dev/null || true
	@echo ""
	@echo "=== Pods ==="
	@kubectl get pods -n $(NAMESPACE) -o wide 2>/dev/null || true

# ---------------------------------------------------------------------------
# Phase 5.5: TTL cleanup
# ---------------------------------------------------------------------------
# cleanup-aks-test - manual invocation of scripts/aks-cleanup.sh against the
# default Phase 5 prefix (rg-daedalus-). The same script runs once daily at
# 09:00 UTC from .github/workflows/nightly-cleanup.yml.
#
# Usage:
#   make cleanup-aks-test                 # real run; deletes expired RGs
#   make cleanup-aks-test DRY_RUN=1       # print intent only
#   DRY_RUN=1 make cleanup-aks-test       # same effect
cleanup-aks-test:
	@DRY_FLAG=""; \
	if [ "$${DRY_RUN:-0}" = "1" ]; then DRY_FLAG="--dry-run"; fi; \
	./scripts/aks-cleanup.sh --prefix rg-daedalus- $$DRY_FLAG

# test-cleanup-script - run the bash unit tests for scripts/aks-cleanup.sh.
# Does not require Azure or network access (uses a PATH-shimmed `az`).
test-cleanup-script:
	@bash scripts/aks-cleanup_test.sh
