# ai-helps-jira

A Go CLI tool for JIRA Cloud that provides daily work summaries and LLM-assisted issue creation. Uses SQLite caching to minimize API calls and avoid JIRA Cloud throttling.

## Features

- **Daily Summary** — Pull assigned issues from boards (Scrum sprints + Kanban) and display in terminal with color-coded status/priority. Outputs markdown (GitHub-flavored or Slack-compatible).
- **SQLite Caching** — Delta fetches only changed issues after the first run. Supports `--cache-only` for fully offline summaries.
- **LLM EPIC Creation** — Describe what you need in plain English; Claude generates a complete EPIC (summary, description, acceptance criteria, priority, labels) and creates it in JIRA.
- **Cross-platform** — Single static binary, no CGO. Builds for Linux and macOS (amd64/arm64).

## Quick Start

### Prerequisites

- Go 1.21+
- JIRA Cloud API token ([create one here](https://id.atlassian.com/manage-profile/security/api-tokens))
- Google Cloud credentials for Vertex AI (for LLM features)

### Install

```sh
git clone git@github.com:yourorg/jira-cli.git
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
ANTHROPIC_VERTEX_PROJECT_ID=your-gcp-project-id
CLOUD_ML_REGION=us-east5
```

For Vertex AI authentication:

```sh
gcloud auth application-default login
```

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
jira-cli --slack-markdown summary

# Custom output file
jira-cli -o weekly-report.md summary

# Override user/project
jira-cli -u user@company.com -p MYPROJ summary
```

### Create EPIC

```sh
# Interactive: prompts for description, previews before creating
jira-cli create-epic

# Non-interactive
jira-cli create-epic -d "Build user onboarding flow" --no-interactive
```

### Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--user` | `-u` | JIRA user email (default: `currentUser()`) |
| `--project` | `-p` | JIRA project key (default: `JIRA_PROJECT` env) |
| `--slack-markdown` | | Slack-compatible mrkdwn output |
| `--outfile` | `-o` | Output file path (default: `{project}.md`) |

## Architecture

```
cmd/
  root.go            # Global flags, logging setup
  summary.go         # Summary command with cache logic
  create_epic.go     # LLM-assisted EPIC creation
internal/
  config/config.go   # .env loading, Config struct
  jira/
    client.go        # Raw HTTP client for JIRA REST API v3 + Agile v1.0
    models.go        # Issue, BoardInfo structs
  llm/
    llm.go           # Claude via Vertex AI
    templates.go     # EPIC prompts and content structs
  cache/
    cache.go         # SQLite: issues, board mappings, fetch log
  format/
    terminal.go      # pterm tables, spinners, color
    markdown.go      # GitHub + Slack markdown output
```

### Cache Design

The SQLite cache (`~/.jira-cli/cache.db`) stores issues and board memberships:

- **First run**: Full fetch from JIRA, stores everything
- **Subsequent runs**: Queries `updated >= "last_fetch"` for changes only, upserts into cache
- **Done issues**: Automatically pruned from cache after each fetch
- **Board mappings**: Junction table supports issues appearing on multiple boards

### JIRA API

Uses JIRA Cloud REST API v3 (`/rest/api/3/`) for search and issue creation, and Agile REST API v1.0 (`/rest/agile/1.0/`) for boards and sprints. Authentication via email + API token (Basic Auth).

## Planned Features

See [docs/features.md](docs/features.md) for detailed implementation plans.

| # | Feature | Command | Description |
|---|---------|---------|-------------|
| 1 | Standup Prep | `standup` | Generate daily standup from recent activity |
| 2 | Ticket Enrichment | `enrich <KEY>` | Fill in sparse tickets with AC, description, labels |
| 3 | Natural Language JQL | `query "..."` | Plain English to JQL translation |
| 4 | Comment Summarizer | `summarize-comments <KEY>` | Summarize long comment threads |
| 5 | Sprint Retro | `retro` | Analyze completed sprint, generate retro report |
| 6 | Release Notes | `release-notes` | Generate user-facing notes from a fix version |
| 7 | Duplicate Detection | `find-similar <KEY>` | Find semantically similar issues |
| 8 | Workload Analysis | `workload` | Flag risks, stale tickets, overload |
| 9 | Smart Ticket Creation | `create-ticket` | Extract structured ticket from freeform text |
| 10 | Dependency Mapper | `dependencies` | Surface implicit cross-issue dependencies |

## Development

```sh
make build          # Build binary
make run ARGS="summary"  # Build and run
make check          # Run tidy + fmt + vet
make test           # Run tests
make lint           # Run vet + staticcheck
make release        # Cross-compile for all platforms
make help           # Show all targets
```

## License

MIT
