# Agents

Instructions for AI coding agents working on this repository.

## Build & Test

```sh
make build          # Build the jira-cli binary
make check          # Run tidy + gofmt + vet (modifies files)
make test           # Run all tests with race detection
make doc-lint       # Check documentation package/target coverage
go build ./...      # Quick compile check
make container      # Build container image (podman)
make container-run  # Rebuild and run Streamable HTTP container + dashboard
make container-run-bitwarden # Rebuild and run with a Bitwarden-backed Jira token
make container-stop # Stop and remove container
```

- For code changes, run relevant behavioral regressions and `go build ./...`.
  Run `go vet ./...` and the full race-enabled suite for integrated changes.
- For documentation-only changes, run `make doc-lint` and `git diff --check`;
  building and testing unchanged Go code is not normally necessary.
- `make check` rewrites formatting and module files. Do not use it as a
  read-only review command, and preserve unrelated working-tree changes.
- JQL generation changes need the generated-query corpus and the strict Jira
  oracle when credentials are available: `go test -tags=oracle ./...`. Assert
  on `errors[]` and parsed shape, not only HTTP status. Report an unavailable or
  skipped oracle separately from a passing one; do not discover credentials to
  enable it.
- Tests should verify intended behavior, including negative and ambiguous cases.
  A green test that expects an incorrect translation is not evidence of correctness.

## Delegation

- Delegate implementation that Luna 6 can reliably handle to `gpt-6-luna` using
  `model: "gpt-6-luna"` and `fork_turns: "none"`. Favor bounded tasks with clear
  requirements, established patterns, and verifiable acceptance tests.
- Keep tasks requiring deeper reasoning, careful judgment, or unresolved design
  decisions with the root agent. Examples include ambiguous query semantics,
  architectural tradeoffs, subtle concurrency, security-sensitive design, and
  difficult root-cause analysis. Do not delegate these merely to save tokens.
- The root may delegate a clearly specified implementation portion after resolving
  the difficult decisions. Give each agent relevant files, scope, constraints, and
  required tests. Reassess and take work back if it proves more complex than expected.
- Parallelize only independent work with non-overlapping file ownership. The root
  owns investigation, decomposition, review, integration, conflict resolution,
  final verification, and user communication.
- Inspect each agent's diff and run the required build/tests before reporting
  completion. Keep trivial edits, read-only work, documentation, and integration
  with the root when delegation overhead would exceed the benefit.
- If Luna or collaboration tools are unavailable, continue with the current agent
  and briefly disclose the fallback. Spawned agents implement their assignment
  themselves; they do not recursively delegate because of this policy.

## Project Overview

Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI digest reports, ticket enrichment, weekly status reports, comment summarization, backlog health checks, duplicate detection, CRUD operations, and Confluence integration. CLI and MCP coverage differs; consult `cmd/` and tool registration in `internal/mcpserver/server.go` rather than assuming feature parity or a fixed tool count. Supports stdio, legacy SSE, and Streamable HTTP transports; containers default to Streamable HTTP. Confluence tools support `body_file` for large content.

## Architecture

