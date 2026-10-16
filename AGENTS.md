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

## Delegation

- For coding tasks with substantive implementation work, proactively delegate the
  implementation to `gpt-6-luna` subagents using `fork_turns: "none"` so they
  receive only the bounded task context they need.
- Split work into non-overlapping file or component ownership when parallelism is
  useful. The root agent remains responsible for planning, reviewing and
  integrating the changes, resolving conflicts, and running the final required
  tests and `go build ./...`.
- Keep trivial edits and tightly ordered work in the root agent when delegation
  overhead would exceed the work itself.

## Project Overview

Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI digest reports, ticket enrichment, weekly status reports, comment summarization, backlog health checks, duplicate detection, full CRUD operations, and Confluence integration (page/blog CRUD, search, analytics, comments, labels). All features are exposed both as CLI commands and 35 MCP tools. Supports stdio and SSE transports, with container deployment for shared multi-client access. Confluence tools support `body_file` parameter for large content that would exceed LLM context limits.

## Architecture

- **`cmd/`** — CLI entry points using cobra. Each file is one subcommand.
- **`internal/jira/`** — JIRA REST API v3 client. `client.go` has the HTTP client and read operations. `crud.go` has write operations (create, edit, transition, comment, link). `models.go` has all data types.
- **`internal/jira/`** — JIRA REST API v3 client. `client.go` has the HTTP client and read operations. `crud.go` has write operations (create, edit, transition, comment, link). `models.go` has all data types. The JQL chokepoint `Client.searchIssues` (unexported) runs `jql.Validate` on every generated query — hand-built and LLM-generated alike — before it is sent, so the scope invariant is enforced in one place.
- **`internal/jql/`** — Structural JQL validator and quote helpers. `Validate()` checks balanced quotes/parens, a single terminal `ORDER BY`, no dangling operators, and the scope invariant; `Quote()` and `QuoteIdent()` are the canonical quoting helpers for interpolated values. Tracked by the JQL hardening epic in `docs/jql-validation-plan.md`.
- **`internal/jqloracle/`** — CI oracle that POSTs generated JQL to Jira's `/rest/api/3/jql/parse?validation=strict` (behind a build tag so `go test ./...` stays hermetic), asserting on `errors[]` and parsed shape.
- **`internal/nl2jql/`** — Deterministic-first natural-language-to-JQL pipeline: `Normalizer` (date + fuzzy-name repair) → strict `participle` AST parse → LLM `FallbackEngine` used only when parsing fails. `engine.go` orchestrates; `parser.go`, `normalizer.go`, `fallback.go` are the stages.
- **`internal/confluence/`** — Confluence Cloud REST API client. Uses both v1 (for space keys) and v2 (for page CRUD) APIs.
- **`internal/llm/`** — LLM integration via the `Provider` interface defined in `provider.go`. `NewProvider(cfg)` is the factory; all feature files (`llm.go`, `query.go`, `weekly.go`, `enrich.go`, `digest.go`, `comments.go`, `health.go`) call `NewProvider()` instead of using the Anthropic SDK directly. The SDK import is isolated to `provider.go` only. `health.go` has rule-based checks (pure Go, no LLM) plus an optional LLM executive summary. `health_test.go` and `provider_test.go` have unit tests.
- **`internal/cache/`** — SQLite cache at `~/.jira-cli/cache.db`. Schema migrations are versioned (v1-v6). Stores issues, comments, links, digest run history, weekly status results, and per-issue detail structs (`issue_details` table, added in v6). Key detail-cache methods: `UpsertIssueDetail`, `GetIssueDetail(key, knownUpdated)`, `GetFreshDetailKeys(updatedByKey)`. `fetcher.go` defines the `Fetcher` interface for testable cache-aware logic. `cache_test.go` and `fetcher_test.go` have comprehensive tests using in-memory SQLite.
- **`internal/nlfeedback/`** — Private JSONL translation reviews for `train-jql`; corrections are review data and are never executed.
- **`internal/mcpserver/`** — MCP server using mcp-go. `tools.go` has core tool handlers (summary, query, digest, enrich, weekly, health, comments, find-similar, and all Confluence tools including create page/blog with `body_file` support). `crud_tools.go` has CRUD tool handlers. `server.go` registers all 35 tools, supports stdio and SSE transports via `Config.Transport`. `store.go` is the in-memory result store with SQLite persistence. `results.go` has per-tool result data structs. `webserver.go` serves the HTML dashboard and `/healthz` health check endpoint.
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

## MCP Setup

The recommended setup is a local binary with stdio transport:

```sh
make build
```

**`.mcp.json` for client projects:**
```json
{
  "mcpServers": {
    "jira": {
      "command": "/path/to/jira-cli",
      "args": ["mcp"]
    }
  }
}
```

- After rebuilding the binary, clients must `/mcp` reconnect to discover new tools.

### Container deployment (alternative)

For shared multi-client SSE access where multiple Claude Code sessions connect to the same instance:

```sh
make container-run   # Stop old → rebuild (--no-cache) → start new container
make container-stop  # Stop and remove the container
```

**`.mcp.json` for SSE clients:**
```json
{
  "mcpServers": {
    "jira": {
      "type": "sse",
      "url": "http://localhost:8081/sse"
    }
  }
}
```

- Do **not** include `"oauth": {}` when connecting to a server you control — it triggers Claude Code to attempt OAuth authentication. The server authenticates to Atlassian using the API token from `.env`.
- After `make container-run`, clients must `/mcp` reconnect to discover new tools.
