package jira

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// --- Transitions ---

type apiTransitionResponse struct {
	Transitions []apiTransition `json:"transitions"`
}

type apiTransition struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	To   apiName `json:"to"`
}

// GetTransitions returns the available workflow transitions for an issue.
func (c *Client) GetTransitions(key string) ([]Transition, error) {
	path := fmt.Sprintf("/rest/api/3/issue/%s/transitions", key)
	slog.Info("fetching transitions", "key", key)

	var resp apiTransitionResponse
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get transitions %s: %w", key, err)
	}

	transitions := make([]Transition, 0, len(resp.Transitions))
	for _, t := range resp.Transitions {
		transitions = append(transitions, Transition{
			ID:   t.ID,
			Name: t.Name,
			To:   t.To.Name,
		})
	}
	slog.Info("transitions fetched", "key", key, "count", len(transitions))
	return transitions, nil
}

// TransitionIssue moves an issue to a new workflow status.
func (c *Client) TransitionIssue(key, transitionID string) error {
	path := fmt.Sprintf("/rest/api/3/issue/%s/transitions", key)
	slog.Info("transitioning issue", "key", key, "transition", transitionID)

	body := map[string]any{
		"transition": map[string]any{"id": transitionID},
	}
	if err := c.doRequest("POST", path, body, nil); err != nil {
		return fmt.Errorf("transition issue %s: %w", key, err)
	}
	slog.Info("issue transitioned", "key", key)
	return nil
}

// --- Comments ---

type apiAddCommentResponse struct {
	ID      string      `json:"id"`
	Author  apiAssignee `json:"author"`
	Body    any         `json:"body"`
	Created string      `json:"created"`
}

// AddComment adds a comment to an issue and returns the created comment.
func (c *Client) AddComment(key, body string) (*Comment, error) {
	path := fmt.Sprintf("/rest/api/3/issue/%s/comment", key)
	slog.Info("adding comment", "key", key)

	reqBody := map[string]any{
		"body": textToADF(body),
	}

	var resp apiAddCommentResponse
	if err := c.doRequest("POST", path, reqBody, &resp); err != nil {
		return nil, fmt.Errorf("add comment to %s: %w", key, err)
	}

	created, _ := time.Parse("2006-01-02T15:04:05.000-0700", resp.Created)
	slog.Info("comment added", "key", key, "id", resp.ID)
	return &Comment{
		ID:          resp.ID,
		IssueKey:    key,
		AuthorName:  resp.Author.DisplayName,
		AuthorEmail: resp.Author.EmailAddress,
		Body:        body,
		Created:     created,
	}, nil
}

// --- Create Issue (generalized) ---

// CreateIssueParams holds the parameters for creating a JIRA issue.
type CreateIssueParams struct {
	ProjectKey   string
	IssueType    string
	Summary      string
	Description  string
	Priority     string
	Labels       []string
	Parent       string
	AssigneeID   string
	CustomFields map[string]any
}

// CreateIssue creates a JIRA issue of any type.
func (c *Client) CreateIssue(params CreateIssueParams) (*Issue, error) {
	project := params.ProjectKey
	if project == "" {
		project = c.project
	}

	slog.Info("creating issue", "type", params.IssueType, "summary", params.Summary, "project", project)

	fields := map[string]any{
		"project":   map[string]any{"key": project},
		"issuetype": map[string]any{"name": params.IssueType},
		"summary":   params.Summary,
	}

	if params.Description != "" {
		fields["description"] = textToADF(params.Description)
	}
	if params.Priority != "" {
		fields["priority"] = map[string]any{"name": params.Priority}
	}
	if len(params.Labels) > 0 {
		fields["labels"] = params.Labels
	}
	if params.Parent != "" {
		fields["parent"] = map[string]any{"key": params.Parent}
	}
	if params.AssigneeID != "" {
		fields["assignee"] = map[string]any{"accountId": params.AssigneeID}
	}
	for k, v := range params.CustomFields {
		fields[k] = v
	}

	body := map[string]any{"fields": fields}

	var resp createIssueResponse
	if err := c.doRequest("POST", "/rest/api/3/issue", body, &resp); err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	slog.Info("issue created", "key", resp.Key, "type", params.IssueType)
	return &Issue{
		Key:     resp.Key,
		Summary: params.Summary,
	}, nil
}

