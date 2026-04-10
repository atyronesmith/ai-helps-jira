# JIRA CLI — LLM Feature Roadmap

Each feature below uses the existing Vertex AI / Claude integration and JIRA REST API client.
Features are ordered roughly by implementation complexity (simplest first).

---

## 1. Standup Prep

Generate a ready-to-paste standup update from recent JIRA activity.

**Command:** `jira-cli standup [--days N] [--format bullet|prose]`

**How it works:**
1. Query JIRA for issues assigned to user with `updated >= -Nd` (default 1 day)
2. Also fetch issues transitioned to Done/Closed in that window
3. Send issue list + status changes to Claude with a standup prompt
4. Claude returns structured output: *yesterday*, *today*, *blockers*
5. Display in terminal, optionally write to file

**JIRA API:**
- `POST /rest/api/3/search/jql` — issues updated recently
- JQL: `assignee = currentUser() AND updated >= "-1d" ORDER BY updated DESC`

**New/changed files:**
- `cmd/standup.go` — command wiring
- `internal/llm/standup.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplayStandup()`
- `internal/format/markdown.go` — add `WriteStandupMarkdown()`

**Implementation steps:**
1. Add `StandupContent` struct and system prompt in `internal/llm/standup.go`
2. Add `GenerateStandup()` function that takes issues and calls Claude
3. Add `cmd/standup.go` with `--days` and `--format` flags
4. Add terminal display and markdown output
5. Wire into root command

---

## 2. Ticket Enrichment / Refinement ✅

Take a sparse ticket (by key) and generate missing fields: acceptance criteria,
fuller description, story points estimate, suggested labels.

**Command:** `jira-cli enrich <ISSUE-KEY> [--apply]`

**How it works:**
1. Fetch the issue by key from JIRA
2. Send current fields to Claude with an enrichment prompt
3. Claude suggests: expanded description, acceptance criteria, labels, story points
4. Preview in terminal
5. With `--apply`, update the issue in JIRA via PUT

**JIRA API:**
- `GET /rest/api/3/issue/{key}` — fetch issue details
- `PUT /rest/api/3/issue/{key}` — update issue (with `--apply`)

**New/changed files:**
- `cmd/enrich.go` — command wiring
- `internal/jira/client.go` — add `GetIssue()` and `UpdateIssue()` methods
- `internal/llm/enrich.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplayEnrichPreview()`

**Implementation steps:**
1. Add `GetIssue(key)` to JIRA client (GET single issue, parse full fields)
2. Add `UpdateIssue(key, fields)` to JIRA client (PUT with partial fields)
3. Add `EnrichmentContent` struct and system prompt in `internal/llm/enrich.go`
4. Add `GenerateEnrichment()` that takes existing issue and returns suggestions
5. Add `cmd/enrich.go` with preview + optional `--apply`
6. Add terminal preview formatting

---

## 3. Natural Language JQL ✅

Type queries in plain English, LLM translates to JQL, executes, shows results.

**Command:** `jira-cli query "show me all critical bugs from last week"`

**How it works:**
1. User provides natural language query as positional arg
2. Send to Claude with project context + JQL syntax reference
3. Claude returns JQL string
4. Execute JQL via existing `searchIssues()`
5. Display results in standard issue table
6. Optionally show the generated JQL for learning (`--show-jql`)

**JIRA API:**
- `POST /rest/api/3/search/jql` — existing `searchIssues()` method

**New/changed files:**
- `cmd/query.go` — command wiring
- `internal/llm/query.go` — JQL generation prompt + response

**Implementation steps:**
1. Add JQL generation prompt in `internal/llm/query.go` — include JQL syntax
   reference, project key, common fields/statuses
2. Add `GenerateJQL()` function
3. Add `cmd/query.go` — takes positional arg, calls LLM, runs search, displays
4. Add `--show-jql` flag to print the generated JQL
5. Reuse existing `printIssueTable()` for output

---

## 4. Comment Thread Summarizer ✅

Summarize long comment threads on an issue into key decisions and action items.

**Command:** `jira-cli summarize-comments <ISSUE-KEY>`

**How it works:**
1. Fetch all comments for the issue
2. Send comment thread to Claude
3. Claude returns: key decisions, action items, open questions, summary
4. Display in terminal

**JIRA API:**
- `GET /rest/api/3/issue/{key}/comment` — fetch comments

**New/changed files:**
- `cmd/summarize_comments.go` — command wiring
- `internal/jira/client.go` — add `GetComments()` method
- `internal/llm/comments.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplayCommentSummary()`

