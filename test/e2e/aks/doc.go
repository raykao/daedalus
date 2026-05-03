// Package aks contains end-to-end tests that run against a live AKS cluster
// previously provisioned by `make deploy-aks-test` (Phase 5.3).
//
// # Build tag
//
// All test files in this package are gated by the `aks_e2e` build tag, so
// `go build ./...` and the regular `go test ./...` invocations skip them.
// Run the suite via:
//
//	make test-aks-e2e
//
// or directly:
//
//	go test -tags aks_e2e ./test/e2e/aks/... -v -count=1 -timeout 30m
//
// # Environment contract
//
// The harness reads its configuration from the process environment. The
// Makefile target sources `aks.env` (mirroring the `smoke.env` pattern)
// before invoking `go test`, so the tests themselves never load the env
// file from Go.
//
// Required:
//   - NATS_URL       e.g. nats://localhost:4222 (typically a kubectl port-forward target)
//   - KUBE_CONTEXT   kubectl context pointing at the AKS cluster under test
//
// Optional (with defaults):
//   - NAMESPACE      defaults to "daedalus"
//   - RELEASE_NAME   defaults to "daedalus"
//   - TASK_TIMEOUT   Go duration string, defaults to "10m"
//   - KEEP_CLUSTER   any non-empty value preserves the cluster on success (logged only)
//   - RESOURCE_GROUP Azure resource group name, logged on test exit to aid manual cleanup
//   - EXPIRES_AT     ISO-8601 timestamp matching the cluster's `expires-at` resource-group
//     tag; logged on test exit only (used purely for operator hint)
//
// # Cluster assumption
//
// The harness assumes the cluster is already up and the Daedalus Helm release
// is deployed. It does NOT provision or destroy infrastructure. Cleanup is the
// operator's responsibility; the test only logs the resource group and the
// expires-at tag (when known) so the operator can extend or destroy manually.
package aks
