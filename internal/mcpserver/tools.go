package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/confluence"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

// defaultConfluenceParent is the page ID for the weekly status parent page.
// Set via CONFLUENCE_PARENT_PAGE env var or confluence_parent_id tool parameter.
const defaultConfluenceParent = ""

// Handlers holds shared state for MCP tool handlers.
type Handlers struct {
	store    *ResultStore
	cache    *cache.Cache
	webPort  int
	bindHost string
}

// NewHandlers creates a new handler set.
func NewHandlers(store *ResultStore, db *cache.Cache, webPort int, bindHost string) *Handlers {
	return &Handlers{store: store, cache: db, webPort: webPort, bindHost: bindHost}
}

func (h *Handlers) viewURL(id string) string {
	host := h.bindHost
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/view/%s", host, h.webPort, id)
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
		mcp.WithString("start_date", mcp.Description("Start of reporting period (YYYY-MM-DD). Defaults to 7 days ago.")),
		mcp.WithString("end_date", mcp.Description("End of reporting period (YYYY-MM-DD). Defaults to today.")),
		mcp.WithBoolean("confluence", mcp.Description("Post report to Confluence. Default false.")),
		mcp.WithString("confluence_parent_id", mcp.Description("Confluence parent page ID. Defaults to configured parent.")),
	)
}

func backlogHealthToolDef() mcp.Tool {
	return mcp.NewTool("jira_backlog_health",
		mcp.WithDescription("Check backlog health: find stale tickets, missing descriptions, orphaned issues (no parent epic), unassigned active issues, and missing labels. Returns categorized findings with AI-generated executive summary and recommendations."),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithNumber("stale_days", mcp.Description("Days without update before an active issue is stale. Default 14.")),
		mcp.WithBoolean("no_llm", mcp.Description("Skip LLM summary, return rule-based checks only. Default false.")),
	)
}

func summarizeCommentsToolDef() mcp.Tool {
	return mcp.NewTool("jira_summarize_comments",
		mcp.WithDescription("Summarize a JIRA issue's comment thread using AI. Returns key decisions, action items, open questions, and an overall summary."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func findSimilarToolDef() mcp.Tool {
	return mcp.NewTool("jira_find_similar",
		mcp.WithDescription("Find duplicate or related JIRA issues using AI similarity analysis. Provide an issue key or freeform text to compare against open project issues."),
		mcp.WithString("issue_key", mcp.Description("JIRA issue key to find similar issues for (e.g. PROJ-123).")),
		mcp.WithString("text", mcp.Description("Freeform text to find similar issues for (alternative to issue_key).")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
		mcp.WithNumber("threshold", mcp.Description("Minimum confidence threshold (0.0-1.0). Default 0.5.")),
		mcp.WithNumber("max_candidates", mcp.Description("Maximum candidate issues to compare. Default 200.")),
	)
}

func confluenceAnalyticsToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_analytics",
		mcp.WithDescription("Get Confluence page view analytics (total views and unique viewers). Provide a page ID to check a single page, or set include_children to also check all child pages."),
		mcp.WithString("page_id", mcp.Required(), mcp.Description("Confluence page ID to analyze.")),
		mcp.WithBoolean("include_children", mcp.Description("Also fetch analytics for child pages. Default true.")),
	)
}

func confluenceUpdateToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_update",
		mcp.WithDescription("Update an existing Confluence page or blog post. Finds content by ID or title search, then replaces its body. Use body_file for large content to avoid context limits."),
		mcp.WithString("page_id", mcp.Description("Confluence content ID. If omitted, searches by title.")),
		mcp.WithString("title", mcp.Description("Content title to search for. Used when page_id is not provided.")),
		mcp.WithString("body", mcp.Description("New body in Confluence storage format (XHTML). Either body or body_file is required.")),
		mcp.WithString("body_file", mcp.Description("Path to a local file containing the body. Use this instead of body for large content.")),
		mcp.WithString("space_key", mcp.Description("Space key to narrow title search (e.g. 'ENG'). Optional.")),
		mcp.WithString("content_type", mcp.Description("Content type: 'page' (default) or 'blog'.")),
	)
}

func confluenceGetPageToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_get_page",
		mcp.WithDescription("Read a Confluence page or blog post by ID or title search. Returns the body in storage format (XHTML), title, version, and ID. Use output_file to save large content to a file instead of returning it in context."),
		mcp.WithString("page_id", mcp.Description("Confluence content ID.")),
		mcp.WithString("title", mcp.Description("Content title to search for. Used when page_id is not provided.")),
		mcp.WithString("space_key", mcp.Description("Space key to narrow title search.")),
		mcp.WithString("content_type", mcp.Description("Content type: 'page' (default) or 'blog'.")),
		mcp.WithString("output_file", mcp.Description("Write the body to this file path instead of returning it inline. Useful for large pages.")),
	)
}

func confluenceSearchToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_search",
		mcp.WithDescription("Search Confluence content using CQL (Confluence Query Language). Examples: 'title = \"My Page\"', 'space = ENG AND label = api', 'text ~ \"error handling\" AND lastModified > now(\"-7d\")'."),
		mcp.WithString("cql", mcp.Required(), mcp.Description("CQL query string.")),
		mcp.WithNumber("limit", mcp.Description("Maximum results to return. Default 25.")),
	)
}

func confluenceListPagesToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_list_pages",
		mcp.WithDescription("List pages in a Confluence space, ordered by last modified. Useful for discovering what pages exist."),
		mcp.WithString("space_key", mcp.Required(), mcp.Description("Space key (e.g. 'ENG', 'FIELDCTO').")),
		mcp.WithNumber("limit", mcp.Description("Maximum results to return. Default 25.")),
	)
}

func confluenceGetCommentsToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_get_comments",
		mcp.WithDescription("Read footer comments on a Confluence page."),
		mcp.WithString("page_id", mcp.Required(), mcp.Description("Confluence page ID.")),
	)
}

func confluenceAddLabelToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_add_label",
		mcp.WithDescription("Add a label to a Confluence page."),
		mcp.WithString("page_id", mcp.Required(), mcp.Description("Confluence page ID.")),
		mcp.WithString("label", mcp.Required(), mcp.Description("Label to add.")),
	)
}

func confluenceCreatePageToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_create_page",
		mcp.WithDescription("Create a new Confluence page under a parent page. Use body_file for large content to avoid context limits."),
		mcp.WithString("space_id", mcp.Required(), mcp.Description("Confluence space ID.")),
		mcp.WithString("parent_id", mcp.Required(), mcp.Description("Parent page ID.")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Page title.")),
		mcp.WithString("body", mcp.Description("Page body in Confluence storage format (XHTML). Either body or body_file is required.")),
		mcp.WithString("body_file", mcp.Description("Path to a local file containing the page body. Use this instead of body for large content.")),
	)
}

