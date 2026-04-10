# jira-cli

A Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI-powered digest reports, ticket enrichment, weekly status generation, and full CRUD operations — all with SQLite caching and a rich web dashboard.

Inspired by the [official Atlassian MCP server](https://github.com/atlassian/mcp-server-atlassian), but built from scratch in Go with SQLite caching to avoid JIRA Cloud API throttling on repeated queries. The caching layer enables delta fetches, smart cache invalidation, and near-instant repeat runs without hitting the API.

## Features

- **Daily Summary** — Pull assigned issues from boards (Scrum sprints + Kanban) and display in terminal with color-coded status/priority. Outputs markdown (GitHub-flavored or Slack-compatible).
- **Natural Language Search** — Describe what you want in plain English; an LLM translates it to JQL and executes the search.
- **Digest Reports** — Walk issue hierarchy (Initiative → Feature → Epic), fetch recent comments, and generate executive progress digests with AI.
- **Ticket Enrichment** — Fill in sparse tickets with AI-generated descriptions, acceptance criteria, labels, and priority suggestions.
- **Weekly Status** — Generate formatted weekly status reports from JIRA activity, optionally post to Confluence.
- **Comment Summarizer** — Summarize long comment threads into key decisions, action items, and open questions.
- **Backlog Health Check** — Rule-based analysis (stale tickets, missing descriptions, orphaned issues, unassigned active, missing labels) with optional LLM executive summary.
- **JIRA CRUD** — Create, edit, transition, comment on, link issues, look up users, and attach files — all from CLI or MCP.
- **MCP Server** — Exposes all 17 tools as Model Context Protocol tools for Claude Code and other MCP clients. Supports stdio and SSE transports. Includes a rich web dashboard.
- **Container Support** — Run as a shared MCP server in podman/docker with SSE transport. Multiple Claude Code instances can connect simultaneously. Red Hat UBI minimal base image with health checks and persistent cache volume.
- **Multi-LLM Provider Support** — Use any LLM backend: Vertex AI (Claude), OpenAI-compatible APIs (DeepInfra, vLLM, etc.), or Ollama for fully local inference.
- **SQLite Caching** — Delta fetches only changed issues after the first run. Smart cache invalidation compares JIRA timestamps. Per-issue detail caching means repeat runs skip re-fetching unchanged issues entirely.
- **Confluence Integration** — Post weekly status reports as child pages with automatic parent page indexing.
- **Cross-platform** — Single static binary, no CGO. Builds for Linux and macOS (amd64/arm64).

## Quick Start

### Prerequisites

- Go 1.23+
- JIRA Cloud API token ([create one here](https://id.atlassian.com/manage-profile/security/api-tokens))
- An LLM provider for AI features (see [LLM Providers](#llm-providers) below)

### Install

```sh
git clone https://github.com/atyronesmith/ai-helps-jira.git
cd ai-helps-jira
cp .env.example .env   # edit with your credentials
make install
```

### Configure

Edit `.env` with your values:

```
JIRA_SERVER=https://yourcompany.atlassian.net
JIRA_EMAIL=your.email@company.com
JIRA_API_TOKEN=your-jira-api-token
JIRA_PROJECT=PROJ
```

Then configure your LLM provider (see [LLM Providers](#llm-providers) below). See `.env.example` for full documentation of all environment variables.

## Usage

### Summary

```sh
# First run: full fetch from JIRA, populates cache
jira-cli summary

# Subsequent runs: delta fetch (only changed issues)
jira-cli summary

# Force full refresh
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
```

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

```sh
# Stdio transport (default, for single Claude Code instance)
jira-cli mcp

# SSE transport (for containers / multiple clients)
jira-cli mcp --transport sse --sse-port 8081
```

Configure in your MCP client (e.g. `.mcp.json`):

```json
{
  "mcpServers": {
    "jira-cli": {
      "command": "./jira-cli",
      "args": ["mcp"]
    }
  }
}
```

For SSE (container or shared server):

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

#### Available MCP Tools

| Tool | Description |
|------|-------------|
| `jira_summary` | Daily summary of assigned issues and sprint info |
| `jira_query` | Natural language search translated to JQL |
| `jira_digest` | Executive digest for Features/Initiatives |
| `jira_enrich` | AI-generated enrichment for sparse tickets |
| `jira_weekly_status` | Weekly status report with optional Confluence posting |
| `jira_create_epic` | LLM-assisted EPIC creation |
| `jira_summarize_comments` | AI summary of issue comment threads |
| `jira_backlog_health` | Backlog health analysis with findings and recommendations |
| `jira_get_issue` | Full issue details with comments and links |
| `jira_create_issue` | Create any issue type |
| `jira_edit_issue` | Update issue fields |
| `jira_get_transitions` | List available workflow transitions |
| `jira_transition` | Change issue workflow status |
| `jira_add_comment` | Add a comment to an issue |
| `jira_lookup_user` | Search users by name/email |
| `jira_link_issues` | Create links between issues |
| `jira_attach_file` | Upload file attachments to issues |

The MCP server includes a web dashboard at `http://localhost:18080` showing stored results with charts and interactive views. A `/healthz` endpoint is available for container health checks.

### Container Deployment

Run as a shared MCP server in podman/docker so multiple Claude Code instances can connect via SSE:

```sh
# Build and run
make container-run

# Or manually:
podman build --format docker -t jira-cli .
podman run -d --name jira-cli-mcp \
  -p 8081:8081 -p 18080:18080 \
  -v jira-cli-cache:/root/.jira-cli:Z \
  --env-file .env \
  jira-cli

# Check health
curl http://localhost:18080/healthz

# View logs
podman logs jira-cli-mcp

# Stop
make container-stop
```

The container uses Red Hat UBI 9 minimal as the base image. Cache is persisted via a named volume (`jira-cli-cache`). The server binds to `0.0.0.0` inside the container for port mapping.

### Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--user` | `-u` | JIRA user email (default: `currentUser()`) |
| `--project` | `-p` | JIRA project key (default: `JIRA_PROJECT` env) |
| `--format` | `-f` | Output format: `markdown` (default), `slack`, `text`, `pretty` |
| `--outfile` | `-o` | Output file path (default: `{project}.md`) |
| `--verbose` | `-v` | Increase verbosity (`-v` for verbose, `-vv` for debug with cache diagnostics) |

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
    store.go           # In-memory result store for web dashboard
    webserver.go       # HTTP server for dashboard + /healthz endpoint
  format/
    terminal.go        # pterm tables, spinners, color
    markdown.go        # GitHub + Slack markdown output
  web/
    templates/         # HTML templates for web dashboard
```

### Cache Design

The SQLite cache (`~/.jira-cli/cache.db`) stores issues, comments, links, and run history:

- **First run**: Full fetch from JIRA, stores everything
- **Subsequent runs**: Queries `updated >= "last_fetch"` for changes only, upserts into cache
- **Per-issue details**: Individual issue details (description, labels, parent, assignee, etc.) are cached. On repeat runs, only issues whose JIRA `updated` timestamp changed are re-fetched. This dramatically reduces API calls — e.g., `weekly-status` with 25 issues goes from 50+ API calls to near-zero on cache hit. The `get-issue` command also checks cache first.
- **Weekly status**: Caches full LLM results; compares JIRA `updated` timestamps to skip re-generation when nothing changed
- **Done issues**: Automatically pruned from cache after each fetch
- **Board mappings**: Junction table supports issues appearing on multiple boards

### JIRA API

Uses JIRA Cloud REST API v3 (`/rest/api/3/`) for search, issue CRUD, comments, transitions, and linking. Uses Agile REST API v1.0 (`/rest/agile/1.0/`) for boards and sprints. Authentication via email + API token (Basic Auth).

## Development

```sh
make build          # Build binary
make run ARGS="summary"  # Build and run
make check          # Run tidy + fmt + vet
make test           # Run tests
make lint           # Run vet + staticcheck
make restart-mcp    # Rebuild and restart MCP server
make container      # Build container image
make container-run  # Build and run MCP container (SSE + dashboard)
make container-stop # Stop and remove container
make release        # Cross-compile for all platforms
make help           # Show all targets
```

## License

MIT — see [LICENSE](LICENSE).
