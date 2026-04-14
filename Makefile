.PHONY: test-contract test-conformance test test-integration test-smoke

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
