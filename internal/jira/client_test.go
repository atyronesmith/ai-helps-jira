package jira

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestClient(ts *httptest.Server) *Client {
	return &Client{
		http:     ts.Client(),
		base:     ts.URL,
		email:    "test@example.com",
		token:    "test-token",
		project:  "TEST",
		assignee: "test@example.com",
	}
}

func jsonResponse(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

// --- Search tests ---

func TestSearchJQL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}

		payload := readBody(t, r)
		if payload["jql"] != "project = TEST" {
			t.Errorf("expected jql 'project = TEST', got %v", payload["jql"])
		}
		if payload["maxResults"] != float64(25) {
			t.Errorf("expected maxResults 25, got %v", payload["maxResults"])
		}

		jsonResponse(t, w, 200, map[string]any{
			"issues": []map[string]any{
				{
					"key": "TEST-1",
					"fields": map[string]any{
						"summary":  "First issue",
						"status":   map[string]any{"name": "Open"},
						"priority": map[string]any{"name": "High"},
						"updated":  "2026-01-15T10:00:00.000+0000",
						"assignee": map[string]any{
							"emailAddress": "alice@example.com",
							"displayName":  "Alice",
							"accountId":    "abc123",
						},
					},
				},
				{
					"key": "TEST-2",
					"fields": map[string]any{
						"summary":  "Second issue",
						"status":   map[string]any{"name": "In Progress"},
						"priority": nil,
						"updated":  "2026-01-16T10:00:00.000+0000",
						"assignee": nil,
					},
				},
			},
			"total": 2,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	issues, err := c.SearchJQL("project = TEST", 25)
	if err != nil {
		t.Fatalf("SearchJQL: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Key != "TEST-1" {
		t.Errorf("expected key TEST-1, got %s", issues[0].Key)
	}
	if issues[0].Summary != "First issue" {
		t.Errorf("expected summary 'First issue', got %s", issues[0].Summary)
	}
	if issues[0].Status != "Open" {
		t.Errorf("expected status Open, got %s", issues[0].Status)
	}
	if issues[0].Priority != "High" {
		t.Errorf("expected priority High, got %s", issues[0].Priority)
	}
	if issues[0].Assignee != "alice@example.com" {
		t.Errorf("expected assignee alice@example.com, got %s", issues[0].Assignee)
	}
	if issues[1].Priority != "" {
		t.Errorf("expected empty priority for nil, got %s", issues[1].Priority)
	}
	if issues[1].Assignee != "" {
		t.Errorf("expected empty assignee for nil, got %s", issues[1].Assignee)
	}
}

func TestGetOpenIssues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := readBody(t, r)
		jql := payload["jql"].(string)
		if !strings.Contains(jql, "assignee = test@example.com") {
			t.Errorf("JQL should contain assignee filter, got %s", jql)
		}
		if !strings.Contains(jql, "project = TEST") {
			t.Errorf("JQL should contain project filter, got %s", jql)
		}
		if !strings.Contains(jql, "status NOT IN") {
			t.Errorf("JQL should filter closed statuses, got %s", jql)
		}

		jsonResponse(t, w, 200, map[string]any{
			"issues": []map[string]any{},
			"total":  0,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetOpenIssues()
	if err != nil {
		t.Fatalf("GetOpenIssues: %v", err)
	}
}

func TestGetOpenIssuesSince(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := readBody(t, r)
		jql := payload["jql"].(string)
		if !strings.Contains(jql, "updated >=") {
			t.Errorf("JQL should contain updated >= filter, got %s", jql)
		}

		jsonResponse(t, w, 200, map[string]any{
			"issues": []map[string]any{},
			"total":  0,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetOpenIssuesSince(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("GetOpenIssuesSince: %v", err)
	}
}

func TestSearchByRelease(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := readBody(t, r)
		jql := payload["jql"].(string)
		if !strings.Contains(jql, "fixVersion") {
			t.Errorf("JQL should contain fixVersion, got %s", jql)
		}
		if !strings.Contains(jql, "v1.0") {
			t.Errorf("JQL should contain release name, got %s", jql)
		}

		jsonResponse(t, w, 200, map[string]any{
			"issues": []map[string]any{},
			"total":  0,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.SearchByRelease("v1.0")
	if err != nil {
		t.Fatalf("SearchByRelease: %v", err)
	}
}

// --- Issue detail tests ---

func TestGetIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/TEST-1") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		jsonResponse(t, w, 200, map[string]any{
			"key": "TEST-1",
			"fields": map[string]any{
				"summary": "Test issue",
				"description": map[string]any{
					"version": 1,
					"type":    "doc",
					"content": []map[string]any{
						{
							"type": "paragraph",
							"content": []map[string]any{
								{"type": "text", "text": "Issue description"},
							},
						},
					},
				},
				"status":    map[string]any{"name": "Open"},
				"priority":  map[string]any{"name": "High"},
				"labels":    []string{"bug", "urgent"},
				"issuetype": map[string]any{"name": "Bug"},
				"assignee": map[string]any{
					"displayName": "Alice",
					"accountId":   "abc123",
				},
				"parent": map[string]any{
					"key": "TEST-0",
					"fields": map[string]any{
						"summary": "Parent epic",
						"status":  map[string]any{"name": "Open"},
					},
				},
				"updated": "2026-01-15T10:00:00.000+0000",
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	detail, err := c.GetIssue("TEST-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if detail.Key != "TEST-1" {
		t.Errorf("expected key TEST-1, got %s", detail.Key)
	}
	if detail.Summary != "Test issue" {
		t.Errorf("expected summary 'Test issue', got %s", detail.Summary)
	}
	if detail.Description != "Issue description" {
		t.Errorf("expected description 'Issue description', got %s", detail.Description)
	}
	if detail.IssueType != "Bug" {
		t.Errorf("expected type Bug, got %s", detail.IssueType)
	}
	if detail.Assignee != "Alice" {
		t.Errorf("expected assignee Alice, got %s", detail.Assignee)
	}
	if detail.AssigneeID != "abc123" {
		t.Errorf("expected assignee ID abc123, got %s", detail.AssigneeID)
	}
	if detail.ParentKey != "TEST-0" {
		t.Errorf("expected parent key TEST-0, got %s", detail.ParentKey)
	}
	if len(detail.Labels) != 2 || detail.Labels[0] != "bug" {
		t.Errorf("expected labels [bug urgent], got %v", detail.Labels)
	}
}

func TestGetIssue_NilFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]any{
			"key": "TEST-2",
			"fields": map[string]any{
				"summary":     "Minimal issue",
				"description": nil,
				"status":      map[string]any{"name": "Open"},
				"priority":    nil,
				"labels":      []string{},
				"issuetype":   map[string]any{"name": "Task"},
				"assignee":    nil,
				"parent":      nil,
				"updated":     "2026-01-15T10:00:00.000+0000",
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	detail, err := c.GetIssue("TEST-2")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if detail.Priority != "" {
		t.Errorf("expected empty priority, got %s", detail.Priority)
	}
	if detail.Assignee != "" {
		t.Errorf("expected empty assignee, got %s", detail.Assignee)
	}
	if detail.Description != "" {
		t.Errorf("expected empty description, got %s", detail.Description)
	}
	if detail.ParentKey != "" {
		t.Errorf("expected empty parent, got %s", detail.ParentKey)
	}
}

func TestUpdateIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/rest/api/3/issue/TEST-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		fields := payload["fields"].(map[string]any)
		labels := fields["labels"].([]any)
		if len(labels) != 2 {
			t.Errorf("expected 2 labels, got %d", len(labels))
		}
		if fields["description"] == nil {
			t.Error("expected ADF description, got nil")
		}

		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.UpdateIssue("TEST-1", "New description", []string{"bug", "urgent"})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
}

// --- Comments tests ---

func TestGetComments(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}

		jsonResponse(t, w, 200, map[string]any{
			"key": "TEST-1",
			"fields": map[string]any{
				"comment": map[string]any{
					"comments": []map[string]any{
						{
							"id": "10001",
							"author": map[string]any{
								"displayName":  "Alice",
								"emailAddress": "alice@example.com",
								"accountId":    "abc123",
							},
							"body": map[string]any{
								"version": 1,
								"type":    "doc",
								"content": []map[string]any{
									{
										"type": "paragraph",
										"content": []map[string]any{
											{"type": "text", "text": "Nice work!"},
										},
									},
								},
							},
							"created": "2026-01-15T10:00:00.000+0000",
						},
					},
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	comments, err := c.GetComments("TEST-1")
	if err != nil {
		t.Fatalf("GetComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].ID != "10001" {
		t.Errorf("expected ID 10001, got %s", comments[0].ID)
	}
	if comments[0].AuthorName != "Alice" {
		t.Errorf("expected author Alice, got %s", comments[0].AuthorName)
	}
	if comments[0].Body != "Nice work!" {
		t.Errorf("expected body 'Nice work!', got %s", comments[0].Body)
	}
	if comments[0].IssueKey != "TEST-1" {
		t.Errorf("expected issue key TEST-1, got %s", comments[0].IssueKey)
	}
}

// --- Links tests ---

func TestGetIssueWithLinks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]any{
			"key": "TEST-1",
			"fields": map[string]any{
				"summary":     "Parent issue",
				"description": nil,
				"status":      map[string]any{"name": "Open"},
				"priority":    map[string]any{"name": "High"},
				"labels":      []string{},
				"issuetype":   map[string]any{"name": "Epic"},
				"issuelinks": []map[string]any{
					{
						"id":   "1",
						"type": map[string]any{"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
						"outwardIssue": map[string]any{
							"key": "TEST-2",
							"fields": map[string]any{
								"summary":   "Blocked issue",
								"status":    map[string]any{"name": "Open"},
								"issuetype": map[string]any{"name": "Task"},
							},
						},
					},
				},
				"subtasks": []map[string]any{
					{
						"key": "TEST-3",
						"fields": map[string]any{
							"summary":   "Sub-task",
							"status":    map[string]any{"name": "Done"},
							"issuetype": map[string]any{"name": "Sub-task"},
						},
					},
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	detail, links, err := c.GetIssueWithLinks("TEST-1")
	if err != nil {
		t.Fatalf("GetIssueWithLinks: %v", err)
	}
	if detail.Key != "TEST-1" {
		t.Errorf("expected key TEST-1, got %s", detail.Key)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links (1 issue link + 1 subtask), got %d", len(links))
	}
	if links[0].TargetKey != "TEST-2" {
		t.Errorf("expected target TEST-2, got %s", links[0].TargetKey)
	}
	if links[0].LinkType != "Blocks" {
		t.Errorf("expected link type Blocks, got %s", links[0].LinkType)
	}
	if links[1].TargetKey != "TEST-3" {
		t.Errorf("expected subtask TEST-3, got %s", links[1].TargetKey)
	}
	if links[1].LinkType != "Subtask" {
		t.Errorf("expected link type Subtask, got %s", links[1].LinkType)
	}
}

func TestGetChildIssues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := readBody(t, r)
		jql := payload["jql"].(string)
		if !strings.Contains(jql, "parent = TEST-1") {
			t.Errorf("JQL should contain parent filter, got %s", jql)
		}

		jsonResponse(t, w, 200, map[string]any{
			"issues": []map[string]any{
				{
					"key": "TEST-2",
					"fields": map[string]any{
						"summary":   "Child issue",
						"status":    map[string]any{"name": "Open"},
						"priority":  map[string]any{"name": "Medium"},
						"issuetype": map[string]any{"name": "Story"},
					},
				},
			},
			"total": 1,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	links, err := c.GetChildIssues("TEST-1")
	if err != nil {
		t.Fatalf("GetChildIssues: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 child, got %d", len(links))
	}
	if links[0].SourceKey != "TEST-1" {
		t.Errorf("expected source TEST-1, got %s", links[0].SourceKey)
	}
	if links[0].TargetKey != "TEST-2" {
		t.Errorf("expected target TEST-2, got %s", links[0].TargetKey)
	}
}

// --- Transitions tests ---

func TestGetTransitions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/rest/api/3/issue/TEST-1/transitions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		jsonResponse(t, w, 200, map[string]any{
			"transitions": []map[string]any{
				{"id": "11", "name": "Start Progress", "to": map[string]any{"name": "In Progress"}},
				{"id": "21", "name": "Done", "to": map[string]any{"name": "Done"}},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	transitions, err := c.GetTransitions("TEST-1")
	if err != nil {
		t.Fatalf("GetTransitions: %v", err)
	}
	if len(transitions) != 2 {
		t.Fatalf("expected 2 transitions, got %d", len(transitions))
	}
	if transitions[0].ID != "11" {
		t.Errorf("expected ID 11, got %s", transitions[0].ID)
	}
	if transitions[0].Name != "Start Progress" {
		t.Errorf("expected name 'Start Progress', got %s", transitions[0].Name)
	}
	if transitions[0].To != "In Progress" {
		t.Errorf("expected to 'In Progress', got %s", transitions[0].To)
	}
}

func TestTransitionIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue/TEST-1/transitions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		transition := payload["transition"].(map[string]any)
		if transition["id"] != "11" {
			t.Errorf("expected transition id 11, got %v", transition["id"])
		}

		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.TransitionIssue("TEST-1", "11")
	if err != nil {
		t.Fatalf("TransitionIssue: %v", err)
	}
}

// --- Comment tests ---

func TestAddComment(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue/TEST-1/comment" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		body := payload["body"].(map[string]any)
		if body["type"] != "doc" {
			t.Errorf("expected ADF doc body, got %v", body["type"])
		}

		jsonResponse(t, w, 201, map[string]any{
			"id": "10001",
			"author": map[string]any{
				"displayName":  "Test User",
				"emailAddress": "test@example.com",
				"accountId":    "abc123",
			},
			"body":    payload["body"],
			"created": "2026-01-15T10:00:00.000+0000",
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	comment, err := c.AddComment("TEST-1", "Hello world")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if comment.ID != "10001" {
		t.Errorf("expected ID 10001, got %s", comment.ID)
	}
	if comment.IssueKey != "TEST-1" {
		t.Errorf("expected issue key TEST-1, got %s", comment.IssueKey)
	}
	if comment.Body != "Hello world" {
		t.Errorf("expected body 'Hello world', got %s", comment.Body)
	}
}

// --- Create/Edit issue tests ---

func TestCreateIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		fields := payload["fields"].(map[string]any)

		project := fields["project"].(map[string]any)
		if project["key"] != "TEST" {
			t.Errorf("expected project TEST, got %v", project["key"])
		}

		issueType := fields["issuetype"].(map[string]any)
		if issueType["name"] != "Story" {
			t.Errorf("expected type Story, got %v", issueType["name"])
		}

		if fields["summary"] != "New story" {
			t.Errorf("expected summary 'New story', got %v", fields["summary"])
		}

		priority := fields["priority"].(map[string]any)
		if priority["name"] != "High" {
			t.Errorf("expected priority High, got %v", priority["name"])
		}

		jsonResponse(t, w, 201, map[string]any{"key": "TEST-99"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	issue, err := c.CreateIssue(CreateIssueParams{
		IssueType:   "Story",
		Summary:     "New story",
		Description: "Story description",
		Priority:    "High",
		Labels:      []string{"feature"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Key != "TEST-99" {
		t.Errorf("expected key TEST-99, got %s", issue.Key)
	}
}

func TestCreateEpic(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := readBody(t, r)
		fields := payload["fields"].(map[string]any)
		issueType := fields["issuetype"].(map[string]any)
		if issueType["name"] != "Epic" {
			t.Errorf("expected type Epic, got %v", issueType["name"])
		}

		jsonResponse(t, w, 201, map[string]any{"key": "TEST-100"})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	issue, err := c.CreateEpic("New epic", "Epic description", "High", []string{"initiative"})
	if err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if issue.Key != "TEST-100" {
		t.Errorf("expected key TEST-100, got %s", issue.Key)
	}
}

func TestEditIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/rest/api/3/issue/TEST-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		fields := payload["fields"].(map[string]any)

		// processFieldsForAPI should convert priority to name object
		priority := fields["priority"].(map[string]any)
		if priority["name"] != "Low" {
			t.Errorf("expected priority name Low, got %v", priority["name"])
		}

		// description should be converted to ADF
		desc := fields["description"].(map[string]any)
		if desc["type"] != "doc" {
			t.Errorf("expected ADF doc, got %v", desc["type"])
		}

		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.EditIssue("TEST-1", map[string]any{
		"priority":    "Low",
		"description": "Updated description",
	})
	if err != nil {
		t.Fatalf("EditIssue: %v", err)
	}
}

// --- User search tests ---

func TestSearchUsers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/rest/api/3/user/search") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "alice" {
			t.Errorf("expected query 'alice', got %s", r.URL.Query().Get("query"))
		}

		jsonResponse(t, w, 200, []map[string]any{
			{
				"accountId":    "abc123",
				"displayName":  "Alice Smith",
				"emailAddress": "alice@example.com",
				"active":       true,
			},
			{
				"accountId":    "inactive1",
				"displayName":  "Alice Old",
				"emailAddress": "alice.old@example.com",
				"active":       false,
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	users, err := c.SearchUsers("alice")
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	// Inactive users should be filtered out
	if len(users) != 1 {
		t.Fatalf("expected 1 active user, got %d", len(users))
	}
	if users[0].AccountID != "abc123" {
		t.Errorf("expected account ID abc123, got %s", users[0].AccountID)
	}
	if users[0].DisplayName != "Alice Smith" {
		t.Errorf("expected name 'Alice Smith', got %s", users[0].DisplayName)
	}
}

// --- Link issues tests ---

func TestLinkIssues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issueLink" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		linkType := payload["type"].(map[string]any)
		if linkType["name"] != "Blocks" {
			t.Errorf("expected link type Blocks, got %v", linkType["name"])
		}
		inward := payload["inwardIssue"].(map[string]any)
		if inward["key"] != "TEST-1" {
			t.Errorf("expected inward key TEST-1, got %v", inward["key"])
		}
		outward := payload["outwardIssue"].(map[string]any)
		if outward["key"] != "TEST-2" {
			t.Errorf("expected outward key TEST-2, got %v", outward["key"])
		}

		w.WriteHeader(201)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.LinkIssues("TEST-1", "TEST-2", "Blocks")
	if err != nil {
		t.Fatalf("LinkIssues: %v", err)
	}
}

// --- Attachment tests ---

func TestAttachFile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue/TEST-1/attachments" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		if r.Header.Get("X-Atlassian-Token") != "no-check" {
			t.Error("missing X-Atlassian-Token header")
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart content type, got %s", r.Header.Get("Content-Type"))
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("parse form file: %v", err)
		}
		defer file.Close()
		if header.Filename != "test.txt" {
			t.Errorf("expected filename test.txt, got %s", header.Filename)
		}
		content, _ := io.ReadAll(file)
		if string(content) != "file content" {
			t.Errorf("expected file content 'file content', got %s", string(content))
		}

		jsonResponse(t, w, 200, []map[string]any{
			{"id": "att1", "filename": "test.txt"},
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("file content"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(ts)
	filename, err := c.AttachFile("TEST-1", filePath)
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	if filename != "test.txt" {
		t.Errorf("expected filename test.txt, got %s", filename)
	}
}

// --- Worklog tests ---

func TestAddWorklog(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue/TEST-1/worklog" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		if payload["timeSpent"] != "2h 30m" {
			t.Errorf("expected timeSpent '2h 30m', got %v", payload["timeSpent"])
		}
		if payload["comment"] == nil {
			t.Error("expected ADF comment body")
		}

		jsonResponse(t, w, 201, map[string]any{
			"id": "wl1",
			"author": map[string]any{
				"displayName": "Test User",
			},
			"timeSpent":        "2h 30m",
			"timeSpentSeconds": 9000,
			"started":          "2026-01-15T09:00:00.000+0000",
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	wl, err := c.AddWorklog("TEST-1", "2h 30m", "Worked on feature", "")
	if err != nil {
		t.Fatalf("AddWorklog: %v", err)
	}
	if wl.ID != "wl1" {
		t.Errorf("expected ID wl1, got %s", wl.ID)
	}
	if wl.TimeSpent != "2h 30m" {
		t.Errorf("expected timeSpent '2h 30m', got %s", wl.TimeSpent)
	}
	if wl.TimeSpentSeconds != 9000 {
		t.Errorf("expected 9000 seconds, got %d", wl.TimeSpentSeconds)
	}
}

func TestAddWorklog_NoComment(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := readBody(t, r)
		if _, ok := payload["comment"]; ok {
			t.Error("comment should not be present when empty")
		}

		jsonResponse(t, w, 201, map[string]any{
			"id":               "wl2",
			"author":           map[string]any{"displayName": "Test"},
			"timeSpent":        "1h",
			"timeSpentSeconds": 3600,
			"started":          "2026-01-15T09:00:00.000+0000",
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.AddWorklog("TEST-1", "1h", "", "")
	if err != nil {
		t.Fatalf("AddWorklog: %v", err)
	}
}

// --- GetMyself tests ---

func TestGetMyself(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/rest/api/3/myself" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		jsonResponse(t, w, 200, map[string]any{
			"accountId":    "self-123",
			"displayName":  "Test User",
			"emailAddress": "test@example.com",
			"active":       true,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	accountID, err := c.GetMyself()
	if err != nil {
		t.Fatalf("GetMyself: %v", err)
	}
	if accountID != "self-123" {
		t.Errorf("expected account ID self-123, got %s", accountID)
	}
}

// --- Watch tests ---

func TestWatchIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]any{
			"accountId":   "self-123",
			"displayName": "Test User",
			"active":      true,
		})
	})
	mux.HandleFunc("/rest/api/3/issue/TEST-1/watchers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var accountID string
		if err := json.Unmarshal(body, &accountID); err != nil {
			t.Fatalf("expected JSON string body, got %s", string(body))
		}
		if accountID != "self-123" {
			t.Errorf("expected account ID self-123, got %s", accountID)
		}
		w.WriteHeader(204)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := newTestClient(ts)
	err := c.WatchIssue("TEST-1")
	if err != nil {
		t.Fatalf("WatchIssue: %v", err)
	}
}

func TestUnwatchIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]any{
			"accountId":   "self-123",
			"displayName": "Test User",
			"active":      true,
		})
	})
	mux.HandleFunc("/rest/api/3/issue/TEST-1/watchers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Query().Get("accountId") != "self-123" {
			t.Errorf("expected accountId=self-123, got %s", r.URL.Query().Get("accountId"))
		}
		w.WriteHeader(204)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := newTestClient(ts)
	err := c.UnwatchIssue("TEST-1")
	if err != nil {
		t.Fatalf("UnwatchIssue: %v", err)
	}
}

// --- Label tests ---

func TestAddLabels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/rest/api/3/issue/TEST-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		update := payload["update"].(map[string]any)
		labels := update["labels"].([]any)
		if len(labels) != 2 {
			t.Fatalf("expected 2 label ops, got %d", len(labels))
		}
		op0 := labels[0].(map[string]any)
		if op0["add"] != "bug" {
			t.Errorf("expected add:bug, got %v", op0)
		}
		op1 := labels[1].(map[string]any)
		if op1["add"] != "urgent" {
			t.Errorf("expected add:urgent, got %v", op1)
		}

		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.AddLabels("TEST-1", []string{"bug", "urgent"})
	if err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
}

func TestRemoveLabels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/rest/api/3/issue/TEST-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}

		payload := readBody(t, r)
		update := payload["update"].(map[string]any)
		labels := update["labels"].([]any)
		if len(labels) != 1 {
			t.Fatalf("expected 1 label op, got %d", len(labels))
		}
		op := labels[0].(map[string]any)
		if op["remove"] != "obsolete" {
			t.Errorf("expected remove:obsolete, got %v", op)
		}

		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.RemoveLabels("TEST-1", []string{"obsolete"})
	if err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
}

// --- Sprint tests ---

func TestListBoards(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/rest/agile/1.0/board") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("projectKeyOrId") != "TEST" {
			t.Errorf("expected project TEST, got %s", r.URL.Query().Get("projectKeyOrId"))
		}

		jsonResponse(t, w, 200, map[string]any{
			"values": []map[string]any{
				{"id": 1, "name": "Scrum Board", "type": "scrum"},
				{"id": 2, "name": "Kanban Board", "type": "kanban"},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	boards, err := c.ListBoards()
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(boards) != 2 {
		t.Fatalf("expected 2 boards, got %d", len(boards))
	}
	if boards[0].ID != 1 || boards[0].Name != "Scrum Board" || boards[0].Type != "scrum" {
		t.Errorf("unexpected board: %+v", boards[0])
	}
}

func TestListSprints(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/rest/agile/1.0/board/1/sprint" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("state") != "active" {
			t.Errorf("expected state=active, got %s", r.URL.Query().Get("state"))
		}

		jsonResponse(t, w, 200, map[string]any{
			"values": []map[string]any{
				{
					"id":        10,
					"name":      "Sprint 1",
					"state":     "active",
					"startDate": "2026-01-13T00:00:00.000Z",
					"endDate":   "2026-01-27T00:00:00.000Z",
				},
			},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	sprints, err := c.ListSprints(1, "active")
	if err != nil {
		t.Fatalf("ListSprints: %v", err)
	}
	if len(sprints) != 1 {
		t.Fatalf("expected 1 sprint, got %d", len(sprints))
	}
	if sprints[0].ID != 10 {
		t.Errorf("expected ID 10, got %d", sprints[0].ID)
	}
	if sprints[0].Name != "Sprint 1" {
		t.Errorf("expected name 'Sprint 1', got %s", sprints[0].Name)
	}
	if sprints[0].State != "active" {
		t.Errorf("expected state active, got %s", sprints[0].State)
	}
	if sprints[0].StartDate.IsZero() {
		t.Error("expected non-zero start date")
	}
}

func TestListSprints_NoStateFilter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "" {
			t.Errorf("expected no state filter, got %s", r.URL.Query().Get("state"))
		}

		jsonResponse(t, w, 200, map[string]any{
			"values": []map[string]any{},
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.ListSprints(1, "")
	if err != nil {
		t.Fatalf("ListSprints: %v", err)
	}
}

// --- Vote tests ---

func TestVoteIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/issue/TEST-1/votes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.VoteIssue("TEST-1")
	if err != nil {
		t.Fatalf("VoteIssue: %v", err)
	}
}

func TestUnvoteIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/rest/api/3/issue/TEST-1/votes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.UnvoteIssue("TEST-1")
	if err != nil {
		t.Fatalf("UnvoteIssue: %v", err)
	}
}

// --- Board issues (multi-endpoint) tests ---

func TestGetBoardIssues(t *testing.T) {
	mux := http.NewServeMux()

	// List boards
	mux.HandleFunc("/rest/agile/1.0/board", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/agile/1.0/board" {
			http.NotFound(w, r)
			return
		}
		jsonResponse(t, w, 200, map[string]any{
			"values": []map[string]any{
				{"id": 1, "name": "Scrum Board", "type": "scrum"},
			},
		})
	})

	// Active sprints for board 1
	mux.HandleFunc("/rest/agile/1.0/board/1/sprint", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]any{
			"values": []map[string]any{
				{"id": 10, "name": "Sprint 1", "state": "active"},
			},
		})
	})

	// Sprint issues via search
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(t, w, 200, map[string]any{
			"issues": []map[string]any{
				{
					"key": "TEST-1",
					"fields": map[string]any{
						"summary":  "Sprint task",
						"status":   map[string]any{"name": "In Progress"},
						"priority": map[string]any{"name": "Medium"},
						"updated":  "2026-01-15T10:00:00.000+0000",
						"assignee": nil,
					},
				},
			},
			"total": 1,
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := newTestClient(ts)
	boards, err := c.GetBoardIssues()
	if err != nil {
		t.Fatalf("GetBoardIssues: %v", err)
	}
	if len(boards) != 1 {
		t.Fatalf("expected 1 board result, got %d", len(boards))
	}
	if boards[0].Name != "Scrum Board" {
		t.Errorf("expected board 'Scrum Board', got %s", boards[0].Name)
	}
	if boards[0].SprintName != "Sprint 1" {
		t.Errorf("expected sprint 'Sprint 1', got %s", boards[0].SprintName)
	}
	if len(boards[0].Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(boards[0].Issues))
	}
	if boards[0].Issues[0].Key != "TEST-1" {
		t.Errorf("expected issue TEST-1, got %s", boards[0].Issues[0].Key)
	}
}

// --- Error handling tests ---

func TestAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"errorMessages":["Issue not found"]}`))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetIssue("NOPE-1")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should contain status code, got: %s", err.Error())
	}
}

func TestAPIError_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"message":"Internal server error"}`))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	err := c.TransitionIssue("TEST-1", "11")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// --- Auth tests ---

func TestBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		jsonResponse(t, w, 200, map[string]any{
			"accountId":   "self-123",
			"displayName": "Test",
			"active":      true,
		})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	c.GetMyself()

	if gotUser != "test@example.com" {
		t.Errorf("expected user 'test@example.com', got %q", gotUser)
	}
	if gotPass != "test-token" {
		t.Errorf("expected password 'test-token', got %q", gotPass)
	}
}

// --- ADF helper tests ---

func TestTextToADF(t *testing.T) {
	adf := textToADF("hello world")
	if adf["type"] != "doc" {
		t.Errorf("expected type doc, got %v", adf["type"])
	}
	if adf["version"] != 1 {
		t.Errorf("expected version 1, got %v", adf["version"])
	}
	content := adf["content"].([]map[string]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	if content[0]["type"] != "paragraph" {
		t.Errorf("expected paragraph, got %v", content[0]["type"])
	}
}

func TestExtractADFText(t *testing.T) {
	adf := map[string]any{
		"version": 1,
		"type":    "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "Hello "},
					map[string]any{"type": "text", "text": "world"},
				},
			},
		},
	}

	got := extractADFText(adf)
	if got != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", got)
	}
}

func TestExtractADFText_Nil(t *testing.T) {
	if got := extractADFText(nil); got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("expected 'hello...', got %q", got)
	}
}
