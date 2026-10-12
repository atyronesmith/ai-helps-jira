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
make container      # Build container image
make container-run  # Run MCP container with SSE transport
make container-stop # Stop and remove container
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

## Configuration

`config.Load()` loads all settings (JIRA + LLM provider). It no longer requires
Vertex environment variables at load time — those are validated lazily by
`llm.NewProvider()` only when the vertex provider is actually used.

For commands that don't need an LLM, use `config.LoadJIRAOnly()` instead.

## LLM Provider Pattern

All LLM calls go through the provider abstraction in `internal/llm/`:

```go
provider := llm.NewProvider(cfg)
result, err := provider.Complete(ctx, systemPrompt, userPrompt, maxTokens)
```

Do not import the Anthropic SDK directly — it is isolated inside `provider.go`.

LLM responses sometimes arrive wrapped in markdown code fences. Always call
`cleanJSON()` on the response before passing it to `json.Unmarshal`.

## Per-Issue Caching

When fetching issue details, check the cache first:

```go
detail, err := db.GetIssueDetail(key, knownUpdated)
```

If the cache misses (or the issue has been updated), fetch from the API and
store the result:

```go
err = db.UpsertIssueDetail(detail)
```

The `IssueDetail` struct includes an `Updated` field used for freshness checks.

## Output Format Support

New commands should accept the `--format` flag (`markdown`, `slack`, `text`,
`pretty`). Use the helpers in `internal/format/`:

- `format.Render*` functions — produce markdown, slack, or plain-text strings.
- `format.Display*` functions — render pretty terminal output via pterm.

See `cmd/weekly_status.go` for a complete example of the pattern.

## MCP Handlers

MCP tool handlers live on the `*Handlers` struct in `internal/mcpserver/`.
Use the shared cache available as `h.cache` rather than calling `cache.Open()`
inside each handler.

## Project Structure

- `cmd/` — CLI command definitions (cobra)
- `internal/jira/` — JIRA REST API client
- `internal/confluence/` — Confluence REST API client
- `internal/llm/` — LLM integration (Vertex AI, OpenAI-compat, Ollama)
- `internal/cache/` — SQLite caching layer
- `internal/mcpserver/` — MCP server, tool handlers, web dashboard
- `internal/format/` — Terminal and markdown output formatting
- `internal/web/templates/` — HTML templates for the web dashboard

## Container Testing

The container runs the MCP server with SSE transport:

```sh
make container-run          # Build image + start container
curl localhost:18080/healthz  # Verify health
curl -N localhost:8081/sse    # Verify SSE endpoint
make container-stop         # Clean up
```

The container uses Red Hat UBI 9 minimal. Cache is persisted via a named volume.
Build uses `--format docker` for HEALTHCHECK support in podman.

## Adding a New MCP Tool

1. Add the tool definition function in `internal/mcpserver/tools.go` (or `crud_tools.go` for CRUD ops)
2. Add the handler method on `*Handlers`
3. Register the tool in `internal/mcpserver/server.go`
4. If the tool has a web view, add a template in `internal/web/templates/` and register it in `webserver.go`

## Running the JQL Oracle Suite

Every JQL shape this tool can generate -- the `nl2jql` fast path, `ensureProjectScope`,
`WeeklyActivityJQL`, and the hand-built `fmt.Sprintf` templates in `internal/jira/client.go`,
`cmd/find_similar.go`, and `internal/mcpserver/` -- has a corpus test that validates it against
Jira's own parser (`POST /rest/api/3/jql/parse?validation=strict`). This is the only check that can
catch a grammar violation or a hallucinated field/value, because only Jira knows its own grammar and
this tenant's vocabulary.

These tests live in `internal/llm/jqltranslate_oracle_test.go` and `internal/jira/jql_oracle_test.go`,
plus the shared client in `internal/jqloracle/`. They are gated behind the `oracle` build tag, so the
default `go build ./...` / `go vet ./...` / `go test ./...` never compiles or runs them -- the default
suite stays hermetic and requires no network access or credentials.

To run the oracle suite, you need a live Jira instance. Export credentials first (never commit `.env`
or print `JIRA_API_TOKEN`):

```sh
set -a && . ./.env && set +a
go test -tags=oracle ./...
```

Without `JIRA_SERVER` / `JIRA_EMAIL` / `JIRA_API_TOKEN` exported, each oracle test skips (not fails)
with a clear message, so `go test -tags=oracle ./...` is still safe to run locally without
credentials -- it just reports fewer tests run.

Notes for anyone extending this corpus:
- Positive (should-be-valid) cases must name real tenant data (a real project key, a real issue key)
  because `validation=strict` checks value existence, not just syntax -- a fictitious project name
  fails for the wrong reason and makes the corpus untrustworthy.
- The endpoint returns HTTP 200 whether a query is valid or not; always assert on the response's
  `errors[]` array, never the status code.
- Where Jira returns a parsed structure, assert on it too (e.g. top-level `where.operator` and
  whether a `project` clause sits at that top level) -- this is what actually catches the
  FIELDCTO-2807 class of bug, which is syntactically valid JQL with zero `errors[]`.

## Reporting Issues

Open an issue with:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Relevant log output from `~/.jira-cli/jira-cli.log`
