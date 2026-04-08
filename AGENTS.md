# Agents

Instructions for AI coding agents working on this repository.

## Build & Test

```sh
make build          # Build the jira-cli binary
make check          # Run go mod tidy + gofmt + go vet
make test           # Run all tests
go build ./...      # Quick compile check
```

Always run `go build ./...` after making changes to verify compilation.

## Project Overview

Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI digest reports, ticket enrichment, weekly status reports, full CRUD operations, and Confluence integration. All features are exposed both as CLI commands and MCP tools.

## Architecture

- **`cmd/`** — CLI entry points using cobra. Each file is one subcommand.
- **`internal/jira/`** — JIRA REST API v3 client. `client.go` has the HTTP client and read operations. `crud.go` has write operations (create, edit, transition, comment, link). `models.go` has all data types.
- **`internal/confluence/`** — Confluence Cloud REST API client. Uses both v1 (for space keys) and v2 (for page CRUD) APIs.
- **`internal/llm/`** — LLM integration via Claude on Vertex AI. Each file handles one feature (JQL generation, digest, weekly status, enrichment, epic creation).
- **`internal/cache/`** — SQLite cache at `~/.jira-cli/cache.db`. Schema migrations are versioned (v1-v5). Stores issues, comments, links, digest run history, and weekly status results.
- **`internal/mcpserver/`** — MCP server using mcp-go. `tools.go` has core tool handlers. `crud_tools.go` has CRUD tool handlers. `server.go` registers all tools. `store.go` is the in-memory result store. `webserver.go` serves the HTML dashboard on port 18080.
- **`internal/config/`** — Configuration from `.env` file. `Load()` requires LLM env vars. `LoadJIRAOnly()` only requires JIRA credentials.
- **`internal/format/`** — Terminal output (pterm) and markdown formatting.
- **`internal/web/templates/`** — HTML templates for the web dashboard (Chart.js, Mermaid diagrams).

## Key Patterns

- **Tool handlers** follow a consistent pattern: parse args → load config → create client → call API → store result → return text. See any handler in `tools.go` for the template.
- **Config loading**: Use `loadConfig(req)` for tools needing LLM, `loadJIRAConfig(req)` for JIRA-only tools.
- **Caching**: The weekly status tool caches full LLM results in SQLite and compares JIRA `updated` timestamps to skip regeneration when nothing changed. The digest tool uses `digest_log` to track last-run times.
- **All credentials** come from environment variables via `.env`. Never hardcode tokens, URLs, or user identifiers.
- **MCP tool registration**: Define the tool in a `*ToolDef()` function, write the handler as a method on `*Handlers`, register with `s.AddTool()` in `server.go`.

## Sensitive Data

- Never hardcode organization-specific JIRA URLs, usernames, project keys, or page IDs
- Use generic placeholders in examples and help text (e.g. `yourcompany.atlassian.net`, `PROJ-123`, `jsmith`)
- The `.env` file, `*.db` files, and `.mcp.json` are in `.gitignore` — never commit them
- Gitleaks runs on every push via GitHub Actions

## Adding Features

When adding a new MCP tool:
1. Define the tool schema in `tools.go` or `crud_tools.go`
2. Add the handler method on `*Handlers`
3. Register in `server.go` with `s.AddTool()`
4. If it needs a web view: add a result type in `store.go`, a data struct in `results.go`, and a template in `web/templates/`

When adding a new CLI command:
1. Create a new file in `cmd/`
2. Register with `rootCmd.AddCommand()` in its `init()` function
3. Follow the existing pattern: load config, setup logging, create client, do work, format output
