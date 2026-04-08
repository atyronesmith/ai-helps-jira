package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/confluence"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

// defaultConfluenceParent is the page ID for the weekly status parent page.
// Set via CONFLUENCE_PARENT_PAGE env var or confluence_parent_id tool parameter.
const defaultConfluenceParent = ""

// Handlers holds shared state for MCP tool handlers.
type Handlers struct {
	store   *ResultStore
	webPort int
}

// NewHandlers creates a new handler set.
func NewHandlers(store *ResultStore, webPort int) *Handlers {
	return &Handlers{store: store, webPort: webPort}
}

func (h *Handlers) viewURL(id string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/view/%s", h.webPort, id)
}

// --- Tool Definitions ---

func summaryToolDef() mcp.Tool {
	return mcp.NewTool("jira_summary",
		mcp.WithDescription("Show daily summary of assigned JIRA issues and sprint info. Returns text + URL for rich HTML dashboard."),
		mcp.WithString("user", mcp.Description("JIRA user email. Defaults to currentUser().")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithBoolean("refresh", mcp.Description("Force full fetch, bypass cache.")),
		mcp.WithBoolean("cache_only", mcp.Description("Use cached data only, no API calls.")),
	)
}

func queryToolDef() mcp.Tool {
	return mcp.NewTool("jira_query",
		mcp.WithDescription("Search JIRA using natural language. Translates to JQL via LLM, executes, returns results with rich HTML view."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural language query (e.g. 'show me all critical bugs from last week').")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithNumber("max_results", mcp.Description("Maximum results. Default 50.")),
	)
}

func digestToolDef() mcp.Tool {
	return mcp.NewTool("jira_digest",
		mcp.WithDescription("Generate executive digest for Features/Initiatives. Walks hierarchy, fetches comments, produces AI summary. Returns text + rich dashboard URL."),
		mcp.WithString("issues", mcp.Required(), mcp.Description("Space-separated issue keys (e.g. 'FEAT-123 FEAT-456') or natural language query.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithNumber("days", mcp.Description("Include comments from last N days. Default: since last run or 7 days.")),
		mcp.WithBoolean("cache_only", mcp.Description("Use cached data only.")),
	)
}

func enrichToolDef() mcp.Tool {
	return mcp.NewTool("jira_enrich",
		mcp.WithDescription("Enrich a sparse JIRA ticket with AI-generated description, acceptance criteria, labels, priority. Use apply=true to write to JIRA."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithBoolean("apply", mcp.Description("Apply enrichment to JIRA. Default false.")),
	)
}

func weeklyStatusToolDef() mcp.Tool {
	return mcp.NewTool("jira_weekly_status",
		mcp.WithDescription("Generate a formatted weekly status report from JIRA activity. Queries user's work for a date range, fetches issue details and comments, uses AI to produce narrative status bullets grouped by project/epic. Optionally posts to Confluence."),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithString("start_date", mcp.Description("Start of reporting period (YYYY-MM-DD). Defaults to Monday of current week.")),
		mcp.WithString("end_date", mcp.Description("End of reporting period (YYYY-MM-DD). Defaults to Friday of current week.")),
		mcp.WithBoolean("confluence", mcp.Description("Post report to Confluence. Default false.")),
		mcp.WithString("confluence_parent_id", mcp.Description("Confluence parent page ID. Defaults to configured parent.")),
	)
}

func createEpicToolDef() mcp.Tool {
	return mcp.NewTool("jira_create_epic",
		mcp.WithDescription("Create a JIRA EPIC with LLM-generated content from a brief description."),
		mcp.WithString("description", mcp.Required(), mcp.Description("Brief description of what the EPIC should accomplish.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

// --- Helper functions ---

func getString(req mcp.CallToolRequest, key string) string {
	args := req.GetArguments()
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}

func getBool(req mcp.CallToolRequest, key string) bool {
	args := req.GetArguments()
	if args == nil {
		return false
	}
	b, _ := args[key].(bool)
	return b
}

func getFloat(req mcp.CallToolRequest, key string) float64 {
	args := req.GetArguments()
	if args == nil {
		return 0
	}
	f, _ := args[key].(float64)
	return f
}

func loadConfig(req mcp.CallToolRequest) (*config.Config, error) {
	return config.Load(getString(req, "user"), getString(req, "project"))
}

func loadJIRAConfig(req mcp.CallToolRequest) (*config.Config, error) {
	return config.LoadJIRAOnly(getString(req, "user"), getString(req, "project"))
}

func getStringSlice(req mcp.CallToolRequest, key string) []string {
	args := req.GetArguments()
	if args == nil {
		return nil
	}
	arr, ok := args[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func errorResult(msg string) *mcp.CallToolResult {
	return mcp.NewToolResultError(msg)
}

func textResult(text string) *mcp.CallToolResult {
	return mcp.NewToolResultText(text)
}

// --- Handlers ---

func (h *Handlers) HandleSummary(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	db, err := cache.Open()
	if err != nil {
		return errorResult(err.Error()), nil
	}
	defer db.Close()

	refresh := getBool(req, "refresh")
	cacheOnly := getBool(req, "cache_only")

	if refresh {
		if err := db.Clear(cfg.JiraProject); err != nil {
			return errorResult(err.Error()), nil
		}
	}

	lastFetch := db.LastFetch(cfg.JiraProject, cfg.Assignee())

	var boards []jira.BoardInfo
	var openIssues []jira.Issue

	if cacheOnly {
		if lastFetch.IsZero() {
			return errorResult(fmt.Sprintf("no cached data for project %s", cfg.JiraProject)), nil
		}
		boards, err = db.GetBoards(cfg.JiraProject)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		openIssues, err = db.GetIssues(cfg.JiraProject)
		if err != nil {
			return errorResult(err.Error()), nil
		}
	} else if !lastFetch.IsZero() && !refresh {
		// Delta fetch
		since := lastFetch.Add(-5 * time.Minute)
		client, err := jira.NewClient(cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		delta, err := client.GetOpenIssuesSince(since)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		if len(delta) > 0 {
			db.UpsertIssues(cfg.JiraProject, delta)
		}
		db.RemoveDone(cfg.JiraProject)
		db.LogFetch(cfg.JiraProject, cfg.Assignee())
		boards, err = db.GetBoards(cfg.JiraProject)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		openIssues, err = db.GetIssues(cfg.JiraProject)
		if err != nil {
			return errorResult(err.Error()), nil
		}
	} else {
		// Full fetch
		client, err := jira.NewClient(cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		boards, err = client.GetBoardIssues()
		if err != nil {
			return errorResult(err.Error()), nil
		}
		openIssues, err = client.GetOpenIssues()
		if err != nil {
			return errorResult(err.Error()), nil
		}
		var allIssues []jira.Issue
		for _, b := range boards {
			allIssues = append(allIssues, b.Issues...)
		}
		allIssues = append(allIssues, openIssues...)
		if len(allIssues) > 0 {
			db.UpsertIssues(cfg.JiraProject, allIssues)
		}
		db.RemoveDone(cfg.JiraProject)
		db.LogFetch(cfg.JiraProject, cfg.Assignee())
	}

	slog.Info("summary complete", "boards", len(boards), "open_issues", len(openIssues))

	// Store for web
	resultID := h.store.Save(ResultSummary, fmt.Sprintf("Summary: %s", cfg.JiraProject), &SummaryResult{
		Boards:     boards,
		OpenIssues: openIssues,
		Project:    cfg.JiraProject,
		User:       cfg.Assignee(),
		JiraServer: cfg.JiraServer,
		FetchedAt:  time.Now(),
	})

	// Build text response
	var text strings.Builder
	fmt.Fprintf(&text, "## JIRA Summary: %s\n\n", cfg.JiraProject)
	for _, board := range boards {
		if board.BoardType == "scrum" && board.SprintName != "" {
			fmt.Fprintf(&text, "### Sprint: %s (%s)\n", board.SprintName, board.Name)
		} else {
			fmt.Fprintf(&text, "### Kanban: %s\n", board.Name)
		}
		for _, issue := range board.Issues {
			fmt.Fprintf(&text, "- [%s](%s/browse/%s) [%s] (%s) %s\n",
				issue.Key, cfg.JiraServer, issue.Key, issue.Status, issue.Priority, issue.Summary)
		}
		text.WriteString("\n")
	}
	if len(openIssues) > 0 {
		fmt.Fprintf(&text, "### All Open Issues (%d)\n", len(openIssues))
		for _, issue := range openIssues {
			fmt.Fprintf(&text, "- [%s](%s/browse/%s) [%s] (%s) %s\n",
				issue.Key, cfg.JiraServer, issue.Key, issue.Status, issue.Priority, issue.Summary)
		}
	}
	fmt.Fprintf(&text, "\n---\nRich dashboard: %s\n", h.viewURL(resultID))

	return textResult(text.String()), nil
}

func (h *Handlers) HandleQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	naturalQuery := getString(req, "query")
	maxResults := int(getFloat(req, "max_results"))
	if maxResults <= 0 {
		maxResults = 50
	}

	slog.Info("query", "input", naturalQuery)

	// Translate to JQL
	result, err := llm.GenerateJQL(cfg, naturalQuery)
	if err != nil {
		return errorResult(fmt.Sprintf("JQL generation failed: %v", err)), nil
	}
	slog.Info("JQL generated", "jql", result.JQL)

	// Execute search
	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	issues, err := client.SearchJQL(result.JQL, maxResults)
	if err != nil {
		return errorResult(fmt.Sprintf("JIRA search failed (JQL: %s): %v", result.JQL, err)), nil
	}

	// Cache results
	if len(issues) > 0 {
		db, err := cache.Open()
		if err == nil {
			db.UpsertIssues(cfg.JiraProject, issues)
			db.Close()
		}
	}

	// Store for web
	resultID := h.store.Save(ResultQuery, fmt.Sprintf("Query: %s", naturalQuery), &QueryResultData{
		Query:      naturalQuery,
		JQL:        result.JQL,
		Issues:     issues,
		User:       cfg.Assignee(),
		JiraServer: cfg.JiraServer,
		QueriedAt:  time.Now(),
	})

	// Build text
	var text strings.Builder
	fmt.Fprintf(&text, "## Query: %s\n\n", naturalQuery)
	fmt.Fprintf(&text, "**JQL:** `%s`\n\n", result.JQL)
	if len(issues) == 0 {
		text.WriteString("No issues found.\n")
	} else {
		fmt.Fprintf(&text, "**%d issues found:**\n\n", len(issues))
		for _, issue := range issues {
			fmt.Fprintf(&text, "- [%s](%s/browse/%s) [%s] (%s) %s\n",
				issue.Key, cfg.JiraServer, issue.Key, issue.Status, issue.Priority, issue.Summary)
		}
	}
	fmt.Fprintf(&text, "\n---\nRich dashboard: %s\n", h.viewURL(resultID))

	return textResult(text.String()), nil
}

var issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

func (h *Handlers) HandleDigest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	db, err := cache.Open()
	if err != nil {
		return errorResult(err.Error()), nil
	}
	defer db.Close()

	issuesArg := getString(req, "issues")
	cacheOnly := getBool(req, "cache_only")
	daysArg := int(getFloat(req, "days"))

	// Parse issue keys
	args := strings.Fields(issuesArg)
	allKeys := true
	for _, a := range args {
		if !issueKeyPattern.MatchString(a) {
			allKeys = false
			break
		}
	}

	var issueKeys []string
	var queryKey string
	var nlDays int

	if allKeys {
		issueKeys = args
		queryKey = "keys:" + strings.Join(args, ",")
	} else {
		if cacheOnly {
			return errorResult("--cache_only requires explicit issue keys, not natural language"), nil
		}
		naturalQuery := strings.Join(args, " ")
		queryKey = "nl:" + naturalQuery

		result, err := llm.GenerateJQL(cfg, naturalQuery)
		if err != nil {
			return errorResult(fmt.Sprintf("JQL generation failed: %v", err)), nil
		}
		nlDays = result.Days
		slog.Info("digest JQL", "jql", result.JQL, "days", result.Days)

		client, err := jira.NewClient(cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		issues, err := client.SearchJQL(result.JQL, 50)
		if err != nil {
			return errorResult(fmt.Sprintf("JIRA search failed (JQL: %s): %v", result.JQL, err)), nil
		}
		for _, issue := range issues {
			issueKeys = append(issueKeys, issue.Key)
		}
	}

	if len(issueKeys) == 0 {
		return textResult("No issues found."), nil
	}

	// Determine "since" cutoff
	var since time.Time
	if daysArg > 0 {
		since = time.Now().AddDate(0, 0, -daysArg)
	} else if nlDays > 0 {
		since = time.Now().AddDate(0, 0, -nlDays)
	} else {
		lastRun := db.LastDigestRun(queryKey)
		if !lastRun.IsZero() {
			since = lastRun
		} else {
			since = time.Now().AddDate(0, 0, -7)
		}
	}

	// Run digest for each key and aggregate results
	var text strings.Builder
	for _, key := range issueKeys {
		result, err := h.runSingleDigest(cfg, db, key, since, cacheOnly)
		if err != nil {
			fmt.Fprintf(&text, "**%s**: Error: %v\n\n", key, err)
			slog.Error("digest error", "key", key, "error", err)
			continue
		}
		if result == nil {
			fmt.Fprintf(&text, "**%s**: No linked issues or no recent comments.\n\n", key)
			continue
		}

		// Store for web
		resultID := h.store.Save(ResultDigest, fmt.Sprintf("Digest: %s", key), result)

		// Append text
		fmt.Fprintf(&text, "## Digest: %s — %s\n\n", result.ParentKey, result.ParentSummary)
		fmt.Fprintf(&text, "**Overall Status:** %s\n\n", result.Digest.OverallStatus)

		if len(result.Digest.Progress) > 0 {
			text.WriteString("### Progress Updates\n")
			for _, p := range result.Digest.Progress {
				fmt.Fprintf(&text, "- **%s** [%s]: %s\n", p.EpicKey, p.Status, p.Update)
			}
			text.WriteString("\n")
		}
		if len(result.Digest.Blockers) > 0 {
			text.WriteString("### Blockers\n")
			for _, b := range result.Digest.Blockers {
				fmt.Fprintf(&text, "- **%s**: %s", b.EpicKey, b.Blocker)
				if b.Impact != "" {
					fmt.Fprintf(&text, " (Impact: %s)", b.Impact)
				}
				text.WriteString("\n")
			}
			text.WriteString("\n")
		}
		if len(result.Digest.NotStarted) > 0 {
			text.WriteString("### Not Started\n")
			for _, n := range result.Digest.NotStarted {
				fmt.Fprintf(&text, "- **%s**: %s — %s\n", n.EpicKey, n.EpicSummary, n.Reason)
			}
			text.WriteString("\n")
		}
		fmt.Fprintf(&text, "**Summary:** %s\n\n", result.Digest.Summary)
		fmt.Fprintf(&text, "Rich dashboard: %s\n\n---\n\n", h.viewURL(resultID))
	}

	// Log digest run
	db.LogDigestRun(queryKey)

	return textResult(text.String()), nil
}

// runSingleDigest processes one issue key for digest.
func (h *Handlers) runSingleDigest(cfg *config.Config, db *cache.Cache, issueKey string, since time.Time, cacheOnly bool) (*DigestResultData, error) {
	slog.Info("digest", "key", issueKey, "since", since)

	var parent *jira.IssueDetail
	var links []jira.IssueLink
	var allComments []jira.Comment

	if cacheOnly {
		cachedLinks, cachedComments, err := collectEpicsCached(db, issueKey, since, 0, make(map[string]bool))
		if err != nil {
			return nil, err
		}
		if len(cachedLinks) == 0 {
			return nil, fmt.Errorf("no cached data for %s", issueKey)
		}
		links = cachedLinks
		allComments = cachedComments
		parent = &jira.IssueDetail{Key: issueKey, Summary: "(cached)"}
	} else {
		client, err := jira.NewClient(cfg)
		if err != nil {
			return nil, err
		}
		p, _, err := client.GetIssueWithLinks(issueKey)
		if err != nil {
			return nil, err
		}
		parent = p
		links, allComments, err = collectEpics(client, db, issueKey, since, 0, make(map[string]bool))
		if err != nil {
			return nil, err
		}
	}

	if len(links) == 0 || len(allComments) == 0 {
		return nil, nil
	}

	// Generate digest with LLM
	digest, err := llm.GenerateDigest(cfg, parent, links, allComments)
	if err != nil {
		return nil, err
	}

	// Convert to format types
	displayDigest := &format.DigestData{
		OverallStatus: digest.OverallStatus,
		Summary:       digest.Summary,
	}
	for _, p := range digest.ProgressUpdates {
		displayDigest.Progress = append(displayDigest.Progress, format.DigestProgress{
			EpicKey: p.EpicKey, EpicSummary: p.EpicSummary,
			Status: p.Status, Update: p.Update,
		})
	}
	for _, b := range digest.Blockers {
		displayDigest.Blockers = append(displayDigest.Blockers, format.DigestBlocker{
			EpicKey: b.EpicKey, EpicSummary: b.EpicSummary,
			Blocker: b.Blocker, Impact: b.Impact,
		})
	}
	for _, n := range digest.NotStarted {
		displayDigest.NotStarted = append(displayDigest.NotStarted, format.DigestNotStarted{
			EpicKey: n.EpicKey, EpicSummary: n.EpicSummary, Reason: n.Reason,
		})
	}

	return &DigestResultData{
		ParentKey:     parent.Key,
		ParentSummary: parent.Summary,
		ParentType:    parent.IssueType,
		Digest:        displayDigest,
		Links:         links,
		User:          cfg.Assignee(),
		JiraServer:    cfg.JiraServer,
		GeneratedAt:   time.Now(),
	}, nil
}

func (h *Handlers) HandleEnrich(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	issueKey := getString(req, "issue_key")
	apply := getBool(req, "apply")

	slog.Info("enrich", "key", issueKey)

	prompt, _ := llm.LoadEnrichPrompt()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	issue, err := client.GetIssue(issueKey)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	enrichment, err := llm.GenerateEnrichment(cfg, issue, prompt)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if apply {
		fullDesc := llm.BuildEnrichedDescription(enrichment)
		if err := client.UpdateIssue(issueKey, fullDesc, enrichment.Labels); err != nil {
			return errorResult(fmt.Sprintf("applied failed: %v", err)), nil
		}
	}

	resultID := h.store.Save(ResultEnrich, fmt.Sprintf("Enrich: %s", issueKey), &EnrichResultData{
		Issue:      issue,
		Enrichment: enrichment,
		Applied:    apply,
		JiraServer: cfg.JiraServer,
		EnrichedAt: time.Now(),
	})

	var text strings.Builder
	fmt.Fprintf(&text, "## Enrichment: %s — %s\n\n", issue.Key, issue.Summary)
	fmt.Fprintf(&text, "**Suggested Description:**\n%s\n\n", enrichment.Description)
	if len(enrichment.AcceptanceCriteria) > 0 {
		text.WriteString("**Acceptance Criteria:**\n")
		for _, c := range enrichment.AcceptanceCriteria {
			fmt.Fprintf(&text, "- %s\n", c)
		}
		text.WriteString("\n")
	}
	fmt.Fprintf(&text, "**Priority:** %s -> %s\n", issue.Priority, enrichment.Priority)
	fmt.Fprintf(&text, "**Labels:** %s\n\n", strings.Join(enrichment.Labels, ", "))
	if apply {
		text.WriteString("Changes have been applied to JIRA.\n\n")
	} else {
		text.WriteString("Preview only. Set apply=true to update JIRA.\n\n")
	}
	fmt.Fprintf(&text, "---\nRich dashboard: %s\n", h.viewURL(resultID))

	return textResult(text.String()), nil
}

func (h *Handlers) HandleWeeklyStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Parse date range — default to current week (Mon-Fri)
	startStr := getString(req, "start_date")
	endStr := getString(req, "end_date")

	now := time.Now()
	if startStr == "" {
		// Find Monday of current week
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		monday := now.AddDate(0, 0, -(weekday - 1))
		startStr = monday.Format("2006-01-02")
	}
	if endStr == "" {
		// Find Friday of current week
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		friday := now.AddDate(0, 0, 5-weekday)
		endStr = friday.Format("2006-01-02")
	}

	startTime, err := time.Parse("2006-01-02", startStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid start_date: %v", err)), nil
	}
	// End time is end of day
	endTime, err := time.Parse("2006-01-02", endStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid end_date: %v", err)), nil
	}
	endTime = endTime.Add(24*time.Hour - time.Second)

	slog.Info("weekly status", "user", cfg.Assignee(), "start", startStr, "end", endStr)

	db, err := cache.Open()
	if err != nil {
		return errorResult(err.Error()), nil
	}
	defer db.Close()

	// Always run the fast search query to get current updated timestamps
	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	assigneeJQL := cfg.Assignee()
	if email := cfg.AssigneeEmail(); email != "" {
		assigneeJQL = fmt.Sprintf("%q", email)
	}
	jql := fmt.Sprintf(
		`project = %s AND updatedBy = %s AND updated >= "%s" ORDER BY updated DESC`,
		cfg.JiraProject, assigneeJQL, startStr,
	)

	issues, err := client.SearchJQL(jql, 100)
	if err != nil {
		return errorResult(fmt.Sprintf("JIRA search failed: %v", err)), nil
	}

	if len(issues) == 0 {
		return textResult("No issues found for the specified date range."), nil
	}

	// Find the most recent update timestamp across all issues
	var latestUpdate time.Time
	for _, issue := range issues {
		if issue.Updated.After(latestUpdate) {
			latestUpdate = issue.Updated
		}
	}

	// Check cache — use it only if no issue has been updated since we last cached
	cacheKey := fmt.Sprintf("weekly:%s:%s:%s", cfg.JiraProject, startStr, endStr)
	var status *llm.WeeklyStatusContent

	if cachedJSON, cachedAt, ok := db.GetWeeklyCache(cacheKey); ok {
		if !latestUpdate.After(cachedAt) {
			// Nothing changed in JIRA since we cached — use cached result
			if err := json.Unmarshal([]byte(cachedJSON), &status); err == nil {
				slog.Info("using cached weekly status (no JIRA changes)", "key", cacheKey,
					"cached_at", cachedAt.Format(time.RFC3339),
					"latest_update", latestUpdate.Format(time.RFC3339))
			} else {
				slog.Warn("failed to unmarshal cached result, will regenerate", "error", err)
				status = nil
			}
		} else {
			slog.Info("cache stale, JIRA data updated since last run",
				"cached_at", cachedAt.Format(time.RFC3339),
				"latest_update", latestUpdate.Format(time.RFC3339))
		}
	}

	if status == nil {
		// Cache miss or stale — fetch details, comments, and run LLM
		db.UpsertIssues(cfg.JiraProject, issues)

		var items []llm.IssueWithComments
		for _, issue := range issues {
			detail, err := client.GetIssue(issue.Key)
			if err != nil {
				slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
				continue
			}

			comments, err := client.GetComments(issue.Key)
			if err != nil {
				slog.Warn("failed to get comments", "key", issue.Key, "error", err)
			}
			if len(comments) > 0 {
				db.UpsertComments(comments)
			}

			// Filter comments to date range
			var relevantComments []jira.Comment
			for _, c := range comments {
				if c.Created.After(startTime) && c.Created.Before(endTime) {
					relevantComments = append(relevantComments, c)
				}
			}

			hasActivity := len(relevantComments) > 0 || (issue.Updated.After(startTime) && issue.Updated.Before(endTime))
			if !hasActivity {
				continue
			}

			item := llm.IssueWithComments{
				Issue:    *detail,
				Comments: relevantComments,
			}
			if detail.ParentKey != "" {
				item.Parent = &jira.IssueDetail{
					Key:     detail.ParentKey,
					Summary: detail.ParentSummary,
				}
			}
			items = append(items, item)
		}

		if len(items) == 0 {
			return textResult("No issues with activity found in the specified date range."), nil
		}

		status, err = llm.GenerateWeeklyStatus(cfg, items, startStr, endStr)
		if err != nil {
			return errorResult(fmt.Sprintf("LLM generation failed: %v", err)), nil
		}

		// Cache the full result
		if resultJSON, err := json.Marshal(status); err == nil {
			if err := db.SetWeeklyCache(cacheKey, string(resultJSON)); err != nil {
				slog.Warn("failed to cache weekly result", "error", err)
			}
		}
	}

	// Store for web dashboard
	resultID := h.store.Save(ResultWeeklyStatus, fmt.Sprintf("Weekly Status: %s to %s", startStr, endStr), &WeeklyStatusResultData{
		UserName:    status.UserName,
		StartDate:   startStr,
		EndDate:     endStr,
		Projects:    status.Projects,
		JiraServer:  cfg.JiraServer,
		GeneratedAt: time.Now(),
	})

	// Build markdown text output in the established format
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n", status.UserName)
	for _, proj := range status.Projects {
		fmt.Fprintf(&text, "* %s ([%s](%s/browse/%s))\n", proj.ProjectName, proj.IssueKey, cfg.JiraServer, proj.IssueKey)
		for _, bullet := range proj.Bullets {
			fmt.Fprintf(&text, "  * %s\n", bullet)
		}
	}
	fmt.Fprintf(&text, "\n---\nRich dashboard: %s\n", h.viewURL(resultID))

	// Post to Confluence if requested
	postToConfluence := getBool(req, "confluence")
	if postToConfluence {
		parentID := getString(req, "confluence_parent_id")
		if parentID == "" {
			parentID = defaultConfluenceParent
		}
		if parentID == "" {
			return errorResult("confluence_parent_id is required (no default configured)"), nil
		}

		confClient := confluence.NewClient(cfg)
		pageTitle := fmt.Sprintf("Weekly Status: %s to %s", startStr, endStr)
		storageBody := confluence.WeeklyStatusToStorage(status.UserName, cfg.JiraServer, status.Projects)

		// Get parent page to find space ID
		parentPage, err := confClient.GetPage(parentID)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to get confluence parent page: %v", err)), nil
		}

		// Check if a page for this week already exists
		existing, err := confClient.GetPageByTitle(parentPage.SpaceID, pageTitle)
		if err != nil {
			slog.Warn("confluence page search failed", "error", err)
		}

		var childPageID string
		var pageURL string
		if existing != nil {
			// Overwrite existing page
			existingPage, _, err := confClient.GetPageBody(existing.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("failed to get existing page: %v", err)), nil
			}
			if err := confClient.UpdatePage(existing.ID, pageTitle, storageBody, existingPage.Version.Number); err != nil {
				return errorResult(fmt.Sprintf("failed to update confluence page: %v", err)), nil
			}
			childPageID = existing.ID
			pageURL = fmt.Sprintf("%s/wiki/pages/%s", cfg.JiraServer, existing.ID)
			fmt.Fprintf(&text, "\nConfluence page updated: %s\n", pageURL)
		} else {
			// Create new child page
			newPage, err := confClient.CreatePage(parentPage.SpaceID, parentID, pageTitle, storageBody)
			if err != nil {
				return errorResult(fmt.Sprintf("failed to create confluence page: %v", err)), nil
			}
			childPageID = newPage.ID
			pageURL = fmt.Sprintf("%s/wiki/pages/%s", cfg.JiraServer, newPage.ID)
			fmt.Fprintf(&text, "\nConfluence page created: %s\n", pageURL)
		}

		// Update parent page with link to this report (most recent at top)
		parentWithBody, parentBody, err := confClient.GetPageBody(parentID)
		if err != nil {
			slog.Warn("failed to read parent page body", "error", err)
		} else {
			childURL := fmt.Sprintf("%s/wiki/spaces/%s/pages/%s",
				cfg.JiraServer, parentPage.SpaceKey, childPageID)
			entryLink := fmt.Sprintf(
				`<li><a href="%s">%s</a></li>`,
				childURL, pageTitle)

			// Insert new entry at the top of the list, or create a list if none exists
			newBody := confluence.InsertIndexEntry(parentBody, entryLink, pageTitle)
			if err := confClient.UpdatePage(parentID, parentWithBody.Title, newBody, parentWithBody.Version.Number); err != nil {
				slog.Warn("failed to update parent page index", "error", err)
			} else {
				slog.Info("parent page updated with link", "child", childPageID)
			}
		}
	}

	return textResult(text.String()), nil
}

func (h *Handlers) HandleCreateEpic(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	description := getString(req, "description")
	slog.Info("create-epic", "description", description)

	epic, err := llm.GenerateEpicContent(cfg, description)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	fullDesc := llm.BuildDescription(epic)
	issue, err := client.CreateEpic(epic.Summary, fullDesc, epic.Priority, epic.Labels)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	resultID := h.store.Save(ResultCreateEpic, fmt.Sprintf("Epic: %s", issue.Key), &CreateEpicResultData{
		CreatedKey: issue.Key,
		Epic:       epic,
		JiraServer: cfg.JiraServer,
		CreatedAt:  time.Now(),
	})

	var text strings.Builder
	fmt.Fprintf(&text, "## EPIC Created: %s\n\n", issue.Key)
	fmt.Fprintf(&text, "**Summary:** %s\n\n", epic.Summary)
	fmt.Fprintf(&text, "**Description:**\n%s\n\n", epic.Description)
	if len(epic.AcceptanceCriteria) > 0 {
		text.WriteString("**Acceptance Criteria:**\n")
		for _, c := range epic.AcceptanceCriteria {
			fmt.Fprintf(&text, "- %s\n", c)
		}
		text.WriteString("\n")
	}
	fmt.Fprintf(&text, "**Priority:** %s\n", epic.Priority)
	fmt.Fprintf(&text, "**Labels:** %s\n\n", strings.Join(epic.Labels, ", "))
	fmt.Fprintf(&text, "View in JIRA: %s/browse/%s\n\n", cfg.JiraServer, issue.Key)
	fmt.Fprintf(&text, "---\nRich dashboard: %s\n", h.viewURL(resultID))

	return textResult(text.String()), nil
}

// --- Digest hierarchy traversal (reused from cmd/digest.go) ---

func isContainerType(issueType string) bool {
	switch strings.ToLower(issueType) {
	case "initiative", "feature":
		return true
	}
	return false
}

func collectEpics(client *jira.Client, db *cache.Cache, issueKey string,
	since time.Time, depth int, seen map[string]bool) ([]jira.IssueLink, []jira.Comment, error) {

	if depth > 3 || seen[issueKey] {
		return nil, nil, nil
	}
	seen[issueKey] = true

	_, links, err := client.GetIssueWithLinks(issueKey)
	if err != nil {
		return nil, nil, err
	}

	children, err := client.GetChildIssues(issueKey)
	if err != nil {
		slog.Warn("failed to fetch children", "key", issueKey, "error", err)
	} else {
		existing := make(map[string]bool)
		for _, l := range links {
			existing[l.TargetKey] = true
		}
		for _, child := range children {
			if !existing[child.TargetKey] {
				links = append(links, child)
			}
		}
	}

	db.UpsertIssueLinks(links)

	var epicLinks []jira.IssueLink
	var allComments []jira.Comment

	for _, l := range links {
		if seen[l.TargetKey] {
			continue
		}
		if isContainerType(l.TargetType) {
			childLinks, childComments, err := collectEpics(client, db, l.TargetKey, since, depth+1, seen)
			if err != nil {
				slog.Warn("traverse child failed", "key", l.TargetKey, "error", err)
				continue
			}
			epicLinks = append(epicLinks, childLinks...)
			allComments = append(allComments, childComments...)
		} else {
			epicLinks = append(epicLinks, l)
		}
	}

	for _, l := range epicLinks {
		if l.SourceKey != issueKey {
			continue
		}
		comments, err := client.GetComments(l.TargetKey)
		if err != nil {
			slog.Warn("fetch comments failed", "key", l.TargetKey, "error", err)
			continue
		}
		db.UpsertComments(comments)
		for _, c := range comments {
			if c.Created.After(since) {
				allComments = append(allComments, c)
			}
		}
	}

	return epicLinks, allComments, nil
}

func collectEpicsCached(db *cache.Cache, issueKey string, since time.Time,
	depth int, seen map[string]bool) ([]jira.IssueLink, []jira.Comment, error) {

	if depth > 3 || seen[issueKey] {
		return nil, nil, nil
	}
	seen[issueKey] = true

	cachedLinks, err := db.GetIssueLinks(issueKey)
	if err != nil {
		return nil, nil, err
	}

	var epicLinks []jira.IssueLink
	var allComments []jira.Comment

	for _, l := range cachedLinks {
		if seen[l.TargetKey] {
			continue
		}
		if isContainerType(l.TargetType) {
			childLinks, childComments, err := collectEpicsCached(db, l.TargetKey, since, depth+1, seen)
			if err != nil {
				slog.Warn("traverse cached child failed", "key", l.TargetKey, "error", err)
				continue
			}
			epicLinks = append(epicLinks, childLinks...)
			allComments = append(allComments, childComments...)
		} else {
			epicLinks = append(epicLinks, l)
		}
	}

	var leafKeys []string
	for _, l := range epicLinks {
		if l.SourceKey == issueKey {
			leafKeys = append(leafKeys, l.TargetKey)
		}
	}
	if len(leafKeys) > 0 {
		comments, err := db.GetCommentsByKeys(leafKeys, since)
		if err != nil {
			return nil, nil, err
		}
		allComments = append(allComments, comments...)
	}

	return epicLinks, allComments, nil
}
