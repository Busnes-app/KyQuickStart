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
	go vet -tags e2e,goolm ./...

ci: tidy-check lint test-race

# Needs Docker. goolm is the test-only Matrix client's pure-Go Olm. The kind TLS helper runs the
# test executable in distroless. test/e2e measured 33.7 min on the workstation and an estimated
# 38 min on CI (2026-10-20: 31.5 min with TestKubernetesKyMessage failing at 33 s, which takes
# about 7 min when it passes): 60m leaves room for a slower runner.
e2e:
	CGO_ENABLED=0 go test -tags e2e,goolm -count=1 -v -timeout 60m ./test/e2e/ ./internal/edge/
# Real Forgejo: revocation, refused reactivation (no session revocation upstream), revoke again.
	KYQ_TEST_FORGEJO_IMAGE=codeberg.org/forgejo/forgejo:16.0.5-rootless@sha256:5effb7305584aca479b29fde6f9631a6dbe86ae798ae02eeea33a3666f0c0bf8 \
		KYQ_TEST_FORGEJO_QUEUE=1 KYQ_TEST_FORGEJO_SCIM=1 KYQ_TEST_FORGEJO_REACTIVATE=1 \
		go test -race -count=1 -v -timeout 5m -run '^TestNativeAccountInspectionAndOffboarding$$' ./internal/adapters/forgejo

# Needs Docker. Gates the publishing workflows; zizmor 1.31.1, offline, no network.
ZIZMOR := ghcr.io/zizmorcore/zizmor@sha256:42d409dbb851d4c5d35a058cf042b828bea1e3f2884378c526eb0974780e18d9
workflow-lint:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
	docker run --rm --network none -v "$(CURDIR)/.github:/src/.github:ro" $(ZIZMOR) \
		--offline --min-severity medium /src/.github/workflows/release-image.yml /src/.github/workflows/kyoffboard-image.yml
