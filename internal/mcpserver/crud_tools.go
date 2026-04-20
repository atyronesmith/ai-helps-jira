package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// --- Tool Definitions ---

func getIssueToolDef() mcp.Tool {
	return mcp.NewTool("jira_get_issue",
		mcp.WithDescription("Get full details of a JIRA issue including description, comments, and linked issues."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithBoolean("include_comments", mcp.Description("Include issue comments. Default true.")),
		mcp.WithBoolean("include_links", mcp.Description("Include linked issues and subtasks. Default true.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func createIssueToolDef() mcp.Tool {
	return mcp.NewTool("jira_create_issue",
		mcp.WithDescription("Create a JIRA issue of any type (Task, Bug, Story, Epic, Sub-task, etc.)."),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Issue summary/title.")),
		mcp.WithString("issue_type", mcp.Required(), mcp.Description("Issue type: Task, Bug, Story, Epic, Sub-task, etc.")),
		mcp.WithString("description", mcp.Description("Issue description in plain text.")),
		mcp.WithString("priority", mcp.Description("Priority: Highest, High, Medium, Low, Lowest.")),
		mcp.WithString("parent", mcp.Description("Parent issue key (for sub-tasks or stories under epics).")),
		mcp.WithString("assignee_account_id", mcp.Description("Assignee account ID. Use jira_lookup_user to find.")),
		mcp.WithString("project", mcp.Description("JIRA project key. Defaults to configured project.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
	)
}

func editIssueToolDef() mcp.Tool {
	return mcp.NewTool("jira_edit_issue",
		mcp.WithDescription("Update fields on an existing JIRA issue. Pass only the fields you want to change."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("summary", mcp.Description("New summary/title.")),
		mcp.WithString("description", mcp.Description("New description in plain text.")),
		mcp.WithString("priority", mcp.Description("New priority: Highest, High, Medium, Low, Lowest.")),
		mcp.WithString("assignee_account_id", mcp.Description("New assignee account ID. Use jira_lookup_user to find.")),
		mcp.WithObject("custom_fields", mcp.Description("Custom fields as key-value pairs (e.g. {\"customfield_NNNNN\": 3} for story points).")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func getTransitionsToolDef() mcp.Tool {
	return mcp.NewTool("jira_get_transitions",
		mcp.WithDescription("List available workflow transitions for an issue. Use this to find the transition ID needed by jira_transition."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func transitionToolDef() mcp.Tool {
	return mcp.NewTool("jira_transition",
		mcp.WithDescription("Transition a JIRA issue to a new workflow status. Use jira_get_transitions first to find the transition ID."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("transition_id", mcp.Required(), mcp.Description("Transition ID from jira_get_transitions.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func addCommentToolDef() mcp.Tool {
	return mcp.NewTool("jira_add_comment",
		mcp.WithDescription("Add a comment to a JIRA issue."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("body", mcp.Required(), mcp.Description("Comment text in plain text.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func lookupUserToolDef() mcp.Tool {
	return mcp.NewTool("jira_lookup_user",
		mcp.WithDescription("Search for JIRA users by name or email. Returns account IDs needed for assignee fields."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search string (name or email).")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func attachFileToolDef() mcp.Tool {
	return mcp.NewTool("jira_attach_file",
		mcp.WithDescription("Upload a file attachment to a JIRA issue. Accepts a local file path."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("file_path", mcp.Required(), mcp.Description("Absolute path to the local file to attach.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func addWorklogToolDef() mcp.Tool {
	return mcp.NewTool("jira_add_worklog",
		mcp.WithDescription("Add a worklog entry to a JIRA issue to record time spent."),
		mcp.WithString("issue_key", mcp.Required(), mcp.Description("JIRA issue key (e.g. PROJ-123).")),
		mcp.WithString("time_spent", mcp.Required(), mcp.Description("Time spent in Jira format (e.g. '2h 30m', '1d', '45m').")),
		mcp.WithString("comment", mcp.Description("Worklog comment describing what was done.")),
		mcp.WithString("started", mcp.Description("When the work started in ISO 8601 format (e.g. '2024-01-15T09:00:00.000+0000'). Defaults to now.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

func linkIssuesToolDef() mcp.Tool {
	return mcp.NewTool("jira_link_issues",
		mcp.WithDescription("Create a link between two JIRA issues."),
		mcp.WithString("inward_issue", mcp.Required(), mcp.Description("Inward issue key (e.g. PROJ-123).")),
		mcp.WithString("outward_issue", mcp.Required(), mcp.Description("Outward issue key (e.g. PROJ-456).")),
		mcp.WithString("link_type", mcp.Required(), mcp.Description("Link type name: Blocks, Cloners, Duplicate, Relates, etc.")),
		mcp.WithString("user", mcp.Description("JIRA user email.")),
		mcp.WithString("project", mcp.Description("JIRA project key.")),
	)
}

// --- Handlers ---

func (h *Handlers) HandleGetIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	if key == "" {
		return errorResult("issue_key is required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Check detail cache first
	detail, cached := h.cache.GetIssueDetail(key, time.Time{})
	var links []jira.IssueLink

	if cached {
		slog.Info("cache hit for issue detail", "key", key)
		links, _ = h.cache.GetIssueLinks(key)
	}

	if !cached {
		detail, links, err = client.GetIssueWithLinks(key)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		h.cache.UpsertIssueDetail(detail)
		if len(links) > 0 {
			h.cache.UpsertIssueLinks(links)
		}
	}

	var text strings.Builder
	fmt.Fprintf(&text, "## %s: %s\n\n", detail.Key, detail.Summary)
	fmt.Fprintf(&text, "- **Type:** %s\n", detail.IssueType)
	fmt.Fprintf(&text, "- **Status:** %s\n", detail.Status)
	fmt.Fprintf(&text, "- **Priority:** %s\n", detail.Priority)
	if detail.Assignee != "" {
		fmt.Fprintf(&text, "- **Assignee:** %s\n", detail.Assignee)
	}
	if len(detail.Labels) > 0 {
		fmt.Fprintf(&text, "- **Labels:** %s\n", strings.Join(detail.Labels, ", "))
	}

	if detail.Description != "" {
		fmt.Fprintf(&text, "\n### Description\n%s\n", detail.Description)
	}

	// Include links unless explicitly disabled
	args := req.GetArguments()
	includeLinks := true
	if v, ok := args["include_links"].(bool); ok {
		includeLinks = v
	}
	if includeLinks && len(links) > 0 {
		fmt.Fprintf(&text, "\n### Linked Issues (%d)\n", len(links))
		for _, l := range links {
			fmt.Fprintf(&text, "- [%s] %s → %s (%s) %s\n", l.LinkType, l.Direction, l.TargetKey, l.TargetStatus, l.TargetSummary)
		}
	}

	// Include comments unless explicitly disabled
	includeComments := true
	if v, ok := args["include_comments"].(bool); ok {
		includeComments = v
	}
	if includeComments {
		comments, _ := h.cache.GetCommentsByKeys([]string{key}, time.Time{})
		if len(comments) == 0 {
			comments, err = client.GetComments(key)
			if err != nil {
				slog.Warn("failed to fetch comments", "key", key, "error", err)
			} else if len(comments) > 0 {
				h.cache.UpsertComments(comments)
			}
		}
		if len(comments) > 0 {
			fmt.Fprintf(&text, "\n### Comments (%d)\n", len(comments))
			for _, c := range comments {
				fmt.Fprintf(&text, "\n**%s** (%s):\n%s\n", c.AuthorName, c.Created.Format("2006-01-02 15:04"), c.Body)
			}
		}
	}

	return textResult(text.String()), nil
}

func (h *Handlers) HandleCreateIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	summary := getString(req, "summary")
	issueType := getString(req, "issue_type")
	if summary == "" || issueType == "" {
		return errorResult("summary and issue_type are required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	params := jira.CreateIssueParams{
		ProjectKey:  getString(req, "project"),
		IssueType:   issueType,
		Summary:     summary,
		Description: getString(req, "description"),
		Priority:    getString(req, "priority"),
		Labels:      getStringSlice(req, "labels"),
		Parent:      getString(req, "parent"),
		AssigneeID:  getString(req, "assignee_account_id"),
	}

	issue, err := client.CreateIssue(params)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Issue created: %s - %s\nView: %s/browse/%s", issue.Key, issue.Summary, cfg.JiraServer, issue.Key)), nil
}

func (h *Handlers) HandleEditIssue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	if key == "" {
		return errorResult("issue_key is required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Build fields map from provided arguments
	fields := make(map[string]any)
	var changed []string

	if v := getString(req, "summary"); v != "" {
		fields["summary"] = v
		changed = append(changed, "summary")
	}
	if v := getString(req, "description"); v != "" {
		fields["description"] = v
		changed = append(changed, "description")
	}
	if v := getString(req, "priority"); v != "" {
		fields["priority"] = v
		changed = append(changed, "priority")
	}
	if v := getStringSlice(req, "labels"); len(v) > 0 {
		fields["labels"] = v
		changed = append(changed, "labels")
	}
	if v := getString(req, "assignee_account_id"); v != "" {
		fields["assignee"] = v
		changed = append(changed, "assignee")
	}
	if args := req.GetArguments(); args != nil {
		if cf, ok := args["custom_fields"].(map[string]any); ok {
			for k, v := range cf {
				fields[k] = v
				changed = append(changed, k)
			}
		}
	}

	if len(fields) == 0 {
		return errorResult("no fields provided to update"), nil
	}

	if err := client.EditIssue(key, fields); err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Issue %s updated. Fields changed: %s", key, strings.Join(changed, ", "))), nil
}

func (h *Handlers) HandleGetTransitions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	if key == "" {
		return errorResult("issue_key is required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	transitions, err := client.GetTransitions(key)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	var text strings.Builder
	fmt.Fprintf(&text, "## Available Transitions for %s\n\n", key)
	for _, t := range transitions {
		fmt.Fprintf(&text, "- **ID: %s** — %s (→ %s)\n", t.ID, t.Name, t.To)
	}
	if len(transitions) == 0 {
		text.WriteString("No transitions available.\n")
	}

	return textResult(text.String()), nil
}

func (h *Handlers) HandleTransition(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	transitionID := getString(req, "transition_id")
	if key == "" || transitionID == "" {
		return errorResult("issue_key and transition_id are required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if err := client.TransitionIssue(key, transitionID); err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Issue %s transitioned successfully.", key)), nil
}

func (h *Handlers) HandleAddComment(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	body := getString(req, "body")
	if key == "" || body == "" {
		return errorResult("issue_key and body are required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	comment, err := client.AddComment(key, body)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Comment added to %s (ID: %s).", key, comment.ID)), nil
}

func (h *Handlers) HandleLookupUser(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := getString(req, "query")
	if query == "" {
		return errorResult("query is required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	users, err := client.SearchUsers(query)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	var text strings.Builder
	fmt.Fprintf(&text, "## Users matching %q\n\n", query)
	for _, u := range users {
		fmt.Fprintf(&text, "- **%s** — Account ID: `%s`", u.DisplayName, u.AccountID)
		if u.EmailAddress != "" {
			fmt.Fprintf(&text, " (%s)", u.EmailAddress)
		}
		text.WriteString("\n")
	}
	if len(users) == 0 {
		text.WriteString("No users found.\n")
	}

	return textResult(text.String()), nil
}

func (h *Handlers) HandleAddWorklog(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	timeSpent := getString(req, "time_spent")
	if key == "" || timeSpent == "" {
		return errorResult("issue_key and time_spent are required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	wl, err := client.AddWorklog(key, timeSpent, getString(req, "comment"), getString(req, "started"))
	if err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Worklog added to %s: %s (ID: %s, started: %s).",
		key, wl.TimeSpent, wl.ID, wl.Started.Format("2006-01-02 15:04"))), nil
}

func (h *Handlers) HandleLinkIssues(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	inward := getString(req, "inward_issue")
	outward := getString(req, "outward_issue")
	linkType := getString(req, "link_type")
	if inward == "" || outward == "" || linkType == "" {
		return errorResult("inward_issue, outward_issue, and link_type are required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if err := client.LinkIssues(inward, outward, linkType); err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Linked %s → %s (type: %s).", inward, outward, linkType)), nil
}

func (h *Handlers) HandleAttachFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := getString(req, "issue_key")
	filePath := getString(req, "file_path")
	if key == "" || filePath == "" {
		return errorResult("issue_key and file_path are required"), nil
	}

	cfg, err := loadJIRAConfig(req)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	filename, err := client.AttachFile(key, filePath)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("File %q attached to %s.", filename, key)), nil
}
