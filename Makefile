# Build pipeline for the parameter-store binary. The Next.js static export is
# built first (into frontend/out) and embedded into the Go binary, per plan 6.3.

BINARY      := bin/parameter-store
PKG         := ./cmd/parameter-store
FRONTEND    := frontend
FRONTEND_OUT:= frontend/out
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X github.com/Suhaibinator/kms/internal/cli.Version=$(VERSION)
GO_TEST_TIMEOUT ?= 10m
GO_TEST_FLAGS   ?=

.PHONY: build frontend backend test test-unit test-integration \
	test-integration-race test-platform-security vet check-frontend clean tidy

# Default: full build (frontend export + backend with embedded assets).
build: frontend backend

# Build the Next.js static export into frontend/out. Tolerates the frontend
# not being scaffolded yet so backend work is unblocked.
frontend:
	@if [ -f "$(FRONTEND)/package.json" ]; then \
		echo ">> building frontend static export"; \
		cd $(FRONTEND) && npm ci && npm run build; \
	else \
		echo ">> skipping frontend build: $(FRONTEND)/package.json not found"; \
		echo "   (the Go build embeds frontend/out; a placeholder is served until the UI is built)"; \
	fi

# Compile the Go binary, embedding whatever is in frontend/out.
backend:
	@echo ">> building $(BINARY) (version $(VERSION))"
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# Fail if the embedded frontend entry point is missing. Intended for CI before
# a release build so an empty UI never ships silently.
check-frontend:
	@if [ ! -f "$(FRONTEND_OUT)/index.html" ]; then \
		echo "ERROR: $(FRONTEND_OUT)/index.html is missing; run 'make frontend' first"; \
		exit 1; \
	fi
	@echo ">> frontend export present"

# Run every Go test, including integration tests, with the race detector. This
# remains the convenient all-in-one local regression command.
test:
	go test ./... -race -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Run Go unit/component packages without the intentionally slower
# internal/integration package. The Go template keeps package selection
# portable and makes the CI unit/integration split explicit.
test-unit:
	go test $$(go list -f '{{if ne .ImportPath "github.com/Suhaibinator/kms/internal/integration"}}{{.ImportPath}}{{end}}' ./...) -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Run the hermetic integration suite. It starts the real stack against
# temporary SQLite/key files and must not depend on external services.
test-integration:
	go test ./internal/integration -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

test-integration-race:
	go test ./internal/integration -race -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Exercise native filesystem permission/ACL behavior. CI runs this target on
# Linux, macOS, and Windows so build-tagged platform tests execute natively.
test-platform-security:
	go test ./internal/fileutil ./internal/crypto ./internal/storage ./internal/cli -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BINARY)
