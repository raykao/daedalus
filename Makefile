.PHONY: test-contract test

test-contract:
	go test ./test/contract/... -v -count=1

test:
	go test ./... -v -count=1
