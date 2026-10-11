.PHONY: build test-race tidy-check lint ci e2e

build:
	go build -trimpath -o kyquickstart ./cmd/kyquickstart
	go build -trimpath -o kyoffboard ./cmd/kyoffboard

test-race:
	go test -race -count=1 ./...

# A stale go.sum fails CI; fail here first.
tidy-check:
	go mod tidy -diff

lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go vet -tags e2e ./...

ci: tidy-check lint test-race

# Needs Docker. The kind TLS helper runs the test executable in distroless.
e2e:
	CGO_ENABLED=0 go test -tags e2e -count=1 -v -timeout 20m ./test/e2e/ ./internal/edge/
