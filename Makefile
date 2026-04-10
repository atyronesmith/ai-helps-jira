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

.PHONY: all build clean install uninstall test lint fmt vet tidy run \
        release check restart-mcp container container-run container-stop help

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

container-run: container  ## Run MCP server in container (SSE on :8081, dashboard on :18080)
	podman run -d --name $(CONTAINER) \
		-p 8081:8081 -p 18080:18080 \
		-v jira-cli-cache:/root/.jira-cli:Z \
		--env-file .env \
		$(IMAGE)
	@echo "MCP SSE:     http://localhost:8081/sse"
	@echo "Dashboard:   http://localhost:18080"
	@echo ""
	@echo "Claude Code .mcp.json:"
	@echo '  {"mcpServers": {"jira-cli": {"type": "sse", "url": "http://localhost:8081/sse"}}}'

container-stop:  ## Stop and remove the MCP container
	podman stop $(CONTAINER) 2>/dev/null || true
	podman rm $(CONTAINER) 2>/dev/null || true

help:  ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | \
		awk -F ':.*## ' '{printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
