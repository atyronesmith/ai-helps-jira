# Agents

Instructions for AI coding agents working on this repository.

## Build & Test

```sh
make build          # Build the jira-cli binary
make check          # Run go mod tidy + gofmt + go vet
make test           # Run all tests
go build ./...      # Quick compile check
make container      # Build container image (podman)
make container-run  # Run MCP container with SSE transport + dashboard
make container-stop # Stop and remove container
```

Always run `go build ./...` after making changes to verify compilation.

## Project Overview

Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI digest reports, ticket enrichment, weekly status reports, comment summarization, backlog health checks, duplicate detection, full CRUD operations, and Confluence integration (page read/update, search, analytics, comments, labels). All features are exposed both as CLI commands and 25 MCP tools. Supports stdio and SSE transports, with container deployment for shared multi-client access.

## Architecture

- **`cmd/`** — CLI entry points using cobra. Each file is one subcommand.
- **`internal/jira/`** — JIRA REST API v3 client. `client.go` has the HTTP client and read operations. `crud.go` has write operations (create, edit, transition, comment, link). `models.go` has all data types.
- **`internal/confluence/`** — Confluence Cloud REST API client. Uses both v1 (for space keys) and v2 (for page CRUD) APIs.
- **`internal/llm/`** — LLM integration via the `Provider` interface defined in `provider.go`. `NewProvider(cfg)` is the factory; all feature files (`llm.go`, `query.go`, `weekly.go`, `enrich.go`, `digest.go`, `comments.go`, `health.go`) call `NewProvider()` instead of using the Anthropic SDK directly. The SDK import is isolated to `provider.go` only. `health.go` has rule-based checks (pure Go, no LLM) plus an optional LLM executive summary. `health_test.go` and `provider_test.go` have unit tests.
- **`internal/cache/`** — SQLite cache at `~/.jira-cli/cache.db`. Schema migrations are versioned (v1-v6). Stores issues, comments, links, digest run history, weekly status results, and per-issue detail structs (`issue_details` table, added in v6). Key detail-cache methods: `UpsertIssueDetail`, `GetIssueDetail(key, knownUpdated)`, `GetFreshDetailKeys(updatedByKey)`. `fetcher.go` defines the `Fetcher` interface for testable cache-aware logic. `cache_test.go` and `fetcher_test.go` have comprehensive tests using in-memory SQLite.
- **`internal/mcpserver/`** — MCP server using mcp-go. `tools.go` has core tool handlers (summary, query, digest, enrich, weekly, health, comments, find-similar, and all Confluence tools). `crud_tools.go` has CRUD tool handlers. `server.go` registers all 25 tools, supports stdio and SSE transports via `Config.Transport`. `store.go` is the in-memory result store with SQLite persistence. `results.go` has per-tool result data structs. `webserver.go` serves the HTML dashboard and `/healthz` health check endpoint.
- **`internal/config/`** — Configuration from `.env` file. `Load()` requires LLM env vars. `LoadJIRAOnly()` only requires JIRA credentials.
- **`internal/format/`** — Terminal output (pterm), markdown, and multi-format rendering. `weekly.go` provides `RenderWeeklyStatus` and `DisplayWeeklyStatus`. `markdown.go` has `RenderSummary`/`RenderDigest` (return strings) and `WriteSummary`/`WriteDigest` (write to file). All support 4 output formats: markdown, slack, text, pretty.
- **`internal/web/templates/`** — HTML templates for the web dashboard (Chart.js, Mermaid diagrams).

## Key Patterns

- **Tool handlers** follow a consistent pattern: parse args → load config → create client → call API → store result → return text. See any handler in `tools.go` for the template.
- **Config loading**: Use `loadConfig(req)` for tools needing LLM, `loadJIRAConfig(req)` for JIRA-only tools.
- **Caching**: The weekly status tool caches full LLM results in SQLite and compares JIRA `updated` timestamps to skip regeneration when nothing changed. The digest tool uses `digest_log` to track last-run times. Per-issue detail caching (`issue_details` table) is used by `cmd/get_issue.go`, `cmd/weekly_status.go`, and MCP handlers to avoid redundant JIRA API calls.
- **All credentials** come from environment variables via `.env`. Never hardcode tokens, URLs, or user identifiers.
- **MCP tool registration**: Define the tool in a `*ToolDef()` function, write the handler as a method on `*Handlers`, register with `s.AddTool()` in `server.go`.

## Global Flags

- `--format` / `-f` — Output format. Supported values: `markdown`, `slack`, `text`, `pretty`.
- `-v` / `-vv` — Verbosity levels. `-vv` enables cache diagnostics.

## Sensitive Data

- Never hardcode organization-specific JIRA URLs, usernames, project keys, or page IDs
- Use generic placeholders in examples and help text (e.g. `yourcompany.atlassian.net`, `PROJ-123`, `jsmith`)
- `LLM_API_KEY` is loaded from the environment (via `.env`). Never commit it.
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

## Container Deployment

The MCP server runs as a container for shared multi-client SSE access. All Claude Code sessions connect to the same instance.

```sh
make container-run   # Stop old → rebuild (--no-cache) → start new container
make container-stop  # Stop and remove the container
```

**`.mcp.json` for client projects** (use SSE, not the local binary):
```json
{
  "mcpServers": {
    "jira-cli": {
      "type": "sse",
      "url": "http://localhost:8081/sse"
    }
  }
}
```

- Do **not** include `"oauth": {}` — it triggers Claude Code to attempt OAuth authentication against the server. The server authenticates to Atlassian using the API token from `.env`, not client-side OAuth.
- Do **not** point `.mcp.json` at the local binary (`"command": ".../jira-cli"`) — it will be stale after container rebuilds and miss new tools.
- After `make container-run`, clients must `/mcp` reconnect to discover new tools.
- The local binary (`jira-cli` in project root) is a build artifact only — it is in `.gitignore` and `.containerignore`. Do not rely on it for MCP.
