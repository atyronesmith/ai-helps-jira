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

# Runtime stage — Red Hat UBI minimal
# Minimal attack surface with microdnf available for debugging
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

RUN microdnf install -y tzdata ca-certificates curl-minimal && \
    microdnf clean all

COPY --from=builder /jira-cli /usr/local/bin/jira-cli

# Cache directory
RUN mkdir -p /root/.jira-cli

# Cache persistence
VOLUME /root/.jira-cli

# MCP SSE port + web dashboard port
EXPOSE 8081 18080

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD curl -sf http://localhost:18080/healthz || exit 1

ENTRYPOINT ["jira-cli"]
CMD ["mcp", "--transport", "sse", "--sse-port", "8081", "--port", "18080", "--bind", "0.0.0.0"]
