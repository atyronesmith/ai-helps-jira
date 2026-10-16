package mcpserver

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// TestNewWebServerParsesTemplates guards that every view template -- including
// query.html's route badge/tooltip -- parses against the template funcMap, so a
// template or funcMap drift (e.g. an unregistered routeTooltip call) cannot panic
// the dashboard at startup.
func TestNewWebServerParsesTemplates(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	if ws.tmpls["query.html"] == nil {
		t.Fatal("query.html template not parsed")
	}
	if len(ws.tmpls) == 0 {
		t.Fatal("no templates parsed")
	}
}

func TestQueryViewRendersAuthoritativeTotal(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultQuery, "Query: open bugs", &QueryResultData{
		Query:  "open bugs",
		Total:  73,
		Issues: []jira.Issue{{Key: "TEST-1", Summary: "First issue", Status: "Open"}, {Key: "TEST-2", Summary: "Second issue", Status: "Open"}},
	})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	var buf strings.Builder
	if err := ws.tmpls["query.html"].ExecuteTemplate(&buf, "layout", store.results[id]); err != nil {
		t.Fatalf("query view render: %v", err)
	}
	if !strings.Contains(buf.String(), "Issues (73 matching, 2 shown)") {
		t.Errorf("query view did not render authoritative total and displayed count: %s", buf.String())
	}
}

func TestStatusEndpoint(t *testing.T) {
	ws := NewWebServer(&ResultStore{results: make(map[string]*StoredResult)}, nil, 0, "127.0.0.1")
	ws.startedAt = time.Now().Add(-37 * time.Second)
	response := httptest.NewRecorder()
	ws.handleStatus(response, httptest.NewRequest("GET", "/api/status", nil))

	if response.Code != 200 {
		t.Fatalf("status code = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	var status serverStatusView
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.Status != "ok" || status.UptimeSeconds < 37 || status.Uptime == "" {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestHealthzRemainsAvailable(t *testing.T) {
	ws := NewWebServer(&ResultStore{results: make(map[string]*StoredResult)}, nil, 0, "127.0.0.1")
	response := httptest.NewRecorder()
	ws.handleHealthz(response, httptest.NewRequest("GET", "/healthz", nil))
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("healthz response = %d %q", response.Code, response.Body.String())
	}
}

// TestIndexRendersQueryJQL verifies the results index surfaces the natural
// language query (in the row title) and the generated JQL for query results,
// so a viewer can see both without opening the detail view.
func TestIndexRendersQueryJQL(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "Legacy generic title", &QueryResultData{
		Query: "my open bugs",
		Route: "fast_path",
		JQL:   "project = TEST AND status = Open",
	})

	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	if ws.tmpls["index.html"] == nil {
		t.Fatal("index.html template not parsed")
	}

	var buf strings.Builder
	data := struct {
		Results []*StoredResult
	}{Results: store.List()}
	if err := ws.tmpls["index.html"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("index render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "<td>Query: my open bugs</td>") {
		t.Errorf("results index title does not show the natural-language query: %s", html)
	}
	if strings.Contains(html, "<td>Legacy generic title</td>") {
		t.Errorf("results index used generic stored title instead of the natural-language query: %s", html)
	}
	for _, want := range []string{"my open bugs", "project = TEST AND status = Open", "fast_path", "cached Jira issues", "last 10 eligible issue-detail lookups", "Server", "id=\"server-health\"", "id=\"server-uptime\"", "fetch('/api/status'", "setInterval(refreshStatus, 15000)", "Unreachable"} {
		if !strings.Contains(html, want) {
			t.Errorf("index HTML missing %q", want)
		}
	}
}

func TestResultTitleFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		result *StoredResult
		want   string
	}{
		{
			name:   "query with no query data",
			result: &StoredResult{Type: ResultQuery, Title: "Legacy query title"},
			want:   "Legacy query title",
		},
		{
			name:   "query with empty natural language data",
			result: &StoredResult{Type: ResultQuery, Title: "Legacy query title", Data: &QueryResultData{}},
			want:   "Legacy query title",
		},
		{
			name:   "non-query row",
			result: &StoredResult{Type: ResultSummary, Title: "Summary title", Data: &QueryResultData{Query: "unexpected"}},
			want:   "Summary title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resultTitle(tt.result); got != tt.want {
				t.Errorf("resultTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIndexEscapesQueryTitle(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "generic", &QueryResultData{Query: `<script>alert("x")</script>`})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	var buf strings.Builder
	data := struct{ Results []*StoredResult }{Results: store.List()}
	if err := ws.tmpls["index.html"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("index render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "<td>Query: &lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;</td>") {
		t.Errorf("query title was not rendered as escaped text: %s", html)
	}
	if strings.Contains(html, "<td>Query: <script>") {
		t.Errorf("query title contains unescaped HTML: %s", html)
	}
}

func TestIndexRendersCacheStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db, err := cache.Open()
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	updated := time.Now().UTC().Truncate(time.Second)
	if err := db.UpsertIssues("PROJ", []jira.Issue{
		{Key: "PROJ-1", Summary: "one", Updated: updated},
		{Key: "PROJ-2", Summary: "two", Updated: updated},
	}); err != nil {
		t.Fatalf("upsert issues: %v", err)
	}
	if err := db.UpsertIssueDetail(&jira.IssueDetail{Key: "PROJ-1", Summary: "one", Updated: updated}); err != nil {
		t.Fatalf("upsert shared detail: %v", err)
	}
	if err := db.UpsertIssueDetail(&jira.IssueDetail{Key: "PROJ-3", Summary: "detail only", Updated: updated}); err != nil {
		t.Fatalf("upsert detail-only issue: %v", err)
	}
	for _, hit := range []bool{true, true, false} {
		db.RecordIssueDetailLookup(hit)
	}

	store := &ResultStore{results: make(map[string]*StoredResult)}
	ws := NewWebServer(store, db, 0, "127.0.0.1")
	var buf strings.Builder
	data := struct{ Results []*StoredResult }{Results: store.List()}
	if err := ws.tmpls["index.html"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("index render: %v", err)
	}

	html := buf.String()
	for _, want := range []string{"3</strong> cached Jira issues", "66.7%", "2/3 hits"} {
		if !strings.Contains(html, want) {
			t.Errorf("index HTML missing %q: %s", want, html)
		}
	}
}