func confluenceCreateBlogToolDef() mcp.Tool {
	return mcp.NewTool("jira_confluence_create_blog",
		mcp.WithDescription("Create a new Confluence blog post. Use body_file for large content to avoid context limits."),
		mcp.WithString("space_id", mcp.Required(), mcp.Description("Confluence space ID.")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Blog post title.")),
		mcp.WithString("body", mcp.Description("Blog post body in Confluence storage format (XHTML). Either body or body_file is required.")),
		mcp.WithString("body_file", mcp.Description("Path to a local file containing the blog post body. Use this instead of body for large content.")),
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

// getBody resolves body content from either the "body" string parameter
// or the "body_file" file path parameter. Returns the content or an error message.
func getBody(req mcp.CallToolRequest) (string, string) {
	body := getString(req, "body")
	bodyFile := getString(req, "body_file")

	if body == "" && bodyFile == "" {
		return "", "either body or body_file is required"
	}
	if bodyFile != "" {
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", fmt.Sprintf("read body_file %q: %v", bodyFile, err)
		}
		return string(data), ""
	}
	return body, ""
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

	db := h.cache

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
		h.cache.UpsertIssues(cfg.JiraProject, issues)
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

	db := h.cache

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

	// Parse date range — default to 7 days ago through today
	startStr := getString(req, "start_date")
	endStr := getString(req, "end_date")

	now := time.Now()
	if endStr == "" {
		endStr = now.Format("2006-01-02")
	}
	if startStr == "" {
		startStr = now.AddDate(0, 0, -7).Format("2006-01-02")
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

	db := h.cache

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

		// Check which issues have fresh cached details
		updatedByKey := make(map[string]time.Time, len(issues))
		for _, issue := range issues {
			updatedByKey[issue.Key] = issue.Updated
		}
		freshKeys := db.GetFreshDetailKeys(updatedByKey)

		var items []llm.IssueWithComments
		var fetchedDetails []*jira.IssueDetail
		for _, issue := range issues {
			var detail *jira.IssueDetail

			if freshKeys[issue.Key] {
				if cached, ok := db.GetIssueDetail(issue.Key, issue.Updated); ok {
					detail = cached
					slog.Info("cache hit for issue detail", "key", issue.Key)
				}
			}

			if detail == nil {
				var err error
				detail, err = client.GetIssue(issue.Key)
				if err != nil {
					slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
					continue
				}
				fetchedDetails = append(fetchedDetails, detail)
			}

			// Comments: use cached if detail was cached, otherwise fetch
			var comments []jira.Comment
			if freshKeys[issue.Key] {
				comments, _ = db.GetCommentsByKeys([]string{issue.Key}, startTime)
			}
			if len(comments) == 0 {
				var err error
				comments, err = client.GetComments(issue.Key)
				if err != nil {
					slog.Warn("failed to get comments", "key", issue.Key, "error", err)
				}
				if len(comments) > 0 {
					db.UpsertComments(comments)
				}
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

		// Cache newly fetched details
		if len(fetchedDetails) > 0 {
			db.UpsertIssueDetails(fetchedDetails)
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

func (h *Handlers) HandleBacklogHealth(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		// Fall back to JIRA-only config if no LLM configured
		cfg, err = loadJIRAConfig(req)
		if err != nil {
			return errorResult(err.Error()), nil
		}
	}

	staleDays := int(getFloat(req, "stale_days"))
	if staleDays <= 0 {
		staleDays = 14
	}
	noLLM := getBool(req, "no_llm")

	slog.Info("backlog-health", "project", cfg.JiraProject, "stale_days", staleDays)

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	issues, err := client.GetOpenIssues()
	if err != nil {
		return errorResult(fmt.Sprintf("JIRA search failed: %v", err)), nil
	}

	if len(issues) == 0 {
		return textResult("No open issues found."), nil
	}

	// Fetch details for each issue (cache-aware)
	updatedByKey := make(map[string]time.Time, len(issues))
	for _, issue := range issues {
		updatedByKey[issue.Key] = issue.Updated
	}
	freshKeys := h.cache.GetFreshDetailKeys(updatedByKey)

	var details []*jira.IssueDetail
	var fetchedDetails []*jira.IssueDetail
	for _, issue := range issues {
		var detail *jira.IssueDetail
		if freshKeys[issue.Key] {
			if cached, ok := h.cache.GetIssueDetail(issue.Key, issue.Updated); ok {
				detail = cached
			}
		}
		if detail == nil {
			detail, err = client.GetIssue(issue.Key)
			if err != nil {
				slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
				continue
			}
			fetchedDetails = append(fetchedDetails, detail)
		}
		details = append(details, detail)
	}
	if len(fetchedDetails) > 0 {
		h.cache.UpsertIssueDetails(fetchedDetails)
	}

	// Run checks
	findings := llm.CheckBacklogHealth(details, staleDays)

	// Count unique problem issues
	seen := make(map[string]bool)
	for _, f := range findings {
		seen[f.Key] = true
	}
	healthyCount := len(details) - len(seen)
	healthPct := 0
	if len(details) > 0 {
		healthPct = healthyCount * 100 / len(details)
	}

	// Build text output
	var text strings.Builder
	fmt.Fprintf(&text, "## Backlog Health Check\n\n")
	fmt.Fprintf(&text, "**%d open issues** — %d healthy, %d with problems (%d%% healthy)\n\n",
		len(details), healthyCount, len(seen), healthPct)

	// Group findings
	order := []string{"stale", "unassigned_active", "missing_description", "orphaned", "missing_labels"}
	labels := map[string]string{
		"stale":               "Stale Tickets",
		"unassigned_active":   "Unassigned Active",
		"missing_description": "Missing Description",
		"orphaned":            "Orphaned (No Parent)",
		"missing_labels":      "Missing Labels",
	}
	grouped := make(map[string][]llm.HealthFinding)
	for _, f := range findings {
		grouped[f.Category] = append(grouped[f.Category], f)
	}
	for _, cat := range order {
		items := grouped[cat]
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&text, "### %s (%d)\n", labels[cat], len(items))
		for _, f := range items {
			fmt.Fprintf(&text, "- **%s**: %s — %s\n", f.Key, f.Summary, f.Detail)
		}
		text.WriteString("\n")
	}

	// LLM summary
	if !noLLM && len(findings) > 0 {
		summary, recs, err := llm.GenerateHealthSummary(cfg, len(details), findings)
		if err != nil {
			slog.Warn("LLM health summary failed", "error", err)
		} else {
			fmt.Fprintf(&text, "### Executive Summary\n%s\n\n", summary)
			if len(recs) > 0 {
				text.WriteString("### Recommendations\n")
				for _, r := range recs {
					fmt.Fprintf(&text, "- %s\n", r)
				}
				text.WriteString("\n")
			}
		}
	}

	if len(findings) == 0 {
		text.WriteString("No problems found — backlog is healthy!\n")
	}

	return textResult(text.String()), nil
}

func (h *Handlers) HandleSummarizeComments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	issueKey := getString(req, "issue_key")
	if issueKey == "" {
		return errorResult("issue_key is required"), nil
	}

	slog.Info("summarize-comments", "key", issueKey)

	// Fetch issue details (cache-aware)
	detail, ok := h.cache.GetIssueDetail(issueKey, time.Time{})
	if !ok {
		client, err := jira.NewClient(cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		detail, err = client.GetIssue(issueKey)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		h.cache.UpsertIssueDetail(detail)
	}

	// Fetch comments (cache-aware)
	comments, _ := h.cache.GetCommentsByKeys([]string{issueKey}, time.Time{})
	if len(comments) == 0 {
		client, err := jira.NewClient(cfg)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		comments, err = client.GetComments(issueKey)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		if len(comments) > 0 {
			h.cache.UpsertComments(comments)
		}
	}

	if len(comments) == 0 {
		return textResult(fmt.Sprintf("No comments found on %s.", issueKey)), nil
	}

	summary, err := llm.GenerateCommentSummary(cfg, detail, comments)
	if err != nil {
		return errorResult(fmt.Sprintf("LLM generation failed: %v", err)), nil
	}

	var text strings.Builder
	fmt.Fprintf(&text, "## Comment Summary: %s — %s\n\n", detail.Key, detail.Summary)
	fmt.Fprintf(&text, "**%d comments analyzed**\n\n", len(comments))
	fmt.Fprintf(&text, "### Summary\n%s\n\n", summary.Summary)

	if len(summary.KeyDecisions) > 0 {
		text.WriteString("### Key Decisions\n")
		for _, d := range summary.KeyDecisions {
			fmt.Fprintf(&text, "- %s\n", d)
		}
		text.WriteString("\n")
	}
	if len(summary.ActionItems) > 0 {
		text.WriteString("### Action Items\n")
		for _, a := range summary.ActionItems {
			fmt.Fprintf(&text, "- %s\n", a)
		}
		text.WriteString("\n")
	}
	if len(summary.OpenQuestions) > 0 {
		text.WriteString("### Open Questions\n")
		for _, q := range summary.OpenQuestions {
			fmt.Fprintf(&text, "- %s\n", q)
		}
		text.WriteString("\n")
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

func (h *Handlers) HandleFindSimilar(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	issueKey := getString(req, "issue_key")
	text := getString(req, "text")
	threshold := getFloat(req, "threshold")
	maxCandidates := int(getFloat(req, "max_candidates"))

	if issueKey == "" && text == "" {
		return errorResult("either issue_key or text is required"), nil
	}
	if threshold <= 0 {
		threshold = 0.5
	}
	if maxCandidates <= 0 {
		maxCandidates = 200
	}

	slog.Info("find-similar", "issue_key", issueKey, "text_len", len(text), "threshold", threshold)

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	provider, err := llm.NewProvider(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Fetch candidate issues
	jql := fmt.Sprintf(
		"project = %s AND status NOT IN (Done, Closed, Resolved) ORDER BY updated DESC",
		cfg.JiraProject,
	)
	issues, err := client.SearchJQL(jql, maxCandidates)
	if err != nil {
		return errorResult(fmt.Sprintf("JIRA search failed: %v", err)), nil
	}

	// Fetch details for candidates (cache-aware)
	updatedByKey := make(map[string]time.Time, len(issues))
	for _, issue := range issues {
		updatedByKey[issue.Key] = issue.Updated
	}
	freshKeys := h.cache.GetFreshDetailKeys(updatedByKey)

	var candidates []*jira.IssueDetail
	var fetchedDetails []*jira.IssueDetail
	for _, issue := range issues {
		var detail *jira.IssueDetail
		if freshKeys[issue.Key] {
			if cached, ok := h.cache.GetIssueDetail(issue.Key, issue.Updated); ok {
				detail = cached
			}
		}
		if detail == nil {
			detail, err = client.GetIssue(issue.Key)
			if err != nil {
				slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
				continue
			}
			fetchedDetails = append(fetchedDetails, detail)
		}
		candidates = append(candidates, detail)
	}
	if len(fetchedDetails) > 0 {
		h.cache.UpsertIssueDetails(fetchedDetails)
	}

	var result *llm.SimilarityResult

	if issueKey != "" {
		// Find target in candidates or fetch separately
		var target *jira.IssueDetail
		for _, c := range candidates {
			if c.Key == issueKey {
				target = c
				break
			}
		}
		if target == nil {
			target, err = client.GetIssue(issueKey)
			if err != nil {
				return errorResult(fmt.Sprintf("get issue %s: %v", issueKey, err)), nil
			}
		}

		filtered := llm.PrepareCandidates(issueKey, candidates)
		result, err = llm.FindSimilar(provider, target, filtered, threshold)
		if err != nil {
			return errorResult(fmt.Sprintf("similarity analysis: %v", err)), nil
		}
	} else {
		result, err = llm.FindSimilarByText(provider, text, candidates, threshold)
		if err != nil {
			return errorResult(fmt.Sprintf("similarity analysis: %v", err)), nil
		}
	}

	// Store for web dashboard
	resultID := h.store.Save(ResultFindSimilar, fmt.Sprintf("Similar: %s", issueKey+text), &FindSimilarResultData{
		TargetKey:  result.TargetKey,
		TargetText: result.TargetText,
		Matches:    result.Matches,
		JiraServer: cfg.JiraServer,
		FoundAt:    time.Now(),
	})

	// Build text response
	var out strings.Builder
	targetLabel := result.TargetKey
	if targetLabel == "" {
		targetLabel = fmt.Sprintf("%q", result.TargetText)
	}
	fmt.Fprintf(&out, "## Similar Issues: %s\n\n", targetLabel)

	if len(result.Matches) == 0 {
		out.WriteString("No similar issues found.\n")
	} else {
		fmt.Fprintf(&out, "**%d matches found:**\n\n", len(result.Matches))
		for _, m := range result.Matches {
			fmt.Fprintf(&out, "- [%s](%s/browse/%s) **%.0f%%** (%s) %s — %s\n",
				m.Key, cfg.JiraServer, m.Key,
				m.Confidence*100, m.Relation, m.Summary, m.Reason)
		}
	}
	fmt.Fprintf(&out, "\n---\nRich dashboard: %s\n", h.viewURL(resultID))

	return textResult(out.String()), nil
}

// HandleConfluenceAnalytics returns page view stats for a Confluence page and optionally its children.
func (h *Handlers) HandleConfluenceAnalytics(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	pageID := getString(req, "page_id")
	if pageID == "" {
		return errorResult("page_id is required"), nil
	}

	includeChildren := true
	if args := req.GetArguments(); args != nil {
		if v, ok := args["include_children"].(bool); ok {
			includeChildren = v
		}
	}

	slog.Info("confluence-analytics", "page_id", pageID, "include_children", includeChildren)

	confClient := confluence.NewClient(cfg)

	parent, err := confClient.GetPageAnalytics(pageID)
	if err != nil {
		return errorResult(fmt.Sprintf("get page analytics: %v", err)), nil
	}

	pages := []ConfluencePageStats{{
		PageID:        parent.PageID,
		Title:         parent.Title,
		TotalViews:    parent.TotalViews,
		UniqueViewers: parent.UniqueViewers,
	}}

	if includeChildren {
		children, err := confClient.GetChildPages(pageID)
		if err != nil {
			slog.Warn("failed to fetch child pages", "page_id", pageID, "error", err)
		} else {
			for _, child := range children {
				analytics, err := confClient.GetPageAnalytics(child.ID)
				if err != nil {
					slog.Warn("failed to get child analytics", "page_id", child.ID, "error", err)
					continue
				}
				pages = append(pages, ConfluencePageStats{
					PageID:        analytics.PageID,
					Title:         analytics.Title,
					TotalViews:    analytics.TotalViews,
					UniqueViewers: analytics.UniqueViewers,
				})
			}
		}
	}

	// Store for web dashboard
	resultID := h.store.Save(ResultConfluenceAnalytics, fmt.Sprintf("Analytics: %s", parent.Title), &ConfluenceAnalyticsResultData{
		ParentTitle: parent.Title,
		Pages:       pages,
		JiraServer:  cfg.JiraServer,
		FetchedAt:   time.Now(),
	})

	// Build markdown response
	var out strings.Builder
	fmt.Fprintf(&out, "## Confluence Analytics: %s\n\n", parent.Title)
	fmt.Fprintf(&out, "| Page | Total Views | Unique Viewers |\n")
	fmt.Fprintf(&out, "|------|------------|----------------|\n")
	for _, p := range pages {
		fmt.Fprintf(&out, "| %s | %d | %d |\n", p.Title, p.TotalViews, p.UniqueViewers)
	}
	fmt.Fprintf(&out, "\n---\nRich dashboard: %s\n", h.viewURL(resultID))

	return textResult(out.String()), nil
}

// HandleConfluenceUpdate finds a Confluence page or blog post by ID or title and updates its body.
func (h *Handlers) HandleConfluenceUpdate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	pageID := getString(req, "page_id")
	title := getString(req, "title")
	spaceKey := getString(req, "space_key")
	contentType := getString(req, "content_type")
	if contentType == "" {
		contentType = "page"
	}

	body, errMsg := getBody(req)
	if errMsg != "" {
		return errorResult(errMsg), nil
	}

	if pageID == "" && title == "" {
		return errorResult("either page_id or title is required"), nil
	}

	confClient := confluence.NewClient(cfg)

	// Common struct to hold ID, title, version for both pages and blog posts
	type contentInfo struct {
		ID      string
		Title   string
		Version int
	}
	var content contentInfo

	if contentType == "blog" {
		if pageID != "" {
			post, _, err := confClient.GetBlogPostBody(pageID)
			if err != nil {
				return errorResult(fmt.Sprintf("get blog post %s: %v", pageID, err)), nil
			}
			content = contentInfo{ID: post.ID, Title: post.Title, Version: post.Version.Number}
		} else {
			posts, err := confClient.SearchBlogPostsByTitle(title)
			if err != nil {
				return errorResult(fmt.Sprintf("search blog posts: %v", err)), nil
			}
			if len(posts) == 0 {
				return errorResult(fmt.Sprintf("no blog post found with title %q", title)), nil
			}
			if len(posts) > 1 {
				var titles []string
				for _, p := range posts {
					titles = append(titles, fmt.Sprintf("%s (id=%s)", p.Title, p.ID))
				}
				return errorResult(fmt.Sprintf("multiple blog posts match title %q — provide page_id: %s", title, strings.Join(titles, ", "))), nil
			}
			content = contentInfo{ID: posts[0].ID, Title: posts[0].Title, Version: posts[0].Version.Number}
		}

		slog.Info("updating confluence blog post", "id", content.ID, "title", content.Title, "version", content.Version)
		if err := confClient.UpdateBlogPost(content.ID, content.Title, body, content.Version); err != nil {
			return errorResult(fmt.Sprintf("update blog post: %v", err)), nil
		}
	} else {
		var page *confluence.Page
		if pageID != "" {
			page, _, err = confClient.GetPageBody(pageID)
			if err != nil {
				return errorResult(fmt.Sprintf("get page %s: %v", pageID, err)), nil
			}
		} else {
			if spaceKey != "" {
				results, err := confClient.SearchPagesByTitle(title)
				if err != nil {
					return errorResult(fmt.Sprintf("search pages: %v", err)), nil
				}
				var matched []confluence.Page
				for _, r := range results {
					p, pErr := confClient.GetPage(r.ID)
					if pErr != nil {
						continue
					}
					if p.SpaceKey == spaceKey {
						matched = append(matched, *p)
					}
				}
				if len(matched) == 0 {
					return errorResult(fmt.Sprintf("no page found with title %q in space %s", title, spaceKey)), nil
				}
				if len(matched) > 1 {
					var titles []string
					for _, m := range matched {
						titles = append(titles, fmt.Sprintf("%s (id=%s)", m.Title, m.ID))
					}
					return errorResult(fmt.Sprintf("multiple pages match title %q: %s", title, strings.Join(titles, ", "))), nil
				}
				page = &matched[0]
			} else {
				results, err := confClient.SearchPagesByTitle(title)
				if err != nil {
					return errorResult(fmt.Sprintf("search pages: %v", err)), nil
				}
				if len(results) == 0 {
					return errorResult(fmt.Sprintf("no page found with title %q", title)), nil
				}
				if len(results) > 1 {
					var titles []string
					for _, r := range results {
						titles = append(titles, fmt.Sprintf("%s (id=%s)", r.Title, r.ID))
					}
					return errorResult(fmt.Sprintf("multiple pages match title %q — provide page_id or space_key: %s", title, strings.Join(titles, ", "))), nil
				}
				page = &results[0]
			}
		}
		content = contentInfo{ID: page.ID, Title: page.Title, Version: page.Version.Number}

		slog.Info("updating confluence page", "id", content.ID, "title", content.Title, "version", content.Version)
		if err := confClient.UpdatePage(content.ID, content.Title, body, content.Version); err != nil {
			return errorResult(fmt.Sprintf("update page: %v", err)), nil
		}
	}

	pageURL := fmt.Sprintf("%s/wiki/pages/viewpage.action?pageId=%s", cfg.JiraServer, content.ID)
	return textResult(fmt.Sprintf("Updated %s [%s](%s) (version %d → %d)",
		contentType, content.Title, pageURL, content.Version, content.Version+1)), nil
}

// HandleConfluenceGetPage reads a Confluence page or blog post by ID or title.
func (h *Handlers) HandleConfluenceGetPage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	pageID := getString(req, "page_id")
	title := getString(req, "title")
	spaceKey := getString(req, "space_key")
	contentType := getString(req, "content_type")
	outputFile := getString(req, "output_file")
	if contentType == "" {
		contentType = "page"
	}

	if pageID == "" && title == "" {
		return errorResult("either page_id or title is required"), nil
	}

	confClient := confluence.NewClient(cfg)

	var contentID, contentTitle, contentSpaceID, body string
	var contentVersion int

	if contentType == "blog" {
		if pageID == "" {
			posts, searchErr := confClient.SearchBlogPostsByTitle(title)
			if searchErr != nil {
				return errorResult(fmt.Sprintf("search: %v", searchErr)), nil
			}
			if len(posts) == 0 {
				return errorResult(fmt.Sprintf("no blog post found with title %q", title)), nil
			}
			if len(posts) > 1 {
				var names []string
				for _, p := range posts {
					names = append(names, fmt.Sprintf("%s (id=%s)", p.Title, p.ID))
				}
				return errorResult(fmt.Sprintf("multiple blog posts match — provide page_id: %s",
					strings.Join(names, ", "))), nil
			}
			pageID = posts[0].ID
		}

		post, postBody, err := confClient.GetBlogPostBody(pageID)
		if err != nil {
			return errorResult(fmt.Sprintf("get blog post: %v", err)), nil
		}
		contentID = post.ID
		contentTitle = post.Title
		contentSpaceID = post.SpaceID
		contentVersion = post.Version.Number
		body = postBody
	} else {
		if pageID == "" {
			if spaceKey != "" {
				results, searchErr := confClient.SearchCQL(
					fmt.Sprintf("space = %q AND title = %q AND type = page", spaceKey, title), 5)
				if searchErr != nil {
					return errorResult(fmt.Sprintf("search: %v", searchErr)), nil
				}
				if len(results) == 0 {
					return errorResult(fmt.Sprintf("no page found with title %q in space %s", title, spaceKey)), nil
				}
				pageID = results[0].ID
			} else {
				results, searchErr := confClient.SearchPagesByTitle(title)
				if searchErr != nil {
					return errorResult(fmt.Sprintf("search: %v", searchErr)), nil
				}
				if len(results) == 0 {
					return errorResult(fmt.Sprintf("no page found with title %q", title)), nil
				}
				if len(results) > 1 {
					var names []string
					for _, r := range results {
						names = append(names, fmt.Sprintf("%s (id=%s)", r.Title, r.ID))
					}
					return errorResult(fmt.Sprintf("multiple pages match — provide page_id or space_key: %s",
						strings.Join(names, ", "))), nil
				}
				pageID = results[0].ID
			}
		}

		page, pageBody, err := confClient.GetPageBody(pageID)
		if err != nil {
			return errorResult(fmt.Sprintf("get page: %v", err)), nil
		}
		contentID = page.ID
		contentTitle = page.Title
		contentSpaceID = page.SpaceID
		contentVersion = page.Version.Number
		body = pageBody
	}

	// If output_file specified, write body to file instead of returning inline
	if outputFile != "" {
		if err := os.WriteFile(outputFile, []byte(body), 0o644); err != nil {
			return errorResult(fmt.Sprintf("write output_file %q: %v", outputFile, err)), nil
		}
		var out strings.Builder
		fmt.Fprintf(&out, "## %s\n\n", contentTitle)
		fmt.Fprintf(&out, "- **Type:** %s\n", contentType)
		fmt.Fprintf(&out, "- **ID:** %s\n", contentID)
		fmt.Fprintf(&out, "- **Space ID:** %s\n", contentSpaceID)
		fmt.Fprintf(&out, "- **Version:** %d\n", contentVersion)
		fmt.Fprintf(&out, "- **Body written to:** %s (%d bytes)\n", outputFile, len(body))
		return textResult(out.String()), nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "## %s\n\n", contentTitle)
	fmt.Fprintf(&out, "- **Type:** %s\n", contentType)
	fmt.Fprintf(&out, "- **ID:** %s\n", contentID)
	fmt.Fprintf(&out, "- **Space ID:** %s\n", contentSpaceID)
	fmt.Fprintf(&out, "- **Version:** %d\n\n", contentVersion)
	fmt.Fprintf(&out, "### Body (storage format)\n\n```html\n%s\n```\n", body)

	return textResult(out.String()), nil
}

// HandleConfluenceSearch searches Confluence using CQL.
func (h *Handlers) HandleConfluenceSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	cql := getString(req, "cql")
	if cql == "" {
		return errorResult("cql is required"), nil
	}

	limit := int(getFloat(req, "limit"))
	if limit <= 0 {
		limit = 25
	}

	confClient := confluence.NewClient(cfg)
	results, err := confClient.SearchCQL(cql, limit)
	if err != nil {
		return errorResult(fmt.Sprintf("search: %v", err)), nil
	}

	if len(results) == 0 {
		return textResult("No results found."), nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "## Confluence Search: %d results\n\n", len(results))
	fmt.Fprintf(&out, "| ID | Title | Space | Version |\n")
	fmt.Fprintf(&out, "|----|-------|-------|---------|\n")
	for _, r := range results {
		link := fmt.Sprintf("[%s](%s/wiki/pages/viewpage.action?pageId=%s)", r.Title, cfg.JiraServer, r.ID)
		fmt.Fprintf(&out, "| %s | %s | %s | %d |\n", r.ID, link, r.SpaceKey, r.Version.Number)
	}

	return textResult(out.String()), nil
}

// HandleConfluenceListPages lists pages in a Confluence space.
func (h *Handlers) HandleConfluenceListPages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	spaceKey := getString(req, "space_key")
	if spaceKey == "" {
		return errorResult("space_key is required"), nil
	}

	limit := int(getFloat(req, "limit"))
	if limit <= 0 {
		limit = 25
	}

	confClient := confluence.NewClient(cfg)
	results, err := confClient.GetPagesInSpace(spaceKey, limit)
	if err != nil {
		return errorResult(fmt.Sprintf("list pages: %v", err)), nil
	}

	if len(results) == 0 {
		return textResult(fmt.Sprintf("No pages found in space %s.", spaceKey)), nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "## Pages in %s (%d results)\n\n", spaceKey, len(results))
	fmt.Fprintf(&out, "| ID | Title | Version |\n")
	fmt.Fprintf(&out, "|----|-------|---------|\n")
	for _, r := range results {
		link := fmt.Sprintf("[%s](%s/wiki/pages/viewpage.action?pageId=%s)", r.Title, cfg.JiraServer, r.ID)
		fmt.Fprintf(&out, "| %s | %s | %d |\n", r.ID, link, r.Version.Number)
	}

	return textResult(out.String()), nil
}

// HandleConfluenceGetComments reads footer comments on a Confluence page.
func (h *Handlers) HandleConfluenceGetComments(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	pageID := getString(req, "page_id")
	if pageID == "" {
		return errorResult("page_id is required"), nil
	}

	confClient := confluence.NewClient(cfg)
	comments, err := confClient.GetPageComments(pageID)
	if err != nil {
		return errorResult(fmt.Sprintf("get comments: %v", err)), nil
	}

	if len(comments) == 0 {
		return textResult("No comments on this page."), nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "## Comments (%d)\n\n", len(comments))
	for _, c := range comments {
		fmt.Fprintf(&out, "**%s:**\n%s\n\n---\n\n", c.Author, c.Body)
	}

	return textResult(out.String()), nil
}

// HandleConfluenceCreatePage creates a new Confluence page.
func (h *Handlers) HandleConfluenceCreatePage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	spaceID := getString(req, "space_id")
	parentID := getString(req, "parent_id")
	title := getString(req, "title")

	body, errMsg := getBody(req)
	if errMsg != "" {
		return errorResult(errMsg), nil
	}

	confClient := confluence.NewClient(cfg)
	page, err := confClient.CreatePage(spaceID, parentID, title, body)
	if err != nil {
		return errorResult(fmt.Sprintf("create page: %v", err)), nil
	}

	pageURL := fmt.Sprintf("%s/wiki/pages/viewpage.action?pageId=%s", cfg.JiraServer, page.ID)
	return textResult(fmt.Sprintf("Page created: [%s](%s) (ID: %s)", page.Title, pageURL, page.ID)), nil
}

// HandleConfluenceCreateBlog creates a new Confluence blog post.
func (h *Handlers) HandleConfluenceCreateBlog(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	spaceID := getString(req, "space_id")
	title := getString(req, "title")

	body, errMsg := getBody(req)
	if errMsg != "" {
		return errorResult(errMsg), nil
	}

	confClient := confluence.NewClient(cfg)
	post, err := confClient.CreateBlogPost(spaceID, title, body)
	if err != nil {
		return errorResult(fmt.Sprintf("create blog post: %v", err)), nil
	}

	pageURL := fmt.Sprintf("%s/wiki/pages/viewpage.action?pageId=%s", cfg.JiraServer, post.ID)
	return textResult(fmt.Sprintf("Blog post created: [%s](%s) (ID: %s)", post.Title, pageURL, post.ID)), nil
}

// HandleConfluenceAddLabel adds a label to a Confluence page.
func (h *Handlers) HandleConfluenceAddLabel(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	pageID := getString(req, "page_id")
	label := getString(req, "label")
	if pageID == "" {
		return errorResult("page_id is required"), nil
	}
	if label == "" {
		return errorResult("label is required"), nil
	}

	confClient := confluence.NewClient(cfg)
	if err := confClient.AddLabel(pageID, label); err != nil {
		return errorResult(fmt.Sprintf("add label: %v", err)), nil
	}

	return textResult(fmt.Sprintf("Added label %q to page %s", label, pageID)), nil
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
