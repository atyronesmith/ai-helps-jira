BINARY    := jira-cli
MODULE    := github.com/atyronesmith/ai-helps-jira
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOFLAGS   := -trimpath
LDFLAGS   := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

# Cross-compile targets
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# Container
IMAGE     := jira-cli
CONTAINER := jira-cli-mcp
CONTAINER_HOST ?= 127.0.0.1
unexport CONTAINER_HOST IMAGE CONTAINER
export JIRA_CONTAINER_HOST := $(CONTAINER_HOST)
export JIRA_CONTAINER_IMAGE := $(IMAGE)
export JIRA_CONTAINER_NAME := $(CONTAINER)
BWS_ENV_FILE ?= .env

.PHONY: all build clean install uninstall test lint fmt vet tidy run \
        release check doc-lint restart-mcp container container-build container-run container-run-bitwarden container-stop \
        container-scan container-sign container-sbom help

all: check build  ## Run checks then build

build:  ## Build binary for current platform
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) .

run: build  ## Build and run with args (e.g., make run ARGS="summary")
	./$(BINARY) $(ARGS)

install: build  ## Install to $GOPATH/bin
	cp $(BINARY) $(shell go env GOPATH)/bin/$(BINARY)

uninstall:  ## Remove from $GOPATH/bin
	rm -f $(shell go env GOPATH)/bin/$(BINARY)

clean:  ## Remove build artifacts
	rm -f $(BINARY)
	rm -rf dist/

tidy:  ## Tidy and verify go.mod
	go mod tidy
	go mod verify

fmt:  ## Format all Go files
	gofmt -s -w .

vet:  ## Run go vet
	go vet ./...

lint: vet  ## Run linters (vet + staticcheck if available)
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... || \
		echo "staticcheck not installed, skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"

test:  ## Run tests
	go test -v -race -count=1 ./...

check: tidy fmt vet  ## Run all checks (tidy, fmt, vet)

doc-lint:  ## Check docs stay in sync with code (package + target coverage)
	@bash scripts/doc-lint.sh

release: clean  ## Build release binaries for all platforms
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		output=dist/$(BINARY)-$${os}-$${arch}; \
		echo "Building $${output}..."; \
		GOOS=$${os} GOARCH=$${arch} go build $(GOFLAGS) \
			-ldflags '$(LDFLAGS)' -o $${output} . || exit 1; \
	done
	@echo "Release binaries in dist/"
	@ls -lh dist/

restart-mcp: build  ## Rebuild and restart the MCP server
	@pkill -f './$(BINARY) mcp' 2>/dev/null || true
	@echo "MCP server restarted (will be relaunched on next tool call)"

container:  ## Build container image
	podman build --format docker -t $(IMAGE) .

container-build:   ## Rebuild container image with --no-cache
	podman build --no-cache --format docker -t $(IMAGE) .
container-run: container-stop container-build  ## Run MCP Streamable HTTP server in container (loopback only)
	podman run -d --name $(CONTAINER) \
		-p $(CONTAINER_HOST):8081:8081 -p $(CONTAINER_HOST):18080:18080 \
		-v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
		--read-only --tmpfs /tmp \
		--cap-drop=ALL \
		--env-file .env \
		$(IMAGE)
	@echo "MCP HTTP (local client): http://localhost:8081/mcp (published on $(CONTAINER_HOST))"
	@echo "Dashboard (local client): http://localhost:18080 (published on $(CONTAINER_HOST))"
	@if [ "$(CONTAINER_HOST)" != "127.0.0.1" ] && [ "$(CONTAINER_HOST)" != "localhost" ]; then \
		echo "Remote clients: replace localhost in these URLs/config with this host's reachable DNS name or IP."; \
		echo "WARNING: MCP and dashboard have no authentication; remote access must be protected by firewall rules and an authenticated proxy."; \
	fi
	@echo ""
	@echo "Claude Code .mcp.json (for a client on this host):"
	@echo '  {"mcpServers": {"jira-cli": {"type": "http", "url": "http://localhost:8081/mcp"}}}'

container-run-bitwarden: container-build  ## Run container with JIRA token fetched from Bitwarden Secrets Manager
	@scripts/container-run-bitwarden.sh "$(BWS_ENV_FILE)"

container-stop:  ## Stop and remove the MCP container
	podman stop $(CONTAINER) 2>/dev/null || true
	podman rm $(CONTAINER) 2>/dev/null || true

container-scan: container  ## Scan container image for CVEs (requires trivy)
	@which trivy >/dev/null 2>&1 || { echo "trivy not installed: brew install trivy"; exit 1; }
	trivy image --severity HIGH,CRITICAL $(IMAGE)

container-sign: container  ## Sign container image (requires cosign)
	@which cosign >/dev/null 2>&1 || { echo "cosign not installed: brew install cosign"; exit 1; }
	cosign sign --yes localhost/$(IMAGE):latest

container-sbom: container  ## Generate SBOM for container image (requires syft)
	@which syft >/dev/null 2>&1 || { echo "syft not installed: brew install syft"; exit 1; }
	@mkdir -p dist
	syft localhost/$(IMAGE):latest -o spdx-json=dist/sbom.spdx.json
	@echo "SBOM written to dist/sbom.spdx.json"

help:  ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | \
		awk -F ':.*## ' '{printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
