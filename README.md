# jira-cli

A Go CLI tool and MCP server for JIRA Cloud. Provides daily summaries, natural language search, AI-powered digest reports, ticket enrichment, weekly status generation, and full CRUD operations — all with SQLite caching and a rich web dashboard.

Inspired by the [official Atlassian MCP server](https://github.com/atlassian/mcp-server-atlassian), but built from scratch in Go with SQLite caching to avoid JIRA Cloud API throttling on repeated queries. The caching layer enables delta fetches, smart cache invalidation, and near-instant repeat runs without hitting the API.

## Features

- **Daily Summary** — Pull assigned issues from boards (Scrum sprints + Kanban) and display in terminal with color-coded status/priority. Outputs markdown (GitHub-flavored or Slack-compatible).
- **Natural Language Search** — Describe what you want in plain English; an LLM translates it to JQL and executes the search.
- **Digest Reports** — Walk issue hierarchy (Initiative → Feature → Epic), fetch recent comments, and generate executive progress digests with AI.
- **Ticket Enrichment** — Fill in sparse tickets with AI-generated descriptions, acceptance criteria, labels, and priority suggestions.
- **Weekly Status** — Generate formatted weekly status reports from JIRA activity, optionally post to Confluence.
- **JIRA CRUD** — Create, edit, transition, comment on, and link issues via MCP tools.
- **MCP Server** — Exposes all features as Model Context Protocol tools for use with Claude Code and other MCP clients. Includes a rich web dashboard on port 18080.
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

### MCP Server

```sh
# Start the MCP server (stdio transport for Claude Code)
jira-cli mcp
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

#### Available MCP Tools

| Tool | Description |
|------|-------------|
| `jira_summary` | Daily summary of assigned issues and sprint info |
| `jira_query` | Natural language search translated to JQL |
| `jira_digest` | Executive digest for Features/Initiatives |
| `jira_enrich` | AI-generated enrichment for sparse tickets |
| `jira_weekly_status` | Weekly status report with optional Confluence posting |
| `jira_create_epic` | LLM-assisted EPIC creation |
| `jira_get_issue` | Full issue details with comments and links |
| `jira_create_issue` | Create any issue type |
| `jira_edit_issue` | Update issue fields |
| `jira_get_transitions` | List available workflow transitions |
| `jira_transition` | Change issue workflow status |
| `jira_add_comment` | Add a comment to an issue |
| `jira_lookup_user` | Search users by name/email |
| `jira_link_issues` | Create links between issues |
| `jira_attach_file` | Upload file attachments to issues |

The MCP server includes a web dashboard at `http://127.0.0.1:18080` showing stored results with charts and interactive views.

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
  mcp.go               # MCP server startup
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
  cache/
    cache.go           # SQLite: issues, comments, links, digest log
  mcpserver/
    server.go          # MCP server setup and tool registration
    tools.go           # Core tool handlers (summary, query, digest, enrich, weekly)
    crud_tools.go      # CRUD tool handlers (get, create, edit, transition, etc.)
    store.go           # In-memory result store for web dashboard
    webserver.go       # HTTP server for dashboard
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
make release        # Cross-compile for all platforms
make help           # Show all targets
```

## License

MIT — see [LICENSE](LICENSE).
