package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOriginalActivityRequestRunsAuthoredEvidenceAndRendersDashboard(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	t.Setenv("JIRA_USER", "")
	year := time.Now().Year()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch req.URL.Path {
		case "/rest/api/3/myself":
			response = map[string]any{"accountId": "actor-1", "timeZone": "America/New_York"}
		case "/rest/api/3/search/jql":
			if req.Method != http.MethodPost {
				t.Errorf("search method %s", req.Method)
			}
			response = map[string]any{"isLast": true, "issues": []any{
				map[string]any{"key": "TEST-1", "fields": map[string]any{
					"project": map[string]any{"key": "TEST"}, "summary": "Review <script>alert(1)</script>",
					"assignee": map[string]any{"accountId": "someone-else"},
				}},
			}}
		case "/rest/api/3/issue/TEST-1/comment":
			response = map[string]any{"startAt": 0, "maxResults": 100, "total": 1, "comments": []any{
				map[string]any{"id": "c-1", "author": map[string]any{"accountId": "actor-1"}, "created": fmt.Sprintf("%04d-10-16T23:30:00.000-0400", year)},
			}}
		case "/rest/api/3/issue/TEST-1/worklog":
			response = map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "worklogs": []any{}}
		case "/rest/api/3/issue/TEST-1/changelog":
			response = map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "values": []any{}}
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		if req.URL.Path != "/rest/api/3/search/jql" && req.Method != http.MethodGet {
			t.Errorf("activity attempted a write: %s %s", req.Method, req.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode fixture: %v", err)
		}
	}))
	defer server.Close()
	t.Setenv("JIRA_SERVER", server.URL)
	const original = "what jira items did I work on during Oct 12 - Oct 16"
	result, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
	if err != nil || result == nil || result.IsError {
		t.Fatalf("original request failed: %+v, %v", result, err)
	}
	text := resultText(result)
	for _, expected := range []string{original, "TEST-1", "Comment", "America/New\\_York", fmt.Sprintf("both dates were interpreted in %04d", year), "Matching issues: 1 (complete; exact count)"} {
		if !strings.Contains(text, expected) {
			t.Errorf("activity response missing %q: %s", expected, text)
		}
	}
	stored := h.store.List()
	if len(stored) != 1 {
		t.Fatalf("history rows = %d", len(stored))
	}
	trace := stored[0].Data.(*QueryResultData)
	if trace.Query != original || trace.Activity == nil || len(trace.Activity.Matches) != 1 || trace.Outcome != "success" || !trace.ActivityYearInferred {
		t.Fatalf("incorrect stored activity trace: %+v", trace)
	}
	ws := NewWebServer(h.store, nil, 0, "127.0.0.1")
	var html strings.Builder
	if err := ws.tmpls["query.html"].ExecuteTemplate(&html, "layout", stored[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), "Authored activity evidence") || !strings.Contains(html.String(), "TEST-1") || strings.Contains(html.String(), "<script>alert(1)</script>") {
		t.Fatal("activity dashboard lost evidence or failed to escape issue data")
	}
}
