package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

func TestQueryReviewedBatchSearchExecutesAndStoresExactJQL(t *testing.T) {
	queries := []struct{ name, input, normalized, where, sort string }{
		{"summary text alias", "list my open items order by the summary text", "list my open items order by the summary text", `statusCategory != Done AND assignee = "reviewer@example.com"`, "summary ASC"},
		{"title alias", "list my open items ordered by title", "list my open items ordered by title", `statusCategory != Done AND assignee = "reviewer@example.com"`, "summary ASC"},
		{"text alternatives", "find my open work items that contain the words atlas, gpu or namespace and order by priority", "find my open work items that contain the words atlas, gpu or namespace and order by priority", `statusCategory != Done AND assignee = "reviewer@example.com" AND text ~ "atlas OR gpu OR namespace"`, "priority DESC"},
		{"epics", "find all my open work epics", "find all my open work epics", `issuetype = Epic AND statusCategory != Done AND assignee = "reviewer@example.com"`, "updated DESC"},
		{"children of epics", "find all of my open work items that are child tasks of an epic", "find all of my open work items that are child tasks of an epic", `statusCategory != Done AND assignee = "reviewer@example.com" AND parent IS NOT EMPTY AND issuetype NOT IN subtaskIssueTypes()`, "updated DESC"},
	}
	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandlers(t)
			setTestJIRAEnv(t)
			want := tc.where + ` AND project = TEST ORDER BY ` + tc.sort
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					JQL string `json:"jql"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.JQL != want {
					t.Errorf("executed=%q error=%v; want=%q", body.JQL, err, want)
				}
				_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
			}))
			defer srv.Close()
			t.Setenv("JIRA_SERVER", srv.URL)
			provider := &missingOwnerSortProvider{field: "created"}
			fixture := llm.TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"TEST": "TEST"}}
			h.translate = func(ctx context.Context, cfg *config.Config, query string) (*llm.QueryResult, error) {
				if query != tc.input {
					t.Errorf("original wording changed: %q", query)
				}
				return llm.GenerateJQLWithFixture(ctx, cfg, query, fixture, provider)
			}
			result, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": tc.input, "user": "reviewer@example.com"}))
			if err != nil || result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if requests.Load() != 1 || provider.calls.Load() != 0 {
				t.Fatalf("searches=%d provider=%d", requests.Load(), provider.calls.Load())
			}
			records := h.store.List()
			if len(records) != 1 {
				t.Fatalf("history=%d", len(records))
			}
			trace := records[0].Data.(*QueryResultData)
			if trace.Query != tc.input || trace.NormalizedInput != tc.normalized || trace.ExecutedJQL != want || trace.GeneratedJQL != want || trace.Route != llm.RouteFastPath || trace.Outcome != "success" {
				t.Fatalf("incorrect query diagnostics: %+v", trace)
			}
		})
	}
}