- **`cmd/`** — CLI entry points using cobra. Each file is one subcommand.
- **`internal/jira/`** — JIRA REST API v3 client. `client.go` has HTTP/read operations, `crud.go` has writes, and `models.go` has data types. Search calls through `searchIssues` and `SearchJQL` converge on `SearchJQLResult` (`search.go`), which validates JQL and follows enhanced-search tokens with caller cancellation and a bounded execution policy. `SearchInfo` distinguishes preview rows, completeness, and exact/reported/unknown totals; the old integer-total API has been removed. The board-scoped Agile path in `getKanbanIssues` has a separate structural guard without a default-project requirement. Verify both boundaries when changing validation.
- **`internal/jql/`** — Structural JQL validator and quote helpers. `Validate()` checks balanced quotes/parens, a single terminal `ORDER BY`, no dangling operators, and the scope invariant; `Quote()` and `QuoteIdent()` are the canonical quoting helpers for interpolated values. Tracked by the JQL hardening epic in `docs/jql-validation-plan.md`.
- **`internal/jqloracle/`** — Shared helper for build-tagged oracle tests that POST generated JQL to Jira's `/rest/api/3/jql/parse?validation=strict`. The default suite remains hermetic. The live oracle runs through a separate default-branch manual workflow; ordinary CI runs only its local protocol tests.
- **`internal/nl2jql/`** — Deterministic-first natural-language-to-JQL pipeline: `Normalizer` (date + fuzzy-name repair) → strict `participle` AST parse → LLM `FallbackEngine` used only when parsing fails. `engine.go` orchestrates; `parser.go`, `normalizer.go`, `fallback.go` are the stages.
- **`internal/confluence/`** — Confluence Cloud REST API client. Uses both v1 (for space keys) and v2 (for page CRUD) APIs.
- **`internal/llm/`** — LLM integration via the `Provider` interface defined in `provider.go`. `NewProvider(cfg)` is the factory; all feature files (`llm.go`, `query.go`, `weekly.go`, `enrich.go`, `digest.go`, `comments.go`, `health.go`) call `NewProvider()` instead of using the Anthropic SDK directly. The SDK import is isolated to `provider.go` only. `health.go` has rule-based checks (pure Go, no LLM) plus an optional LLM executive summary. `health_test.go` and `provider_test.go` have unit tests.
- **`internal/cache/`** — SQLite caches at `~/.jira-cli/scopes/<hash>.db`, isolated by normalized Jira site and authenticated account, with transactional migrations through v8. The legacy `cache.db` is never adopted as scoped data. Summary membership is keyed separately by effective user and requested project, and full reconciliations replace membership and advance checkpoints atomically. Operation snapshots fence writes across mutations. Stores issues, board memberships, comments, links, digest history, weekly reports, issue details (v6), and project metadata (v7). Detail-cache methods include `UpsertIssueDetail`, `GetIssueDetail(key, knownUpdated)`, and `GetFreshDetailKeys`. `fetcher.go` provides testable cache-aware fetching.
- **`internal/nlfeedback/`** — Versioned private JSONL feedback records for the `train-jql` CLI command in `cmd/train_jql.go`. The trainer and `query --dry-run` use the production translator without executing searches. Corrections are review data, not executable instructions.
- **`internal/mcpserver/`** — MCP server using mcp-go. `tools.go` has core/AI/Confluence handlers; `crud_tools.go` has Jira CRUD handlers. `server.go` registers tools and supports stdio, SSE, and Streamable HTTP. `store.go` currently combines in-memory results with per-process SQLite databases; do not assume durable history across restarts. `results.go` defines view data. `webserver.go` serves the dashboard and `/healthz`, which is currently a liveness endpoint rather than dependency readiness.
- **`internal/config/`** — Configuration from environment, `.env`, and mounted secrets. `Load()` and `LoadJIRAOnly()` require Jira credentials and a default project unless overridden; LLM settings are checked lazily by `llm.NewProvider`. `LoadTranslation()` requires a default project but not Jira credentials.
- **`internal/format/`** — Terminal output (pterm), markdown, and multi-format rendering. `weekly.go` provides `RenderWeeklyStatus` and `DisplayWeeklyStatus`. `markdown.go` has `RenderSummary`/`RenderDigest` (return strings) and `WriteSummary`/`WriteDigest` (write to file). All support 4 output formats: markdown, slack, text, pretty.
- **`internal/web/templates/`** — HTML templates for the web dashboard (Chart.js, Mermaid diagrams).

## Key Patterns

- **Tool handlers** follow a consistent pattern: parse args → load config → create client → call API → store result → return text. See any handler in `tools.go` for the template.
- **Config loading**: Existing MCP wrappers are `loadConfig(req)` and `loadJIRAConfig(req)`. Neither eagerly constructs an LLM provider. CLI translation-only commands use `config.LoadTranslation()` and cached project metadata. Preserve that credential-free, no-search path.
- **Caching**: The weekly status tool requires a complete current search before reusing an LLM result, keyed by site/account/effective user/project/date range and a sorted issue-input fingerprint that includes membership. The digest tool uses `digest_log` to track last-run times. Per-issue detail caching (`issue_details` table) is used by `cmd/get_issue.go`, `cmd/weekly_status.go`, and MCP handlers to avoid redundant JIRA API calls.
- **Configuration precedence**: Explicit user/project arguments override defaults. `godotenv.Load()` fills missing environment values without replacing existing ones. Nonempty environment values (including values loaded from `.env`) take precedence over `/run/secrets/<NAME>`, then `/var/run/secrets/<NAME>`. Provider selection uses the same resolver. Never hardcode credentials, tenant URLs, or user identifiers.
- **MCP tool registration**: Define the tool in a `*ToolDef()` function, write the handler as a method on `*Handlers`, register with `s.AddTool()` in `server.go`.

## Correctness Requirements

Apply these requirements to new and changed behavior. Known gaps in existing
paths are tracked in the implementation plan below; this section does not claim
that every path already satisfies them.

- Preserve Boolean intent, ownership, date meaning, and ordering. Reject or
  clarify ambiguity instead of silently dropping meaningful words or constraints.
- Distinguish structural/tenant validation from semantic correctness. Valid JQL
  can still ask the wrong question; assert intended meaning in regression tests.
- Use consistent open-work and effective-user semantics across CLI, MCP, search,
  and offline cache reads. Account for configured user overrides explicitly.
