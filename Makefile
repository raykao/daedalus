.PHONY: test-contract test-conformance test

test-contract:
	go test ./test/contract/... -v -count=1

test-conformance:
	go test ./test/conformance/... -v -count=1

test:
	go test ./... -v -count=1
