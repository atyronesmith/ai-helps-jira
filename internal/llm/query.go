package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

type QueryResult struct {
	JQL   string `json:"jql"`
	Route string `json:"route,omitempty"`
	Days  int    `json:"days,omitempty"` // optional time window extracted from natural language
}

// QueryResult.Route values, one per translation tier, so logs and the web
// dashboard can tell the tiers apart:
//
//   - RouteFastPath — the deterministic nl2jql parser handled the query (no LLM).
//   - RouteFallback — the nl2jql strict, schema-scoped LLM fallback (one-line JQL).
//   - RouteJSON      — the original JSON-prompt LLM path: the last-resort
//     safety net and the only tier that still extracts a comment-window "Days" value.
//
// All three are re-exported from the nl2jql engine, the single source of truth
// since it owns Result.Route and has no llm dependency; the fast path and the
// strict fallback are stamped by that engine, while generateJQLLLM stamps
// RouteJSON. Keeping the tiers distinct stops the two LLM tiers collapsing into
// the same "fallback" label.
const (
	// All three route labels are re-exported from the nl2jql engine, the leaf
	// package that owns Result.Route, so the two packages share one source of
	// truth and cannot drift. This engine stamps RouteFastPath and RouteFallback;
	// generateJQLLLM stamps RouteJSON.
	RouteFastPath = nl2jql.RouteFastPath
	RouteFallback = nl2jql.RouteFallback
	RouteJSON     = nl2jql.RouteJSON
)

const QuerySystemPrompt = `You are a JIRA JQL expert. Translate the user's natural-language query into valid JQL.

Context:
- The JIRA project key is %s
- The current user is referred to as %s in JQL
- Today's date is %s

Common JQL fields: project, assignee, reporter, status, priority, issuetype, labels, summary, description, created, updated, resolved, sprint, fixVersion, component.

Common statuses: "To Do", "In Progress", "In Review", "Done", "Closed", "Resolved".
Common priorities: Highest, High, Medium, Low, Lowest.
Common issue types: Bug, Story, Task, Epic, Sub-task.

Date functions: now(), startOfDay(), startOfWeek(), startOfMonth(), endOfDay(), endOfWeek(), endOfMonth().
Relative dates: "-1w" (one week ago), "-1d" (one day ago), "-2w", "-1m", etc.

Rules:
1. Always scope to project = %s unless the user explicitly asks about a different project or all projects.
2. If the user mentions "my" or "me", use assignee = %s.
3. If the user mentions a specific person by name or username, use assignee = "name" or assignee = "email".
4. Use ORDER BY when it improves readability (e.g. priority ASC, created DESC).
5. If the user mentions a time window for viewing changes or activity (e.g. "last day", "past 2 weeks", "since Monday"), include a "days" field with the number of days. Do NOT put time constraints in the JQL for this — the days field controls a separate comment filter. Only use date constraints in JQL when filtering by issue creation/update dates. Omit the "days" field if no time window is mentioned.
6. "Open" or "unresolved" issues means status NOT IN ("Done", "Closed", "Resolved") — do NOT enumerate the open statuses individually, and never include Done/Closed/Resolved when the user asked for open work.
7. JQL is left-to-right with no implicit operator precedence like SQL: AND and OR are evaluated strictly left to right unless you add parentheses. ALWAYS wrap any OR-combined group in parentheses when it is combined with AND, e.g. project = X AND assignee = currentUser() AND (status = "To Do" OR status = "In Progress") — never emit project = X AND assignee = currentUser() AND status = "To Do" OR status = "In Progress", which silently drops the project/assignee scoping from the OR branches.
8. Respond ONLY with valid JSON (no markdown fences): {"jql": "your JQL here"} or {"jql": "your JQL here", "days": 1}`

// generateJQLLLM is the original "always LLM" JQL translation: it builds the
// schema-aware JSON prompt and parses {jql, days}. It is now the ultimate
// fallback inside GenerateJQL (see jqltranslate.go) and the only path that
// still extracts a comment-window "Days" value. It stamps QueryResult.Route
// with RouteJSON so this JSON-prompt LLM tier is distinguishable from the
// nl2jql strict fallback (RouteFallback) in logs and the dashboard. Before
// returning it hard-scopes the JQL with ensureProjectScope, mirroring the
// nl2jql pipeline so the tier no longer relies on QuerySystemPrompt rule #1.
func generateJQLLLM(ctx context.Context, cfg *config.Config, naturalQuery string) (*QueryResult, error) {
	slog.Info("generating JQL", "query", naturalQuery)

	provider, err := NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM provider: %w", err)
	}

	today := time.Now().Format("2006-01-02")
	systemPrompt := fmt.Sprintf(QuerySystemPrompt,
		cfg.JiraProject, cfg.Assignee(), today,
		cfg.JiraProject, cfg.Assignee(),
	)

	text, err := provider.Complete(ctx, systemPrompt, naturalQuery, 1024)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}
	slog.Debug("raw LLM response", "text", text)

	var result QueryResult
	if err := json.Unmarshal([]byte(cleanJSON(text)), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}
	if result.JQL == "" {
		return nil, fmt.Errorf("LLM returned empty JQL")
	}

	// Hard-scope to the tenant's project the way the nl2jql pipeline does, so
	// this JSON-prompt tier no longer depends on QuerySystemPrompt rule #1
	// being obeyed: a model that omits the project predicate still gets the
	// default appended, and an explicit project is preserved (ensureProjectScope
	// is idempotent, so it never double-scopes).
	result.JQL = ensureProjectScope(result.JQL, cfg)
	result.Route = RouteJSON
	slog.Info("generated JQL", "jql", result.JQL, "route", result.Route)
	return &result, nil
}
