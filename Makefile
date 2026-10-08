.PHONY: build test-race tidy-check lint ci e2e

build:
	go build -trimpath -o kyquickstart ./cmd/kyquickstart

test-race:
	go test -race -count=1 ./...

# A stale go.sum fails CI; fail here first.
tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go vet -tags e2e ./...

ci: tidy-check lint test-race

# Needs Docker on this machine.
e2e:
	go test -tags e2e -count=1 -v ./test/e2e/
