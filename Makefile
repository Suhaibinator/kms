# Build pipeline for the parameter-store binary. The Next.js static export is
# built first (into frontend/out) and embedded into the Go binary, per plan 6.3.

BINARY      := bin/parameter-store
PKG         := ./cmd/parameter-store
FRONTEND    := frontend
FRONTEND_OUT:= frontend/out
TYPESCRIPT_SDK := sdk/typescript
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X github.com/Suhaibinator/kms/internal/cli.Version=$(VERSION)
GO_TEST_TIMEOUT ?= 10m
GO_TEST_FLAGS   ?=
INTEGRATION_COVERPKG ?= ./internal/...,./sdk/go/...
INTEGRATION_COVERAGE_PROFILE ?= integration-coverage.out

.PHONY: build frontend backend test test-unit test-unit-shard test-integration \
	test-integration-race check-integration-coverage test-platform-security \
	vet check-frontend check-configgen typescript test-typescript \
	check-typescript clean tidy

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
	@# `next build` wipes frontend/out, taking the tracked .gitkeep with it.
	@# That file is what makes the //go:embed directive resolve on a fresh
	@# clone before this target has ever run, so put it back — otherwise the
	@# build leaves the working tree with a spurious deletion staged.
	@mkdir -p $(FRONTEND_OUT) && touch $(FRONTEND_OUT)/.gitkeep

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

# Verify the committed managed-config binding, schema, and contract without
# rewriting them. Run the same command after changing configuration tags.
check-configgen:
	go run ./cmd/kms-config-gen \
		-package ./internal/configstorefixture/config \
		-type Config \
		-binding-package configkms \
		-binding-output internal/configstorefixture/configkms/config_kms.gen.go \
		-schema-output internal/configstorefixture/runtime.schema.json \
		-contract-output internal/configstorefixture/runtime.contract.json \
		-check
	go run ./cmd/kms-config-gen \
		-package ./examples/managed-config/config \
		-type Config \
		-binding-package configkms \
		-binding-output examples/managed-config/configkms/config_kms.gen.go \
		-schema-output examples/managed-config/runtime.schema.json \
		-contract-output examples/managed-config/runtime.contract.json \
		-check

# Build and verify the independently published Node.js SDK. These targets use
# npm ci so they exercise the committed lockfile and behave the same on a fresh
# clone as they do in CI.
typescript:
	@echo ">> building TypeScript SDK"
	cd $(TYPESCRIPT_SDK) && npm ci && npm run build

test-typescript:
	@echo ">> testing TypeScript SDK"
	cd $(TYPESCRIPT_SDK) && npm ci && npm run test && npm run test:types

check-typescript:
	@echo ">> checking TypeScript SDK"
	cd $(TYPESCRIPT_SDK) && npm ci && npm run check

# Run every Go test, including integration tests, with the race detector. This
# remains the convenient all-in-one local regression command.
test:
	go test ./... -race -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Run Go unit/component packages without the intentionally slower
# internal/integration package. The Go template keeps package selection
# portable and makes the CI unit/integration split explicit.
test-unit:
	go test $$(go list -f '{{if ne .ImportPath "github.com/Suhaibinator/kms/internal/integration"}}{{.ImportPath}}{{end}}' ./...) -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Split the unit suite into disjoint CI shards. Keeping the package selection
# here makes it easy to reproduce a shard locally and ensures the coverage
# profiles can be combined without duplicate package blocks.
test-unit-shard:
	@packages=''; \
	case "$(GO_TEST_SHARD)" in \
		configgen) packages='./internal/configgen' ;; \
		cli-storage) packages='./internal/cli ./internal/storage' ;; \
		remaining) packages="$$(go list -f '{{if and (ne .ImportPath "github.com/Suhaibinator/kms/internal/integration") (ne .ImportPath "github.com/Suhaibinator/kms/internal/configgen") (ne .ImportPath "github.com/Suhaibinator/kms/internal/cli") (ne .ImportPath "github.com/Suhaibinator/kms/internal/storage")}}{{.ImportPath}}{{end}}' ./...)" ;; \
		*) echo "ERROR: unknown GO_TEST_SHARD $(GO_TEST_SHARD)" >&2; exit 2 ;; \
	esac; \
	go test $$packages -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Run the hermetic integration suite. Instrument production packages explicitly:
# internal/integration contains only external-package tests, so its own package
# has no statements and would otherwise produce a vacuous 0.0% profile.
test-integration:
	go test ./internal/integration -coverpkg=$(INTEGRATION_COVERPKG) -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

test-integration-race:
	go test ./internal/integration -coverpkg=$(INTEGRATION_COVERPKG) -race -count=1 -timeout=$(GO_TEST_TIMEOUT) $(GO_TEST_FLAGS)

# Reject an absent or vacuous integration coverage profile. CI invokes this
# after test-integration; it is also useful when changing the integration
# package selection or coverpkg patterns locally.
check-integration-coverage:
	@if [ ! -s "$(INTEGRATION_COVERAGE_PROFILE)" ]; then \
		echo "ERROR: integration coverage profile $(INTEGRATION_COVERAGE_PROFILE) is missing or empty"; \
		exit 1; \
	fi
	@total=$$(go tool cover -func="$(INTEGRATION_COVERAGE_PROFILE)" | awk '$$1 == "total:" { value=$$3; gsub(/%/, "", value); print value }'); \
	if [ -z "$$total" ]; then \
		echo "ERROR: integration coverage profile has no total statement coverage"; \
		exit 1; \
	fi; \
	if ! awk -v total="$$total" 'BEGIN { exit !(total > 0.0) }'; then \
		echo "ERROR: integration production coverage is $$total%; expected greater than 0.0%"; \
		exit 1; \
	fi; \
	echo ">> integration production coverage: $$total%"

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