**Implementation steps:**
1. Add `GetComments(key)` to JIRA client — parse comment author, body, created
2. Add comment summary prompt and `CommentSummary` struct in `internal/llm/comments.go`
3. Add `GenerateCommentSummary()` function
4. Add terminal display for decisions/actions/questions
5. Wire up `cmd/summarize_comments.go`

---

## 5. Sprint Retrospective Report

Analyze a completed sprint and generate a retro summary.

**Command:** `jira-cli retro [--board BOARD] [--sprint SPRINT]`

**How it works:**
1. Find the most recently closed sprint (or specified sprint)
2. Fetch all issues from that sprint
3. Calculate metrics: completed vs incomplete, velocity, carryover
4. Send to Claude for narrative analysis
5. Output structured retro: what went well, what didn't, action items

**JIRA API:**
- `GET /rest/agile/1.0/board/{id}/sprint?state=closed` — closed sprints
- `GET /rest/agile/1.0/sprint/{id}/issue` — sprint issues
- `POST /rest/api/3/search/jql` — for additional context

**New/changed files:**
- `cmd/retro.go` — command wiring
- `internal/jira/client.go` — add `GetClosedSprints()`, `GetSprintIssues()`
- `internal/llm/retro.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplayRetro()`
- `internal/format/markdown.go` — add `WriteRetroMarkdown()`

**Implementation steps:**
1. Add `GetClosedSprints(boardID)` to JIRA client
2. Add `GetSprintIssues(sprintID)` — all issues in sprint regardless of status
3. Build sprint metrics (completed count, incomplete, by priority, by assignee)
4. Add retro prompt and `RetroContent` struct in `internal/llm/retro.go`
5. Add `GenerateRetro()` — takes metrics + issues, returns narrative
6. Add terminal and markdown display
7. Wire up `cmd/retro.go` with board/sprint selection

---

## 6. Release Notes Generator

Generate user-facing release notes from issues in a fix version or date range.

**Command:** `jira-cli release-notes [--version VERSION | --since DATE] [--audience internal|external]`

**How it works:**
1. Query issues by fixVersion or resolved date range
2. Group by issue type (feature, bug fix, improvement)
3. Send to Claude with audience context
4. Claude generates polished, user-facing release notes
5. Output as markdown

**JIRA API:**
- `POST /rest/api/3/search/jql` — JQL with `fixVersion = X` or `resolved >= DATE`

**New/changed files:**
- `cmd/release_notes.go` — command wiring
- `internal/llm/release_notes.go` — prompt + response struct
- `internal/format/markdown.go` — add `WriteReleaseNotes()`

**Implementation steps:**
1. Add release notes prompt in `internal/llm/release_notes.go` — differentiate
   internal vs external audience tone
2. Add `GenerateReleaseNotes()` function
3. Add `cmd/release_notes.go` with version/date/audience flags
4. Query JIRA for matching issues
5. Group issues by type, send to LLM
6. Write formatted release notes markdown

---

## 7. Duplicate / Related Issue Detection

Find semantically similar issues when creating or viewing a ticket.

**Command:** `jira-cli find-similar <ISSUE-KEY>` or `jira-cli find-similar --text "description"`

**How it works:**
1. Get the target issue's summary + description (or use provided text)
2. Fetch recent open issues in the project
3. Send both to Claude: "which of these existing issues are related or duplicates?"
4. Claude returns ranked list with similarity reasoning
5. Display matches with links

**JIRA API:**
- `GET /rest/api/3/issue/{key}` — target issue
- `POST /rest/api/3/search/jql` — candidate issues

**New/changed files:**
- `cmd/find_similar.go` — command wiring
- `internal/llm/similarity.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplaySimilarIssues()`

**Implementation steps:**
1. Add similarity prompt in `internal/llm/similarity.go` — ask for ranked matches
   with confidence and reasoning
2. Add `GenerateSimilarityCheck()` function
3. Fetch target issue + recent project issues (from cache or API)
4. Send to LLM, parse ranked results
5. Display in terminal with issue links and match reasoning
6. Wire up `cmd/find_similar.go`

---

## 8. Workload / Risk Analysis

Analyze assigned issues and flag risks: overload, stale tickets, deadline pressure.

**Command:** `jira-cli workload [--user USER] [--threshold N]`

**How it works:**
1. Fetch all open issues for user (from cache or API)
2. Calculate metrics: count by priority, age distribution, status distribution
3. Send to Claude for risk assessment
4. Claude identifies: overload signals, stale tickets, priority imbalance, suggestions
5. Display risk dashboard

**JIRA API:**
- Uses existing `GetOpenIssues()` or cache

**New/changed files:**
- `cmd/workload.go` — command wiring
- `internal/llm/workload.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplayWorkload()`

