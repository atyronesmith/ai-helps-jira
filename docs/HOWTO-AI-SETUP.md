# HOWTO: Set Up jira-cli MCP Server for Your Project

This guide is for AI coding agents (Claude Code, etc.) that need JIRA access. The recommended path is building the binary and using stdio transport. A container alternative is available for shared/remote setups.

## Step 1: Check for an Existing Binary

Check if `jira-cli` is already built:

```sh
which jira-cli || ls /path/to/jira-cli
```

If found, skip to [Step 3: Add MCP Configuration](#step-3-add-mcp-configuration).

## Step 2: Build from Source

Clone and build the jira-cli project:

```sh
git clone https://github.com/atyronesmith/ai-helps-jira.git /tmp/jira-cli
cd /tmp/jira-cli
```

### Configure environment

Copy and edit `.env` with JIRA credentials (ask the user for these values):

```sh
cp .env.example .env
# Edit .env with:
#   JIRA_SERVER=https://yourcompany.atlassian.net
#   JIRA_EMAIL=user@company.com
#   JIRA_API_TOKEN=<api-token>
#   JIRA_PROJECT=PROJ
#
# For AI features (query, digest, enrich, etc.), also set LLM_PROVIDER
# and related vars. See .env.example for all options.
```

### Build the binary

Requires Go 1.25+:

```sh
make build
```

### Container alternative

If you prefer a container (for shared multi-client access), see [docs/CONTAINER-SETUP.md](CONTAINER-SETUP.md) for detailed instructions. The container provides SSE transport on port 8081 and a web dashboard on port 18080.

## Step 3: Add MCP Configuration

Add to your project's `.mcp.json` (create it if it doesn't exist):

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

If using the container instead, use the SSE configuration:

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

The `"oauth": {}` disables OAuth discovery, which is required for servers without authentication.

Claude Code will auto-detect this and connect on next startup.

## Step 4: Verify Connection

Ask Claude Code to list available tools or run:

```
Use jira_summary to show my current issues
```

You should see 36 tools available:

**Core & AI-powered:** `jira_summary`, `jira_translate_query`, `jira_query`, `jira_digest`, `jira_enrich`, `jira_weekly_status`, `jira_create_epic`, `jira_summarize_comments`, `jira_backlog_health`, `jira_find_similar`

**Confluence:** `jira_confluence_analytics`, `jira_confluence_update`, `jira_confluence_get_page`, `jira_confluence_search`, `jira_confluence_list_pages`, `jira_confluence_get_comments`, `jira_confluence_add_label`, `jira_confluence_create_page`, `jira_confluence_create_blog`

**CRUD:** `jira_get_issue`, `jira_create_issue`, `jira_edit_issue`, `jira_get_transitions`, `jira_transition`, `jira_add_comment`, `jira_add_worklog`, `jira_lookup_user`, `jira_link_issues`, `jira_attach_file`, `jira_watch_issue`, `jira_unwatch_issue`, `jira_add_labels`, `jira_remove_labels`, `jira_list_sprints`, `jira_vote_issue`, `jira_unvote_issue`

## Available Tools

### Read-only

| Tool | Use When |
|------|----------|
| `jira_summary` | You need an overview of assigned issues and sprint status |
| `jira_translate_query` | You want to inspect generated JQL and local validation without executing a Jira search |
| `jira_query` | You want to search JIRA using natural language (e.g. "critical bugs from last week") |
| `jira_get_issue` | You need full details of a specific issue (description, comments, links) |
| `jira_get_transitions` | You need to find the transition ID before changing an issue's status |
| `jira_lookup_user` | You need to find a user's account ID for assignment |
| `jira_list_sprints` | You need to see sprints for the project's boards |

### AI-powered (read-only)

| Tool | Use When |
|------|----------|
| `jira_digest` | You need an executive summary of Features/Initiatives with hierarchy |
| `jira_enrich` | You want AI suggestions for sparse tickets (use `apply: false` to preview) |
| `jira_weekly_status` | You need a formatted weekly status report |
| `jira_summarize_comments` | You want a summary of a long comment thread |
| `jira_backlog_health` | You want to find stale, orphaned, or under-specified issues |
| `jira_create_epic` | You need to create an EPIC with LLM-generated content |
| `jira_find_similar` | You want to find duplicate or related issues using AI similarity analysis |

### Confluence

| Tool | Use When |
|------|----------|
| `jira_confluence_get_page` | You need to read a Confluence page or blog post by ID or title |
| `jira_confluence_search` | You want to search Confluence using CQL |
| `jira_confluence_list_pages` | You need to list pages in a Confluence space |
| `jira_confluence_get_comments` | You need to read footer comments on a Confluence page |
| `jira_confluence_analytics` | You want page view stats for a page and its children |
| `jira_confluence_create_page` | You need to create a new Confluence page |
| `jira_confluence_create_blog` | You need to create a new Confluence blog post |
| `jira_confluence_update` | You need to update an existing page or blog post body |
| `jira_confluence_add_label` | You need to add a label to a Confluence page |

