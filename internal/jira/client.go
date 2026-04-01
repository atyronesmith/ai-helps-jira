package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

type Client struct {
	http     *http.Client
	base     string // e.g. https://yourcompany.atlassian.net
	email    string
	token    string
	project  string
	assignee string
}

func NewClient(cfg *config.Config) (*Client, error) {
	slog.Info("connecting to JIRA", "server", cfg.JiraServer)
	return &Client{
		http:     &http.Client{Timeout: 30 * time.Second},
		base:     cfg.JiraServer,
		email:    cfg.JiraEmail,
		token:    cfg.JiraAPIToken,
		project:  cfg.JiraProject,
		assignee: cfg.Assignee(),
	}, nil
}

// doRequest executes an authenticated request and decodes the JSON response.
func (c *Client) doRequest(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	slog.Debug("http request", "method", method, "path", path)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		slog.Error("JIRA API error", "status", resp.StatusCode, "path", path,
			"body", truncate(string(respBody), 200))
		return fmt.Errorf("JIRA API %s %s: %s (status %d)",
			method, path, truncate(string(respBody), 200), resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// --- Search (v3 API) ---

type searchRequest struct {
	JQL        string   `json:"jql"`
	MaxResults int      `json:"maxResults"`
	Fields     []string `json:"fields"`
}

type searchResponse struct {
	Issues []apiIssue `json:"issues"`
	Total  int        `json:"total"`
}

type apiIssue struct {
	Key    string    `json:"key"`
	Fields apiFields `json:"fields"`
}

type apiFields struct {
	Summary  string       `json:"summary"`
	Status   apiName      `json:"status"`
	Priority *apiName     `json:"priority"`
	Updated  string       `json:"updated"`
	Labels   []string     `json:"labels"`
	Type     apiName      `json:"issuetype"`
	Project  apiKeyedName `json:"project"`
	Assignee *apiAssignee `json:"assignee"`
}

type apiAssignee struct {
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
}

type apiName struct {
	Name string `json:"name"`
}

type apiKeyedName struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (c *Client) searchIssues(jql string, max int) ([]Issue, error) {
	req := searchRequest{
		JQL:        jql,
		MaxResults: max,
		Fields:     []string{"summary", "status", "priority", "updated", "assignee"},
	}
	var resp searchResponse
	if err := c.doRequest("POST", "/rest/api/3/search/jql", req, &resp); err != nil {
		return nil, err
	}
	slog.Info("search returned", "count", len(resp.Issues), "total", resp.Total,
		"jql_prefix", truncate(jql, 60))
	return parseAPIIssues(resp.Issues), nil
}

func parseAPIIssues(raw []apiIssue) []Issue {
	issues := make([]Issue, 0, len(raw))
	for _, r := range raw {
		var updated time.Time
		if r.Fields.Updated != "" {
			updated, _ = time.Parse("2006-01-02T15:04:05.000-0700", r.Fields.Updated)
		}
		pri := ""
		if r.Fields.Priority != nil {
			pri = r.Fields.Priority.Name
		}
		assignee := ""
		if r.Fields.Assignee != nil {
			assignee = r.Fields.Assignee.EmailAddress
		}
		issues = append(issues, Issue{
			Key:      r.Key,
			Status:   r.Fields.Status.Name,
			Priority: pri,
			Summary:  r.Fields.Summary,
			Updated:  updated,
			Assignee: assignee,
		})
	}
	return issues
}

func (c *Client) GetOpenIssues() ([]Issue, error) {
	jql := fmt.Sprintf(
		"assignee = %s AND project = %s "+
			"AND status NOT IN (Done, Closed, Resolved) "+
			"ORDER BY priority ASC, status ASC",
		c.assignee, c.project,
	)
	slog.Debug("get_open_issues", "jql", jql)
	return c.searchIssues(jql, 100)
}

func (c *Client) GetOpenIssuesSince(since time.Time) ([]Issue, error) {
	jql := fmt.Sprintf(
		"assignee = %s AND project = %s "+
			"AND status NOT IN (Done, Closed, Resolved) "+
			"AND updated >= \"%s\" "+
			"ORDER BY priority ASC, status ASC",
		c.assignee, c.project, since.Format("2006-01-02 15:04"),
	)
	slog.Debug("get_open_issues_since", "jql", jql, "since", since)
	return c.searchIssues(jql, 100)
}

// SearchJQL runs an arbitrary JQL query and returns matching issues.
func (c *Client) SearchJQL(jql string, max int) ([]Issue, error) {
	slog.Info("search_jql", "jql", jql, "max", max)
	return c.searchIssues(jql, max)
}

// --- Boards & Sprints (Agile REST API — still v1.0) ---

type boardListResponse struct {
	Values []apiBoard `json:"values"`
}

type apiBoard struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type sprintListResponse struct {
	Values []apiSprint `json:"values"`
}

type apiSprint struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

func (c *Client) GetBoardIssues() ([]BoardInfo, error) {
	slog.Debug("fetching boards", "project", c.project)
	path := fmt.Sprintf("/rest/agile/1.0/board?projectKeyOrId=%s", c.project)
	var boards boardListResponse
	if err := c.doRequest("GET", path, nil, &boards); err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	slog.Info("found boards", "count", len(boards.Values))

	var results []BoardInfo
	for _, board := range boards.Values {
		slog.Debug("checking board", "name", board.Name, "id", board.ID, "type", board.Type)

		if info := c.getSprintIssues(board); info != nil {
			results = append(results, *info)
			continue
		}

		if info := c.getKanbanIssues(board); info != nil {
			results = append(results, *info)
		}
	}
	return results, nil
}

func (c *Client) getSprintIssues(board apiBoard) *BoardInfo {
	path := fmt.Sprintf("/rest/agile/1.0/board/%d/sprint?state=active", board.ID)
	var sprints sprintListResponse
	if err := c.doRequest("GET", path, nil, &sprints); err != nil {
		slog.Warn("board does not support sprints",
			"board", board.Name, "id", board.ID, "type", board.Type, "error", err)
		return nil
	}
	if len(sprints.Values) == 0 {
		slog.Debug("no active sprints", "board", board.Name)
		return nil
	}

	sprint := sprints.Values[0]
	slog.Info("active sprint", "name", sprint.Name, "id", sprint.ID, "board", board.Name)

	jql := fmt.Sprintf(
		"assignee = %s AND sprint = %d AND project = %s ORDER BY priority ASC",
		c.assignee, sprint.ID, c.project,
	)
	issues, err := c.searchIssues(jql, 50)
	if err != nil {
		slog.Warn("failed to fetch sprint issues", "sprint", sprint.Name, "error", err)
		return nil
	}
	if len(issues) == 0 {
		return nil
	}

	for i := range issues {
		issues[i].Board = board.Name
		issues[i].Sprint = sprint.Name
	}

	return &BoardInfo{
		Name:       board.Name,
		BoardType:  "scrum",
		SprintName: sprint.Name,
		Issues:     issues,
	}
}

func (c *Client) getKanbanIssues(board apiBoard) *BoardInfo {
	// Use board-specific agile endpoint so each board only shows its own issues
	jql := fmt.Sprintf(
		"assignee = %s AND status NOT IN (Done, Closed, Resolved) "+
			"ORDER BY priority ASC, status ASC",
		c.assignee,
	)
	path := fmt.Sprintf("/rest/agile/1.0/board/%d/issue?jql=%s&maxResults=50",
		board.ID, url.QueryEscape(jql))
	slog.Debug("kanban issues", "board", board.Name, "path", path)

	var resp searchResponse
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		slog.Warn("failed to fetch kanban issues", "board", board.Name, "error", err)
		return nil
	}
	issues := parseAPIIssues(resp.Issues)
	slog.Info("board issues", "board", board.Name, "count", len(issues))
	if len(issues) == 0 {
		return nil
	}
	for i := range issues {
		issues[i].Board = board.Name
	}
	return &BoardInfo{
		Name:      board.Name,
		BoardType: "kanban",
		Issues:    issues,
	}
}

// --- Create Issue (v3 API) ---

type createIssueRequest struct {
	Fields createFields `json:"fields"`
}

type createFields struct {
	Project     apiKeyedName `json:"project"`
	IssueType   apiName      `json:"issuetype"`
	Summary     string       `json:"summary"`
	Description any          `json:"description"`
	Priority    apiName      `json:"priority"`
	Labels      []string     `json:"labels"`
}

type createIssueResponse struct {
	Key string `json:"key"`
}

func (c *Client) CreateEpic(summary, description, priority string, labels []string) (*Issue, error) {
	slog.Info("creating EPIC", "summary", summary)

	// v3 API uses Atlassian Document Format for description
	adfDesc := map[string]any{
		"version": 1,
		"type":    "doc",
		"content": []map[string]any{
			{
				"type": "paragraph",
				"content": []map[string]any{
					{"type": "text", "text": description},
				},
			},
		},
	}

	req := createIssueRequest{
		Fields: createFields{
			Project:     apiKeyedName{Key: c.project},
			IssueType:   apiName{Name: "Epic"},
			Summary:     summary,
			Description: adfDesc,
			Priority:    apiName{Name: priority},
			Labels:      labels,
		},
	}

	var resp createIssueResponse
	if err := c.doRequest("POST", "/rest/api/3/issue", req, &resp); err != nil {
		return nil, fmt.Errorf("create epic: %w", err)
	}
	slog.Info("EPIC created", "key", resp.Key)
	return &Issue{
		Key:     resp.Key,
		Summary: summary,
	}, nil
}

// --- Get / Update Issue (v3 API) ---

type apiIssueDetail struct {
	Key    string          `json:"key"`
	Fields apiDetailFields `json:"fields"`
}

type apiDetailFields struct {
	Summary     string      `json:"summary"`
	Description any         `json:"description"` // ADF document
	Status      apiName     `json:"status"`
	Priority    *apiName    `json:"priority"`
	Labels      []string    `json:"labels"`
	Type        apiName     `json:"issuetype"`
}

// GetIssue fetches full issue details including description.
func (c *Client) GetIssue(key string) (*IssueDetail, error) {
	path := fmt.Sprintf("/rest/api/3/issue/%s?fields=summary,description,status,priority,labels,issuetype", key)
	slog.Info("fetching issue", "key", key)

	var raw apiIssueDetail
	if err := c.doRequest("GET", path, nil, &raw); err != nil {
		return nil, fmt.Errorf("get issue %s: %w", key, err)
	}

	pri := ""
	if raw.Fields.Priority != nil {
		pri = raw.Fields.Priority.Name
	}

	desc := extractADFText(raw.Fields.Description)

	return &IssueDetail{
		Key:         raw.Key,
		Summary:     raw.Fields.Summary,
		Description: desc,
		Status:      raw.Fields.Status.Name,
		Priority:    pri,
		Labels:      raw.Fields.Labels,
		IssueType:   raw.Fields.Type.Name,
	}, nil
}

// UpdateIssue updates fields on an existing issue.
func (c *Client) UpdateIssue(key string, description string, labels []string) error {
	slog.Info("updating issue", "key", key)

	fields := map[string]any{
		"description": textToADF(description),
		"labels":      labels,
	}
	body := map[string]any{"fields": fields}

	if err := c.doRequest("PUT", fmt.Sprintf("/rest/api/3/issue/%s", key), body, nil); err != nil {
		return fmt.Errorf("update issue %s: %w", key, err)
	}
	slog.Info("issue updated", "key", key)
	return nil
}

// textToADF converts plain text to Atlassian Document Format.
func textToADF(text string) map[string]any {
	return map[string]any{
		"version": 1,
		"type":    "doc",
		"content": []map[string]any{
			{
				"type": "paragraph",
				"content": []map[string]any{
					{"type": "text", "text": text},
				},
			},
		},
	}
}

// extractADFText recursively extracts plain text from an ADF document.
func extractADFText(adf any) string {
	if adf == nil {
		return ""
	}
	doc, ok := adf.(map[string]any)
	if !ok {
		return ""
	}
	var result []byte
	extractTextNodes(doc, &result)
	return string(result)
}

func extractTextNodes(node map[string]any, buf *[]byte) {
	if node["type"] == "text" {
		if text, ok := node["text"].(string); ok {
			*buf = append(*buf, text...)
		}
		return
	}
	// Add newline between paragraphs
	if node["type"] == "paragraph" && len(*buf) > 0 {
		*buf = append(*buf, '\n')
	}
	content, ok := node["content"].([]any)
	if !ok {
		return
	}
	for _, child := range content {
		if childMap, ok := child.(map[string]any); ok {
			extractTextNodes(childMap, buf)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
