# Build stage
FROM golang:1.25 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w \
      -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
      -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo none) \
      -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /jira-cli .

# Runtime stage — Red Hat UBI minimal (pinned)
FROM registry.access.redhat.com/ubi9/ubi-minimal:9.5

LABEL name="jira-cli" \
      version="1.0.0" \
      summary="JIRA CLI and MCP server with AI features" \
      description="Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, digest reports, ticket enrichment, and full CRUD — with SQLite caching and web dashboard." \
      maintainer="atyronesmith" \
      url="https://github.com/atyronesmith/ai-helps-jira" \
      license="MIT" \
      io.k8s.display-name="jira-cli MCP Server" \
      io.k8s.description="JIRA Cloud MCP server with SSE transport"

RUN microdnf install -y tzdata ca-certificates curl-minimal && \
    microdnf clean all && \
    find / -perm /6000 -type f -exec chmod a-s {} + 2>/dev/null || true

# Non-root user (UID 1001, root group for OpenShift compatibility)
RUN useradd -r -u 1001 -g 0 -d /home/jira-cli -m jira-cli && \
    mkdir -p /home/jira-cli/.jira-cli && \
    chown -R 1001:0 /home/jira-cli

COPY --from=builder /jira-cli /usr/local/bin/jira-cli

# Cache persistence
VOLUME /home/jira-cli/.jira-cli

USER 1001
ENV HOME=/home/jira-cli

# MCP SSE port + web dashboard port
EXPOSE 8081 18080

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD curl -sf http://localhost:18080/healthz || exit 1

ENTRYPOINT ["jira-cli"]
CMD ["mcp", "--transport", "sse", "--sse-port", "8081", "--port", "18080", "--bind", "0.0.0.0"]
