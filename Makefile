# Build pipeline for the parameter-store binary. The Next.js static export is
# built first (into frontend/out) and embedded into the Go binary, per plan 6.3.

BINARY      := bin/parameter-store
PKG         := ./cmd/parameter-store
FRONTEND    := frontend
FRONTEND_OUT:= frontend/out
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X github.com/Suhaibinator/kms/internal/cli.Version=$(VERSION)

.PHONY: build frontend backend test vet check-frontend clean tidy

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

# Run the full test suite with the race detector.
test:
	go test ./... -race

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BINARY)