// --- Edit Issue (generalized) ---

// processFieldsForAPI converts user-friendly field values to JIRA API format.
func processFieldsForAPI(fields map[string]any) map[string]any {
	processed := make(map[string]any, len(fields))
	for k, v := range fields {
		switch k {
		case "description":
			if s, ok := v.(string); ok {
				processed[k] = textToADF(s)
			} else {
				processed[k] = v
			}
		case "priority":
			if s, ok := v.(string); ok {
				processed[k] = map[string]any{"name": s}
			} else {
				processed[k] = v
			}
		case "assignee":
			if s, ok := v.(string); ok {
				processed[k] = map[string]any{"accountId": s}
			} else {
				processed[k] = v
			}
		default:
			processed[k] = v
		}
	}
	return processed
}

// EditIssue updates arbitrary fields on an existing issue.
func (c *Client) EditIssue(key string, fields map[string]any) error {
	slog.Info("editing issue", "key", key, "fields", len(fields))

	processed := processFieldsForAPI(fields)
	body := map[string]any{"fields": processed}

	if err := c.doRequest("PUT", fmt.Sprintf("/rest/api/3/issue/%s", key), body, nil); err != nil {
		return fmt.Errorf("edit issue %s: %w", key, err)
	}
	slog.Info("issue edited", "key", key)
	return nil
}

// --- User Search ---

type apiUser struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	Active       bool   `json:"active"`
}

// SearchUsers finds JIRA users by name or email.
func (c *Client) SearchUsers(query string) ([]User, error) {
	path := fmt.Sprintf("/rest/api/3/user/search?query=%s&maxResults=10", url.QueryEscape(query))
	slog.Info("searching users", "query", query)

	var resp []apiUser
	if err := c.doRequest("GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}

	users := make([]User, 0, len(resp))
	for _, u := range resp {
		if !u.Active {
			continue
		}
		users = append(users, User{
			AccountID:    u.AccountID,
			DisplayName:  u.DisplayName,
			EmailAddress: u.EmailAddress,
		})
	}
	slog.Info("users found", "count", len(users))
	return users, nil
}

// --- Issue Links ---

// LinkIssues creates a link between two issues.
func (c *Client) LinkIssues(inwardKey, outwardKey, linkType string) error {
	slog.Info("linking issues", "inward", inwardKey, "outward", outwardKey, "type", linkType)

	body := map[string]any{
		"type":         map[string]any{"name": linkType},
		"inwardIssue":  map[string]any{"key": inwardKey},
		"outwardIssue": map[string]any{"key": outwardKey},
	}

	if err := c.doRequest("POST", "/rest/api/3/issueLink", body, nil); err != nil {
		return fmt.Errorf("link issues: %w", err)
	}
	slog.Info("issues linked", "inward", inwardKey, "outward", outwardKey)
	return nil
}

// --- Attachments ---

// AttachFile uploads a file attachment to a JIRA issue.
func (c *Client) AttachFile(key, filePath string) (string, error) {
	slog.Info("attaching file", "key", key, "file", filePath)

	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", fmt.Errorf("copy file data: %w", err)
	}
	writer.Close()

	reqURL := fmt.Sprintf("%s/rest/api/3/issue/%s/attachments", c.base, key)
	req, err := http.NewRequest("POST", reqURL, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload attachment: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("attach file to %s: %s (status %d)", key, truncate(string(respBody), 200), resp.StatusCode)
	}

	slog.Info("file attached", "key", key, "file", filepath.Base(filePath))
	return filepath.Base(filePath), nil
}