- Distinguish returned rows, incomplete results, unknown totals, estimates, and
  verified exact counts. A partial page length is not a matching total.
- Preserve original natural-language input, exact executed JQL, and separate
  display transformations so diagnostics do not misrepresent what was executed.
- Keep cache membership isolated by site, effective identity, and project where
  those affect scope. Store an issue's actual project, not merely the default.
- Reconcile removals from cached result sets after closure, reassignment, moves,
  or visibility changes. An open-only delta cannot observe all removals.
- Advance synchronization checkpoints only after successful retrieval/storage.
  Preserve mutation invalidation; a cache failure must not turn an already
  completed remote write into an apparent failure.

## Jira MCP Usage

- For Jira searches, call `jira_query` and pass the user's exact original wording in `natural_query`.
- Do not pretranslate the request to JQL, replace `my`/`me` with a username or email, or call `jira_lookup_user` first. The server owns deterministic translation and its configured LLM fallback.
- If a Jira MCP tool returns an error, report it. Do not bypass MCP with `curl`, direct Jira REST calls, local CLI commands, or credential discovery unless the user explicitly requests that fallback.
- These rules govern Jira business operations. Explicitly requested development
  diagnostics and local mock-based tests remain permitted within their scope;
  they do not authorize production writes or credential discovery. The server
  itself translates to and executes JQL internally.
- For recommendations, retrieve candidates with `jira_query`, inspect their full
  content with `jira_get_issue`, and let the host assistant reason over the data.
  If a shortlist is necessary, disclose it and any result truncation.

## Global Flags

- `--format` / `-f` — Output format. Supported values: `markdown`, `slack`, `text`, `pretty`.
- `-v` / `-vv` — Verbosity levels. `-vv` enables cache diagnostics.

## Sensitive Data

- Never hardcode organization-specific JIRA URLs, usernames, project keys, or page IDs
- Use generic placeholders in examples and help text (e.g. `yourcompany.atlassian.net`, `PROJ-123`, `jsmith`)
- Never print or commit Jira API tokens, `LLM_API_KEY`, Bitwarden machine access
  tokens, Keychain values, mounted secret contents, or credential-bearing config.
- The Bitwarden launcher retrieves its machine token from macOS Keychain on the
  host, fetches the Jira token with `bws`, and mounts a Podman secret. Keep machine
  credentials out of the container. Its temporary env file omits `JIRA_API_TOKEN`
  because environment values otherwise take precedence over mounted secrets.
- `.env`, `*.db`, and `.mcp.json` are ignored; do not commit them. Treat the feedback
  corpus at `~/.jira-cli/nl2jql-feedback.jsonl` as private, including when a custom
  output path is used. Commit only deliberately sanitized regression fixtures.
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

For multiple trusted clients sharing the configured Jira account, the container
defaults to Streamable HTTP. Multiple clients are not independently authenticated
Jira users. Both host ports publish on loopback by default; protect remote access
with an authenticated proxy and firewall restrictions before changing that.

```sh
make container-run   # Stop old → rebuild (--no-cache) → start new container
make container-run-bitwarden # Rebuild, fetch token on host, then replace/start
make container-stop  # Stop and remove the container
```

**`.mcp.json` for Streamable HTTP clients:**
```json
{
  "mcpServers": {
    "jira": {
      "type": "http",
      "url": "http://localhost:8081/mcp"
    }
  }
}
```

- Legacy SSE is available only when the server is explicitly started with
  `--transport sse`; those clients use `type: "sse"` and `/sse`. Do not configure
  `/sse` against the default Streamable HTTP container.
- Do not add `"oauth": {}` to these server configurations. The server authenticates
  to Atlassian with its configured Jira token; it does not provide client OAuth.
- After rebuilding/replacing a server, reconnect the client to refresh discovery
  (for example, `/mcp` in clients supporting that command). `make container-run`
  builds local source; it does not pull a prebuilt application image.

## Implementation Plans

- [Project reliability plan](docs/project-reliability-implementation-plan.md) —
  phases, acceptance criteria, known correctness gaps, and Phase 0 regression-gate
  results. Phase 1 implements complete query searches/count metadata; Phase 2 implements scoped caches, atomic summary reconciliation, mutation fences, and weekly fingerprints. Phases 3–7 remain planned.
- [JQL AST handoff](docs/FIELDCTO-2813-implementation-plan.md) — deferred shared
  JQL parser/renderer work, separate from the existing natural-language AST.
- [JQL metadata analysis](docs/jql-metadata-resolver-analysis.md) — proposed cached
  tenant-field resolution, not an implemented field catalog.
- [JQL hardening history](docs/jql-validation-plan.md) and
  [feature roadmap](docs/features.md) — verify current symbols and implementation
  before relying on historical inventories. Update status only with evidence.