### Write operations

| Tool | Use When |
|------|----------|
| `jira_create_issue` | Creating a new issue (Task, Bug, Story, Sub-task, etc.) |
| `jira_edit_issue` | Updating fields on an existing issue |
| `jira_transition` | Moving an issue to a new workflow status |
| `jira_add_comment` | Adding a comment to an issue |
| `jira_link_issues` | Creating a link between two issues |
| `jira_attach_file` | Uploading a file to an issue |
| `jira_add_worklog` | Recording time spent on an issue |
| `jira_watch_issue` | Adding yourself as a watcher on an issue |
| `jira_unwatch_issue` | Removing yourself as a watcher |
| `jira_add_labels` | Adding labels to an issue without replacing existing |
| `jira_remove_labels` | Removing labels from an issue |
| `jira_vote_issue` | Adding your vote to an issue |
| `jira_unvote_issue` | Removing your vote from an issue |
| `jira_enrich` | Applying AI enrichment to JIRA (use `apply: true`) |

## Common Patterns

### Look up an issue before editing

```
1. jira_get_issue(issue_key: "PROJ-123")
2. Read the current state
3. jira_edit_issue(issue_key: "PROJ-123", summary: "Updated title")
```

### Transition an issue

```
1. jira_get_transitions(issue_key: "PROJ-123")
2. Find the transition ID for the target status
3. jira_transition(issue_key: "PROJ-123", transition_id: "31")
```

### Create an issue and link it

```
1. jira_create_issue(summary: "New task", issue_type: "Task", priority: "Medium")
2. Note the returned issue key
3. jira_link_issues(inward_issue: "PROJ-100", outward_issue: "PROJ-NEW", link_type: "Blocks")
```

### Log work done

After completing significant work tracked by a JIRA issue:

```
1. jira_add_comment(issue_key: "PROJ-123", body: "Implemented X. Changes: ...")
```

### Find users for assignment

```
1. jira_lookup_user(query: "jsmith")
2. Use the returned account_id
3. jira_edit_issue(issue_key: "PROJ-123", assignee_account_id: "...")
```

## Parameters

Most tools accept optional `user` and `project` parameters to override defaults:

- `user` — JIRA user email (defaults to the configured user)
- `project` — JIRA project key (defaults to the configured project)

If your project uses a different JIRA project than the default, pass `project` with every call.

## Web Dashboard

The MCP server starts a web dashboard on port 18080 in both stdio and SSE modes. Browse `http://localhost:18080` to see rich HTML views of tool results. Tools that generate dashboard views return a URL in their response.

### Index page

The landing page (`/`) lists all stored results in reverse chronological order. Each row shows the tool type (color-coded badge), title, and timestamp. Bulk select and delete is supported.

### Views by tool

| Tool | View contents |
|------|---------------|
| `jira_summary` | Two doughnut charts (status and priority distribution). One table per board/sprint showing issue key, status, priority, and summary. Issue keys link to JIRA. |
| `jira_query` | Original query in a blockquote, collapsible generated JQL, status/priority doughnut charts, and a results table with linked issue keys. |
| `jira_digest` | Epic header with overall status badge (on-track/at-risk/blocked). Interactive Mermaid hierarchy diagram with pan and zoom. Collapsible tables for progress updates, blockers, and not-started items. Executive summary paragraph. |
| `jira_enrich` | Side-by-side comparison of current vs. suggested description. Acceptance criteria list. Priority and labels comparison table. Green banner if already applied. |
| `jira_create_epic` | Created epic key (linked to JIRA), summary, full description, acceptance criteria, priority, and labels. |
| `jira_weekly_status` | Report header with user and date range. Sections grouped by project/epic, each with narrative bullet points describing completed work. Epic keys link to JIRA. |

### Styling

The dashboard uses Pico CSS with color-coded status badges (green for on-track/done, orange for at-risk/in-progress, red for blocked/to-do) and priority indicators (red for highest through green for lowest). Charts use Chart.js; hierarchy diagrams use Mermaid. The layout is responsive — two columns on desktop, single column on mobile.

## Troubleshooting

**"Connection refused"** — The MCP server isn't running. For stdio: check that the binary path in `.mcp.json` is correct. For container: check `podman ps`.

**"SDK auth failed" / "needs authentication"** — Claude Code is attempting OAuth discovery. Ensure your `.mcp.json` includes `"oauth": {}` to disable it (SSE only). Not needed for stdio.

**"Missing required env vars"** — The `.env` file is incomplete. Check with the project owner.

**Tools return errors about LLM** — AI-powered tools need an LLM provider configured in `.env`. CRUD tools work without an LLM.

**Stale data** — Use `refresh: true` on `jira_summary` to force a full fetch. Most tools use cached data that auto-refreshes.
