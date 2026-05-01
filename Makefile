.PHONY: test-contract test-conformance test test-integration test-smoke \
        helm-aks-deploy helm-aks-teardown helm-aks-status helm-aks-logs

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

test-smoke:
	@echo "Requires GITHUB_TOKEN with Copilot access"
	@test -n "$$GITHUB_TOKEN" || { echo "ERROR: GITHUB_TOKEN not set"; exit 1; }
	go test ./test/integration/... -tags=smoke -v -count=1 -timeout=600s

# ---------------------------------------------------------------------------
# AKS Helm deployment targets
# ---------------------------------------------------------------------------

# helm-aks-deploy - install or upgrade the Daedalus release on AKS.
# Applies the AKS overlay values-aks.yaml on top of the chart's own values.yaml
# (Helm auto-loads deploy/helm/daedalus/values.yaml from the chart directory).
# Creates the namespace if it does not already exist.
# Blocks until all resources are ready or the 5-minute timeout expires.
#
# Usage:
#   make helm-aks-deploy
#   make helm-aks-deploy RELEASE_NAME=daedalus NAMESPACE=daedalus
helm-aks-deploy: check-aks-context
	helm upgrade --install $(RELEASE_NAME) deploy/helm/daedalus/ \
	    -f deploy/helm/values-aks.yaml \
	    --namespace $(NAMESPACE) \
	    --create-namespace \
	    --wait --timeout 5m

# helm-aks-teardown - remove the Helm release and associated resources.
# Does NOT delete the namespace or any Kubernetes Secrets you created
# manually (e.g. acr-pull-secret, copilot-secret).
#
# Usage:
#   make helm-aks-teardown
helm-aks-teardown: check-aks-context
	helm uninstall $(RELEASE_NAME) --namespace $(NAMESPACE) || true

# helm-aks-status - show Helm release status and live Kubernetes resources.
# Prints ScaledJobs, active Jobs, and running Pods in the namespace.
#
# Usage:
#   make helm-aks-status
helm-aks-status: check-aks-context
	@echo "=== Helm release status ==="
	helm status $(RELEASE_NAME) --namespace $(NAMESPACE)
	@echo ""
	@echo "=== ScaledJobs ==="
	kubectl get scaledjobs -n $(NAMESPACE) -o wide || true
	@echo ""
	@echo "=== Jobs ==="
	kubectl get jobs -n $(NAMESPACE) -o wide || true
	@echo ""
	@echo "=== Pods ==="
	kubectl get pods -n $(NAMESPACE) -o wide || true

# helm-aks-logs - tail logs for proxy and agent containers of a worker.
# Defaults to the most recently created pod for WORKER.
# Streams both the proxy sidecar and the agent container side by side.
#
# Usage:
#   make helm-aks-logs               # defaults to WORKER=copilot
#   make helm-aks-logs WORKER=claude
WORKER ?= copilot
helm-aks-logs: check-aks-context
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
