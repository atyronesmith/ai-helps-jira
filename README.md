# jira-cli

A Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI-powered digest reports, ticket enrichment, weekly status generation, and full CRUD operations — all with SQLite caching and a rich web dashboard.

Inspired by the [official Atlassian MCP server](https://github.com/atlassian/mcp-server-atlassian), but built from scratch in Go with SQLite caching to avoid JIRA Cloud API throttling on repeated queries. The cache isolates Jira sites and accounts, reconciles summary membership, and reuses unchanged issue details and weekly reports. Online summaries and weekly reports still search Jira to verify the current issue set.

## Features

- **Daily Summary** — Pull assigned issues from boards (Scrum sprints + Kanban) and display in terminal with color-coded status/priority. Outputs markdown (GitHub-flavored or Slack-compatible).
- **Natural Language Search** — Describe what you want in plain English; a deterministic parser translates it to JQL (an LLM handles complex queries it cannot), and the search runs.
- **Digest Reports** — Walk issue hierarchy (Initiative → Feature → Epic), fetch recent comments, and generate executive progress digests with AI.
- **Ticket Enrichment** — Fill in sparse tickets with AI-generated descriptions, acceptance criteria, labels, and priority suggestions.
- **Weekly Status** — Generate formatted weekly status reports from JIRA activity, optionally post to Confluence.
- **Comment Summarizer** — Summarize long comment threads into key decisions, action items, and open questions.
- **Backlog Health Check** — Rule-based analysis (stale tickets, missing descriptions, orphaned issues, unassigned active, missing labels) with optional LLM executive summary.
- **JIRA CRUD** — Create, edit, transition, comment on, link issues, look up users, and attach files — all from CLI or MCP.
- **MCP Server** — Exposes all 36 tools as Model Context Protocol tools for Claude Code and other MCP clients. Supports stdio and SSE transports. Includes a rich web dashboard.
- **Container Support** — Run as a shared MCP server in podman/docker with SSE transport. Multiple Claude Code instances can connect simultaneously. Red Hat UBI minimal base image with health checks and persistent cache volume.
- **Multi-LLM Provider Support** — Use any LLM backend: Vertex AI (Claude), OpenAI-compatible APIs (DeepInfra, vLLM, etc.), or Ollama for fully local inference.
- **SQLite Caching** — Delta fetches only changed issues after the first run. Smart cache invalidation compares JIRA timestamps. Per-issue detail caching means repeat runs skip re-fetching unchanged issues entirely.
- **Confluence Integration** — Post weekly status reports as child pages with automatic parent page indexing.
- **Cross-platform** — Single static binary, no CGO. Builds for Linux and macOS (amd64/arm64).

## Quick Start

### Option A: Build from source (recommended)

Requires Go 1.25+.

```sh
git clone https://github.com/atyronesmith/ai-helps-jira.git
cd ai-helps-jira
cp .env.example .env   # edit with your credentials
make build
```

Edit `.env` with your JIRA credentials (see [Creating a JIRA API Token](#creating-a-jira-api-token)):

```
JIRA_SERVER=https://yourcompany.atlassian.net
JIRA_EMAIL=your.email@company.com
JIRA_API_TOKEN=your-jira-api-token
JIRA_PROJECT=PROJ
```

Then add to your project's `.mcp.json`:

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

For AI features, also configure an [LLM provider](#llm-providers) in your `.env`. See `.env.example` for full documentation of all environment variables.

### Option B: Container image

For shared or remote setups where multiple clients connect simultaneously. No Go toolchain needed. See [docs/CONTAINER-SETUP.md](docs/CONTAINER-SETUP.md) for detailed platform-specific instructions.

```sh
# 1. Create a .env file with your credentials
cat > .env <<'EOF'
JIRA_SERVER=https://yourcompany.atlassian.net
JIRA_EMAIL=your.email@company.com
JIRA_API_TOKEN=your-jira-api-token
JIRA_PROJECT=PROJ
EOF

# 2. Run the MCP server
podman run -d --name jira-cli-mcp \
  -p 8081:8081 -p 18080:18080 \
  -v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
  --read-only --tmpfs /tmp \
  --cap-drop=ALL \
  --env-file .env \
  quay.io/aasmith/jira-cli:latest

# 3. Verify it's running
curl -sf http://localhost:18080/healthz
```

Then add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "jira": {
      "type": "sse",
      "url": "http://localhost:8081/sse",
      "oauth": {}
    }
  }
}
```

## Usage

### Summary

```sh
# First run: full fetch from JIRA, populates cache
jira-cli summary

# Subsequent runs: reconcile the complete current summary
jira-cli summary

# Explicit full reconciliation (the same policy as an ordinary online run)
jira-cli summary --refresh

# Use cached data only (no API calls)
jira-cli summary --cache-only

# Slack-compatible markdown output
jira-cli -f slack summary

# Plain text output
jira-cli -f text summary

# Custom output file
jira-cli -o weekly-report.md summary

# Override user/project
jira-cli -u user@company.com -p MYPROJ summary
```

### Natural Language Query

```sh
jira-cli query "show me all critical bugs from last week"
jira-cli query "unresolved stories assigned to me" --show-jql
jira-cli query "epics created this month" --max 10
jira-cli query --dry-run "issues I closed in June 2026"
# Count all matches, retaining at most 10 issue rows for display
jira-cli query --count "my open bugs" --max 10

# Interactively review translations and save a private local feedback corpus
jira-cli train-jql
jira-cli train-jql report
# Replay reviewed, sanitized fixtures without Jira credentials or a live model
jira-cli train-jql replay internal/nlfeedback/testdata/regressions.jsonl
```

Supported core-field queries and dry runs need no Jira credentials or model. Unsupported
queries use one typed LLM completion, compiled and validated locally, with a
45-second deadline and no automatic retries. Invalid output and provider errors
are terminal; the translator never switches prompts to retry the request.

Recognized ordering phrases accept an omitted direction and default to ascending.
For example, `my open issues ordered by date created` stays deterministic,
preserves the effective assignee and open-work filter, and orders oldest first.
Use `descending` or `desc` for newest first.

The trainer saves failures, skips, and corrections in a private local corpus.
Replay uses the production translator with a frozen clock, catalog, aliases,
and canned provider responses, and exits nonzero for regressions or missing
reviewed expectations. See [feedback and replay](docs/translation-feedback.md)
for the record format, privacy rules, and metric definitions.

Tenant fields require a cached catalog in the configured Jira site/account scope:

```sh
jira-cli metadata refresh
# Optional: replace the snapshot with fields for one verified project/work type.
# Use your Jira numeric IDs and work-type display name; -p selects the project key.
jira-cli -p PROJ metadata refresh --project-id 10000 --work-type-id 10001 --work-type-name Task
jira-cli query --dry-run "my work items with story points > 3"
```

`JIRA_FIELD_ALIASES` accepts a JSON object such as
`{"effort":"customfield_10010"}`. Exact IDs and configured aliases precede names
and fuzzy matching; ambiguous names require clarification. A site-visible catalog
does not establish project/work-type applicability. Scoped refresh uses Jira's
experimental project-fields endpoint and fails without replacing the cache when
that API is unavailable. It replaces the whole snapshot, so only the latest
selected scope is retained. Fuzzy matching requires known applicability when a
scope is requested. Dry runs, the trainer, and `jira_translate_query` read cached
metadata and projects only; they never fetch Jira data or search issues. Snapshots
older than 24 hours remain available offline and are labeled stale in translation
inspection; refresh explicitly after tenant changes.

Digest comment windows are separate from issue search dates: `my bugs in the last
3 days` selects a rolling comment window, while `my bugs created in the last 3 days`
filters issue creation dates. Vague durations, sprint/month approximations, and
calendar comment windows require clarification instead of guessing a day count.

Query results distinguish displayed rows from matching totals. List searches follow
continuation tokens until the display limit or an explicit terminal page; a short
page alone never proves completion. `--count` (or a question beginning with
`how many` or `count`) enumerates matches while retaining at most `--max` rows.
An exact total requires a completed enumeration. Each search allows up to 100
pages of at most 100 requested rows and 60 seconds; interruptions report partial
rows and return an error. Missing pagination metadata is shown as incomplete,
with an unknown or unverified Jira-reported total. No approximate-count endpoint
is used. The count reflects the visible matches encountered during traversal,
not an atomic snapshot of a changing Jira project.

The MCP `jira_query` tool follows the same policy; `exact_count: true` requests
count enumeration and `max_results` limits its displayed preview. The dashboard
shows the same completeness and count evidence. Missing search metadata is
reported as unknown; a preview row count is never treated as a matching total.

### Digest

```sh
# By issue key
jira-cli digest FEAT-123

# By natural language
jira-cli digest "Features targeting the next release"
jira-cli digest "top 5 initiatives for my team"

# With time window
jira-cli digest FEAT-123 --days 14

# Offline (cached data only)
jira-cli digest FEAT-123 --cache-only
```

### Ticket Enrichment

```sh
# Preview suggestions
jira-cli enrich PROJ-123

# Apply to JIRA
jira-cli enrich PROJ-123 --apply
```

### Create EPIC

```sh
# Interactive: prompts for description, previews before creating
jira-cli create-epic

# Non-interactive
jira-cli create-epic -d "Build user onboarding flow" --no-interactive
```

### Weekly Status

```sh
# Generate weekly status (defaults to last 7 days)
jira-cli weekly-status

# Custom date range
jira-cli weekly-status --start 2026-03-01 --end 2026-03-31

# Post to Confluence
jira-cli weekly-status --confluence

# With verbose cache diagnostics
jira-cli -vv weekly-status
```

### Comment Summarizer

```sh
# Summarize comment thread on an issue
jira-cli summarize-comments PROJ-123
```

### Backlog Health Check

```sh
# Run backlog health analysis with LLM summary
jira-cli backlog-health

# Rule-based checks only (no LLM)
jira-cli backlog-health --no-llm

# Custom stale threshold
jira-cli backlog-health --stale-days 30
```

### Configuration

```sh
# Show JIRA settings, LLM provider, and cache stats
jira-cli config
```

### JIRA CRUD

```sh
# Get full issue details
jira-cli get-issue PROJ-123

# Create an issue
jira-cli create-issue --summary "Fix login bug" --type Bug --priority High

# Edit an issue
jira-cli edit-issue PROJ-123 --priority Critical --assignee user@example.com

# Transition an issue
jira-cli get-transitions PROJ-123          # list available transitions
jira-cli transition PROJ-123 31            # transition by ID

# Comments, links, attachments
jira-cli add-comment PROJ-123 --body "Done"
jira-cli link-issues --inward PROJ-123 --outward PROJ-456 --type Blocks
jira-cli attach-file PROJ-123 report.pdf

# Look up users
jira-cli lookup-user jsmith
```

### MCP Server

The MCP server exposes all 36 tools to Claude Code and other MCP-compatible clients. There are two ways to run it:

#### Option A: Local binary (stdio, recommended)

The simplest setup — build once and point `.mcp.json` at the binary.

1. Build the binary:
   ```sh
   make build
   ```

2. Add to your project's `.mcp.json`:
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

3. Claude Code will auto-detect the config on next startup.

#### Option B: Container with SSE

Best for shared or remote setups where multiple clients connect simultaneously.

1. Build and start:
   ```sh
   make container-run
   ```

2. Verify it's running:
   ```sh
   curl -sf http://localhost:18080/healthz
   # {"status":"ok"}
   ```

3. Add to your project's `.mcp.json`:
   ```json
   {
     "mcpServers": {
       "jira": {
         "type": "sse",
         "url": "http://localhost:8081/sse",
         "oauth": {}
       }
     }
   }
   ```

The `"oauth": {}` disables OAuth discovery, which is required for servers without authentication. See [Container Deployment](#container-deployment) below for production hardening.

#### Verifying the connection

Ask Claude Code to run any tool, e.g.:

```
Use jira_summary to show my current issues
```

You should see 36 tools available.

The MCP server also starts a web dashboard at `http://localhost:18080` (in both stdio and SSE modes). It renders stored tool results as interactive HTML pages with Chart.js charts, Mermaid hierarchy diagrams, color-coded status/priority tables, and side-by-side comparisons. Six tools have dashboard views: summary, query, digest, enrich, create_epic, and weekly_status. See the [Web Dashboard](docs/HOWTO-AI-SETUP.md#web-dashboard) section for details on each view.

For a more detailed setup guide (including troubleshooting), see [docs/HOWTO-AI-SETUP.md](docs/HOWTO-AI-SETUP.md).

#### Available MCP Tools (36)

**Core & AI-powered**

| Tool | Description |
|------|-------------|
| `jira_summary` | Daily summary of assigned issues and sprint info |
| `jira_translate_query` | Translate and locally validate natural language without executing a Jira search |
| `jira_query` | Natural language search; deterministic parser first, LLM fallback for complex queries |
| `jira_digest` | Executive digest for Features/Initiatives; natural-language issues translated to JQL (deterministic parser first, LLM fallback) |
| `jira_enrich` | AI-generated enrichment for sparse tickets |
| `jira_weekly_status` | Weekly status report with optional Confluence posting |
| `jira_create_epic` | LLM-assisted EPIC creation |
| `jira_summarize_comments` | AI summary of issue comment threads |
| `jira_backlog_health` | Backlog health analysis with findings and recommendations |
| `jira_find_similar` | Find duplicate or related issues using AI similarity analysis |

**Confluence**

| Tool | Description |
|------|-------------|
| `jira_confluence_get_page` | Read a page or blog post by ID or title |
| `jira_confluence_search` | Search Confluence using CQL |
| `jira_confluence_list_pages` | List pages in a space |
| `jira_confluence_get_comments` | Read footer comments on a page |
| `jira_confluence_analytics` | Page view stats for a page and its children |
| `jira_confluence_create_page` | Create a new page under a parent |
| `jira_confluence_create_blog` | Create a new blog post |
| `jira_confluence_update` | Update an existing page or blog post body |
| `jira_confluence_add_label` | Add a label to a page |

**CRUD**

| Tool | Description |
|------|-------------|
| `jira_get_issue` | Full issue details with comments and links |
| `jira_create_issue` | Create any issue type |
| `jira_edit_issue` | Update issue fields |
| `jira_get_transitions` | List available workflow transitions |
| `jira_transition` | Change issue workflow status |
| `jira_add_comment` | Add a comment to an issue |
| `jira_add_worklog` | Record time spent on an issue |
| `jira_lookup_user` | Search users by name/email |
| `jira_link_issues` | Create links between issues |
| `jira_attach_file` | Upload file attachments to issues |
| `jira_watch_issue` | Add yourself as a watcher on an issue |
| `jira_unwatch_issue` | Remove yourself as a watcher |
| `jira_add_labels` | Add labels to an issue without replacing existing |
| `jira_remove_labels` | Remove labels from an issue |
| `jira_list_sprints` | List sprints for the project's boards |
| `jira_vote_issue` | Add your vote to an issue |
| `jira_unvote_issue` | Remove your vote from an issue |

### Container Deployment

A pre-built multi-arch image (amd64 + arm64) is available on Quay.io:

```sh
podman pull quay.io/aasmith/jira-cli:latest
```

Run as a shared MCP server so multiple Claude Code instances can connect via SSE:

```sh
# Using the pre-built image
podman run -d --name jira-cli-mcp \
  -p 8081:8081 -p 18080:18080 \
  -v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
  --read-only --tmpfs /tmp \
  --cap-drop=ALL \
  --env-file .env \
  quay.io/aasmith/jira-cli:latest

# Or build and run from source
make container-run

# Check health
curl http://localhost:18080/healthz

# View logs
podman logs jira-cli-mcp

# Stop
podman stop jira-cli-mcp && podman rm jira-cli-mcp
# or: make container-stop
```

The container uses Red Hat UBI 9 minimal as the base image. It runs as non-root (UID 1001), with a read-only root filesystem, all capabilities dropped, and SUID/SGID binaries stripped. Cache is persisted via a named volume (`jira-cli-cache`).

#### Secrets

By default, credentials are passed via `--env-file .env`. For production deployments, mount secret files instead — env vars are visible in `podman inspect`:

```sh
podman run -d --name jira-cli-mcp \
  -p 8081:8081 -p 18080:18080 \
  -v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
  -v ./secrets:/run/secrets:ro,Z \
  --read-only --tmpfs /tmp \
  --cap-drop=ALL \
  jira-cli
```

Create one file per secret in `./secrets/` (e.g. `JIRA_API_TOKEN`, `JIRA_SERVER`). The application checks `/run/secrets/<NAME>` before falling back to environment variables.

#### TLS

The MCP server does not terminate TLS. For network-exposed deployments, place it behind a reverse proxy:

```
[Claude Code] → HTTPS → [nginx/HAProxy] → HTTP → [jira-cli :8081]
```

For local podman usage on localhost, TLS is not required.

#### Supply Chain

```sh
make container-scan   # CVE scan with Trivy
make container-sign   # Sign image with cosign (Sigstore)
make container-sbom   # Generate SBOM with syft (SPDX format)
```

### Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--user` | `-u` | JIRA user email (default: `currentUser()`) |
| `--project` | `-p` | JIRA project key (default: `JIRA_PROJECT` env) |
| `--format` | `-f` | Output format: `markdown` (default), `slack`, `text`, `pretty` |
| `--outfile` | `-o` | Output file path (default: `{project}.md`) |
| `--verbose` | `-v` | Increase verbosity (`-v` for verbose, `-vv` for debug with cache diagnostics) |

## Creating a JIRA API Token

1. Log in to [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens)
2. Click **Create API token**
3. Give it a label (e.g. "jira-cli") and click **Create**
4. Copy the token immediately — it won't be shown again
5. Paste it into your `.env` file as `JIRA_API_TOKEN`

Your `JIRA_SERVER` is the URL you use to access JIRA (e.g. `https://yourcompany.atlassian.net`). Your `JIRA_EMAIL` is the email address associated with your Atlassian account.

## LLM Providers

Set `LLM_PROVIDER` to choose your backend. All AI features (query, digest, enrich, weekly status, EPIC creation) work with any supported provider.

| Provider | `LLM_PROVIDER` | Key env vars | Notes |
|----------|----------------|--------------|-------|
| Vertex AI (Claude) | `vertex` (default) | `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION` | Requires `gcloud auth application-default login` |
| OpenAI-compatible | `openai` | `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL` | Works with OpenAI, DeepInfra, vLLM, etc. |
| Ollama | `ollama` | `OLLAMA_BASE_URL`, `LLM_MODEL` | Local or remote; no API key needed |

`ANTHROPIC_VERTEX_PROJECT_ID` and `CLOUD_ML_REGION` are only required when using the `vertex` provider.

Example for DeepInfra:

```
LLM_PROVIDER=openai
LLM_BASE_URL=https://api.deepinfra.com/v1
LLM_API_KEY=your-deepinfra-key
LLM_MODEL=meta-llama/Llama-3.3-70B-Instruct-Turbo
```

Example for local Ollama:

```
LLM_PROVIDER=ollama
OLLAMA_BASE_URL=http://localhost:11434
LLM_MODEL=llama3
```

LLM responses wrapped in JSON code fences are automatically stripped, improving compatibility with non-Claude models.

See `.env.example` for full documentation of all provider options.

## Architecture

```
cmd/
  root.go              # Global flags, logging setup
  summary.go           # Summary command with cache logic
  query.go             # Natural language query command
  digest.go            # Digest command with hierarchy traversal
  enrich.go            # Ticket enrichment command
  create_epic.go       # LLM-assisted EPIC creation
  weekly_status.go     # Weekly status report with Confluence support
  summarize_comments.go # Comment thread summarizer
  backlog_health.go    # Backlog health check
  config.go            # Show settings and cache stats
  get_issue.go         # Get issue details
  create_issue.go      # Create issues
  edit_issue.go        # Edit issues
  get_transitions.go   # List workflow transitions
  transition.go        # Transition issue status
  add_comment.go       # Add comments
  lookup_user.go       # Search users
  link_issues.go       # Link issues
  attach_file.go       # Upload attachments
  mcp.go               # MCP server startup (stdio + SSE)
internal/
  config/config.go     # .env loading, Config struct
  jira/
    client.go          # HTTP client for JIRA REST API v3 + Agile v1.0
    crud.go            # CRUD operations (create, edit, transition, comment, link)
    models.go          # Issue, IssueDetail, Comment, Transition structs
  confluence/
    client.go          # Confluence Cloud REST API client
    format.go          # XHTML storage format conversion
  llm/
    llm.go             # Multi-provider LLM client (Vertex AI, OpenAI-compat, Ollama)
    digest.go          # Digest report generation
    weekly.go          # Weekly status generation
    enrich.go          # Ticket enrichment
    comments.go        # Comment thread summarization
    health.go          # Backlog health rules + LLM summary
  cache/
    cache.go           # SQLite: issues, comments, links, digest log
    fetcher.go         # Fetcher interface for testable cache-aware logic
  mcpserver/
    server.go          # MCP server setup, tool registration, SSE/stdio transport
    tools.go           # Tool handlers (summary, query, digest, enrich, weekly, health, comments)
    crud_tools.go      # CRUD tool handlers (get, create, edit, transition, etc.)
    store.go           # Durable instance-owned dashboard history and retention
    webserver.go       # Dashboard, /healthz liveness, /readyz passive readiness
  format/
    terminal.go        # pterm tables, spinners, color
    markdown.go        # GitHub + Slack markdown output
  web/
    templates/         # HTML templates for web dashboard
```

### Cache Design

SQLite databases at `~/.jira-cli/cache-v1/<hash>.db` isolate normalized Jira sites
and authenticated accounts. Summary membership and weekly reports also distinguish
the effective user and project. The current schema is created directly and
reopened without clearing its data. Historical schema upgrades and the unscoped
cache API have been removed: this application has no production data to migrate.
Earlier `cache.db` and `scopes/*.db` files are not read or converted. The first
run with this cache format starts cold; offline summaries require a new successful
online refresh.

- **Summaries**: Every online run retrieves the complete open-work set and board memberships. A transaction replaces that user's memberships and advances the checkpoint together. Failed refreshes preserve the prior snapshot; `--cache-only` uses a verified snapshot without API calls.
- **Open work**: Uses Jira status category rather than workflow status names. Issues that close, move, leave the user's assignments, or become inaccessible disappear on the next successful reconciliation. Canonical issue data remains separate from result membership.
- **Issue details**: Cached details are checked against Jira `updated` timestamps where available. Newer canonical observations cannot be overwritten by older search-index rows. Mutation revisions reject cache fills started before a local mutation.
- **Weekly status**: A complete current search precedes report reuse. A fingerprint includes site/account, effective user, project, dates, sorted issue keys, and report-relevant fields, so removals and same-timestamp replacements invalidate reports. Regeneration fetches comments; detail or comment failures prevent caching a partial report.
- **Mutation invalidation**: Clears affected canonical/detail/comment/link data, invalidates summary snapshots, and clears weekly reports. Summary refreshes directly recheck locally invalidated issues while Jira's search index catches up. A cache failure is logged without turning a completed remote mutation into an apparent failure.
- **Board mappings**: Membership belongs to the requested user's snapshot and preserves separate boards and active sprints, including empty groups.

The container launchers mount the named volume `jira-cli-cache` at
`/home/jira-cli/.jira-cli`. With those launchers:

| Operation | Cache data retained? |
| --- | --- |
| Stop/start or restart the container | Yes |
| Build/rebuild an image | Yes |
| Remove the container with `podman rm` | Yes |
| Run `make container-stop` / `make container-run` | Yes; the same named volume is reused |
| Remove `jira-cli-cache` or delete its underlying storage | No |

Named volumes are independent of containers; even `podman rm --volumes` removes
anonymous volumes, not this named volume. See the
[Podman removal documentation](https://docs.podman.io/en/latest/markdown/podman-rm.1.html#volumes-v).
Dashboard history persists in `~/.jira-cli/results-v1/<instance-hash>.db`, also
inside the named volume. The stable owner defaults to `default`; use
`jira-cli mcp --history-instance workbench` for a separately owned history.
Reusing an owner shares its results, deletion, and retention across local
processes. Defaults retain up to 1000 entries for 30 days; customize with
`--history-max-results` and `--history-max-age` (zero disables the respective
limit). Pruning occurs at startup and on save. Old PID histories are left alone.

The dashboard lists successes and failed attempts. Query views keep original and
normalized wording, exact execution text, separate display JQL, validation,
timing, and search completeness. `/healthz` reports liveness only. `/readyz`
returns 503 for failed, unknown, or stale required dependency observations;
`/api/status` includes checked-at times. These are passive observations from
normal operations and expire after five minutes. Deterministic-only or idle use
can leave readiness unknown. Polling does not call Jira or a model. See the
[Phase 6 contracts and limitations](docs/project-reliability-implementation-plan.md#phase-6-delivered-contracts-2026-10-16).

### JIRA API

Uses JIRA Cloud REST API v3 (`/rest/api/3/`) for search, issue CRUD, comments, transitions, and linking. Uses Agile REST API v1.0 (`/rest/agile/1.0/`) for boards and sprints. Authentication via email + API token (Basic Auth).

## Development

```sh
make build          # Build binary
make run ARGS="summary"  # Build and run
make check          # Run tidy + fmt + vet
make test           # Run tests
make train-jql      # Review translations and collect private feedback
make train-jql-report # Summarize private feedback
make train-jql-replay # Replay the sanitized corpus (CORPUS=path to override)
make test-translation # Run focused translation/replay tests
make test-dashboard # Run browser refresh + MCP regressions (requires Node.js)
make verify         # Build, vet, race tests, docs, browser tests, diff checks
make lint           # Run vet + staticcheck
make restart-mcp    # Rebuild and restart MCP server
make container      # Build container image
make container-run  # Build and run MCP container (SSE + dashboard)
make container-stop # Stop and remove container
make container-scan # CVE scan with Trivy
make container-sign # Sign image with cosign
make container-sbom # Generate SBOM (SPDX)
make release        # Cross-compile for all platforms
make help           # Show all targets
```

`verify` checks the working tree without running the formatting or module
rewrites performed by `check`; it requires Node.js for the dashboard browser
regressions. Trainer targets accept extra CLI flags through `ARGS`, for example
`make train-jql ARGS='-p PROJ'`. See [feedback and replay](docs/translation-feedback.md).

## License

MIT — see [LICENSE](LICENSE).