**Implementation steps:**
1. Build metrics from open issues: count by priority, average age, status breakdown
2. Add workload analysis prompt in `internal/llm/workload.go`
3. Add `GenerateWorkloadAnalysis()` — takes metrics, returns risk assessment
4. Add terminal display with color-coded risk indicators
5. Wire up `cmd/workload.go` — can use cache data if available

---

## 9. Smart Ticket Creation from Context

Create tickets from natural language, meeting notes, or pasted text. LLM extracts
structured fields.

**Command:** `jira-cli create-ticket [--from-text "..."] [--from-file notes.txt] [--type story|bug|task]`

**How it works:**
1. Accept freeform text (flag, file, or stdin)
2. Send to Claude with project context
3. Claude extracts: summary, description, type, priority, labels, components
4. Preview extracted fields, confirm
5. Create issue in JIRA

**JIRA API:**
- `POST /rest/api/3/issue` — extends existing `CreateEpic()` to handle any type

**New/changed files:**
- `cmd/create_ticket.go` — command wiring
- `internal/jira/client.go` — generalize `CreateEpic()` to `CreateIssue(type, ...)`
- `internal/llm/ticket.go` — prompt + response struct

**Implementation steps:**
1. Generalize `CreateEpic()` into `CreateIssue(issueType, summary, desc, priority, labels)`
2. Add ticket extraction prompt in `internal/llm/ticket.go`
3. Add `ExtractTicketFromText()` function
4. Add `cmd/create_ticket.go` with text/file/stdin input
5. Reuse preview + confirm flow from create-epic
6. Update `create-epic` to use generalized `CreateIssue()`

---

## 10. Cross-Issue Dependency Mapper

Analyze issue descriptions and comments to surface implicit dependencies.

**Command:** `jira-cli dependencies [--scope sprint|backlog|all]`

**How it works:**
1. Fetch issues in scope (current sprint, backlog, or all open)
2. For each issue, get description + comments
3. Send batch to Claude: "identify implicit dependencies between these issues"
4. Claude returns dependency graph: issue A blocks/depends-on issue B, with reasoning
5. Display as dependency list or simple ASCII graph

**JIRA API:**
- `POST /rest/api/3/search/jql` — issues in scope
- `GET /rest/api/3/issue/{key}` — full descriptions
- `GET /rest/api/3/issue/{key}/comment` — comments for context

**New/changed files:**
- `cmd/dependencies.go` — command wiring
- `internal/jira/client.go` — add `GetIssueWithComments()` if not already present
- `internal/llm/dependencies.go` — prompt + response struct
- `internal/format/terminal.go` — add `DisplayDependencies()`

**Implementation steps:**
1. Add `GetIssueDetails(key)` to fetch full description + comments
2. Add dependency analysis prompt in `internal/llm/dependencies.go`
3. Add `AnalyzeDependencies()` — takes issue batch, returns edges + reasoning
4. Add terminal display (list or simple graph format)
5. Wire up `cmd/dependencies.go` with scope flag
6. Consider chunking large issue sets to fit context window

---

## Shared Infrastructure Needed

Several features need common additions:

### JIRA Client Extensions
- `GetIssue(key)` — single issue with full fields (needed by: 2, 4, 7, 10)
- `GetComments(key)` — issue comments (needed by: 4, 10)
- `UpdateIssue(key, fields)` — partial update (needed by: 2)
- `CreateIssue(type, ...)` — generalized creation (needed by: 9, refactors existing)
- `GetClosedSprints(boardID)` — closed sprints (needed by: 5)
- `GetSprintIssues(sprintID)` — all issues in sprint (needed by: 5)

### LLM Module Pattern
Each feature follows the same pattern as the existing EPIC generation:
1. Define a response struct in `internal/llm/<feature>.go`
2. Define a system prompt constant
3. Add a `Generate<Feature>()` function that calls Claude and parses JSON
4. Reuse the same Vertex AI client setup from `llm.go`

### Suggested Implementation Order
1. **Standup** — simplest, reuses existing queries, high daily value
2. **Enrich** — needs `GetIssue` + `UpdateIssue`, moderate complexity
3. **Query (NL JQL)** — reuses existing search, just adds LLM translation
4. **Comment Summarizer** — needs `GetComments`, straightforward
5. **Retro** — needs sprint API extensions, moderate
6. **Release Notes** — needs version/date queries, moderate
7. **Find Similar** — needs batch comparison logic
8. **Workload** — can use cache, analysis-focused
9. **Smart Ticket** — generalizes create-epic, moderate
10. **Dependencies** — most complex, needs full issue details + chunking
