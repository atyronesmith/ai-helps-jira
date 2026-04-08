# Contributing

Thanks for your interest in contributing to jira-cli!

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone <your-fork-url>`
3. Copy `.env.example` to `.env` and fill in your JIRA credentials
4. Run `make check` to verify your setup

## Development

```sh
make build          # Build the binary
make check          # Run tidy + fmt + vet
make test           # Run tests
make lint           # Run vet + staticcheck
make restart-mcp    # Rebuild and restart MCP server (for testing MCP tools)
```

## Making Changes

1. Create a branch: `git checkout -b my-feature`
2. Make your changes
3. Run `make check` and `make test`
4. Commit with a clear message describing the change
5. Push and open a pull request

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep functions focused and small
- Use `slog` for structured logging (logs go to `~/.jira-cli/jira-cli.log`)
- Credentials must come from environment variables, never hardcoded
- Don't commit `.env`, `*.db`, or generated output files

## Project Structure

- `cmd/` — CLI command definitions (cobra)
- `internal/jira/` — JIRA REST API client
- `internal/confluence/` — Confluence REST API client
- `internal/llm/` — LLM integration (Claude via Vertex AI)
- `internal/cache/` — SQLite caching layer
- `internal/mcpserver/` — MCP server, tool handlers, web dashboard
- `internal/format/` — Terminal and markdown output formatting
- `internal/web/templates/` — HTML templates for the web dashboard

## Adding a New MCP Tool

1. Add the tool definition function in `internal/mcpserver/tools.go` (or `crud_tools.go` for CRUD ops)
2. Add the handler method on `*Handlers`
3. Register the tool in `internal/mcpserver/server.go`
4. If the tool has a web view, add a template in `internal/web/templates/` and register it in `webserver.go`

## Reporting Issues

Open an issue with:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Relevant log output from `~/.jira-cli/jira-cli.log`
