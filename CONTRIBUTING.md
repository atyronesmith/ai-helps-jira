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

## Regression Gates (Phase 0)

Every pull request and push to `main` runs `Build & Vet`, `Doc Lint`, and
`Race Tests` in `.github/workflows/build.yml`. Keep all three checks required in
branch protection. Repository administrators configure that policy in GitHub;
the workflow file defines the checks but cannot enforce branch protection itself.

Run the same checks locally without Jira credentials or a running model:

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
go test -race -tags=oracle -count=1 ./internal/jqloracle
python3 -m unittest discover -s scripts -p 'test_run_jql_oracle.py' -v
make doc-lint
git diff --check
```

The tagged `internal/jqloracle` package tests use local HTTP fixtures only. The
ordinary suite checks production behavior with mock providers and servers. Define
new regressions by the requested behavior, including negative and ambiguous
inputs; syntactically valid JQL can still express the wrong intent. Do not bless
an incorrect translation to make CI pass. The two ambiguous status conjunctions
in `TestParseAmbiguousStatusIntent_KnownGap` are explicitly skipped until Phase 3
implements conservative rejection. They are known gaps, not passing coverage.

## Running the JQL Oracle Suite

The build-tagged corpus in `internal/llm/jqltranslate_oracle_test.go` and
`internal/jira/jql_oracle_test.go` checks fast-path, scope, weekly-activity, and
hand-built query shapes against Jira's
`POST /rest/api/3/jql/parse?validation=strict`. This parser checks grammar and
tenant vocabulary; it does not establish agreement with the user's intent or
prove that the corpus covers every generator. Hand-built fixtures reconstructed
from other packages need to stay aligned with their source call sites.

The default suite never runs the live corpus. To run it, explicitly export:

- `JIRA_SERVER`, `JIRA_EMAIL`, `JIRA_API_TOKEN`: the tenant URL and credentials.
- `JIRA_PROJECT`: a real project key visible to that account.
- `JIRA_ORACLE_ISSUE_KEY`: a real visible issue key for the identity-lookup case.
- `JIRA_ORACLE_SPRINT_ID`: a real visible sprint ID.
- `JIRA_ORACLE_RELEASE`: a real release name in the configured project.

Fixtures must also support the corpus's named Jira issue types, priorities, and
statuses (including Bug, Story, Open, Closed, Done, and In Progress). A tenant
vocabulary mismatch fails validation; it must not be relabeled a passing check.
The corpus only calls the strict-parse endpoint. It does not create fixture data,
search issues, or initialize a live model.

```sh
python3 scripts/run-jql-oracle.py
```

The runner does not load `.env` or discover credentials. It runs
`go test -json -tags=oracle -count=1 ./...` and reports `PASS`, `FAIL`, or
`SKIPPED` with oracle case counts. Missing explicit configuration is `SKIPPED`
with a nonzero exit. Test failures, any skipped oracle case, malformed or
incomplete results, and zero passing leaf oracle cases cannot report `PASS`.
Raw `go test -tags=oracle ./...` can still succeed while live tests skip; that
exit status alone is not evidence that the oracle passed.

For GitHub Actions, create a protected environment named **jira-oracle** and restrict
its deployment branch policy to the repository's default branch. Store
`JIRA_SERVER`, `JIRA_EMAIL`, and `JIRA_API_TOKEN` as **environment secrets**, not
repository secrets, so other PR workflows cannot obtain them. Configure repository
**variables** for the four fixture settings. Environment reviewers can add an
additional approval requirement if the repository needs one.
Manually dispatch **JQL Jira Oracle** from the default branch. The workflow also rejects
other refs before checkout, checks out the dispatch's default-branch commit, and
supplies credentials only to the oracle execution step. It has no PR trigger and
never checks out a PR head. Missing fixture variables produce a `SKIPPED` summary;
missing secrets produce the runner's `SKIPPED` summary and a failed step. A green
workflow with skipped execution does not mean the oracle passed: inspect the
explicit step summary. Never add `pull_request_target` or execute untrusted PR
code with these credentials.

Notes for anyone extending this corpus:

- Positive cases must name real tenant data: strict parsing checks value existence.
- HTTP 200 can contain `errors[]`; positive assertions must check that array and
  require parsed structure. Scope and ordering regressions need assertions about
  that structure, not just acceptance by Jira.
- Keep negative syntax/vocabulary cases distinct from semantic checks. A valid
  but wrongly scoped OR query should fail the structural expectation.
- Report skipped or unavailable live validation separately from passing hermetic
  checks. Never commit credentials or private feedback records.

## Reporting Issues

Open an issue with:
- What you expected to happen
- What actually happened
- Steps to reproduce
- Relevant log output from `~/.jira-cli/jira-cli.log`
