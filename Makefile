.PHONY: build test-race tidy-check lint ci e2e workflow-lint

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
	CGO_ENABLED=0 go test -tags e2e -count=1 -v -timeout 30m ./test/e2e/ ./internal/edge/

# Needs Docker. Gates release-image.yml; zizmor 1.31.1, offline, no network.
ZIZMOR := ghcr.io/zizmorcore/zizmor@sha256:42d409dbb851d4c5d35a058cf042b828bea1e3f2884378c526eb0974780e18d9
workflow-lint:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
	docker run --rm --network none -v "$(CURDIR)/.github:/src/.github:ro" $(ZIZMOR) \
		--offline --min-severity medium /src/.github/workflows/release-image.yml
