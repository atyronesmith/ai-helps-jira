package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func renderDashboardTemplate(t *testing.T, ws *WebServer, name string, data any) string {
	t.Helper()
	var out strings.Builder
	if err := ws.tmpls[name].ExecuteTemplate(&out, "layout", data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return out.String()
}

func TestPhase6FailedQueryViewSeparatesExecutedAndDisplayJQL(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultQuery, "failed search", &QueryResultData{
		Query:           `<script>alert("query")</script>`,
		NormalizedInput: "normalized wording",
		GeneratedJQL:    `project = "<bad>"`,
		DisplayJQL:      `project = "display only"`,
		Outcome:         "failure",
		ErrorStage:      "validation",
		ErrorCategory:   "invalid_output",
		Validation:      "local_invalid",
		SearchError:     `<img src=x onerror="alert(1)">`,
	})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	html := renderDashboardTemplate(t, ws, "query.html", store.results[id])
	for _, want := range []string{
		"normalized wording", "Outcome:", "failure", "validation", "invalid_output",
		"Exact executed JQL:</strong> not recorded (no execution established)",
		"Generated JQL (before execution)", "Display JQL (display only)",
		"Search error:",
		"&lt;script&gt;alert(&#34;query&#34;)&lt;/script&gt;",
		"&lt;img src=x onerror=&#34;alert(1)&#34;&gt;",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("query view missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert") || strings.Contains(html, "<img src=x") {
		t.Fatalf("untrusted query diagnostics rendered as markup: %s", html)
	}
	if strings.Contains(html, "Exact executed JQL</summary>") {
		t.Fatalf("display JQL was mislabeled as executed: %s", html)
	}
}

func TestPhase6LegacyJQLIsNeverLabeledExecuted(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultQuery, "legacy", &QueryResultData{Query: "old query", JQL: "project = TEST"})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	html := renderDashboardTemplate(t, ws, "query.html", store.results[id])
	if !strings.Contains(html, "Legacy JQL (display only; execution not established)") || strings.Contains(html, "Exact executed JQL</summary>") {
		t.Fatalf("legacy JQL execution labeling is misleading: %s", html)
	}
}

func TestPhase6IndexShowsFailureOutcome(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "failed query", &QueryResultData{
		Query: "broken request", Outcome: "failure", ErrorStage: "translation", ErrorCategory: "unsupported",
	})
	store.Save(ResultAttempt, "failed tool", &AttemptResultData{
		Tool: "jira_digest", Outcome: "failure", ErrorCategory: "connectivity",
	})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	html := renderDashboardTemplate(t, ws, "index.html", struct{ Results []*StoredResult }{store.List()})
	for _, want := range []string{"Outcome: failure", "stage: translation", "category: unsupported", "category: connectivity"} {
		if !strings.Contains(html, want) {
			t.Errorf("index missing failure metadata %q", want)
		}
	}
}

func TestPhase6AttemptViewRendersBoundedFailureMetadata(t *testing.T) {
	started := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultAttempt, "failure", &AttemptResultData{
		Tool: "jira_digest", Outcome: "failure", ErrorCategory: "connectivity",
		StartedAt: started, CompletedAt: started.Add(250 * time.Millisecond), Duration: 250 * time.Millisecond,
	})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	html := renderDashboardTemplate(t, ws, "attempt.html", store.results[id])
	for _, want := range []string{"Tool attempt", "jira_digest", "failure", "connectivity", "2026-10-16 12:00", "250ms"} {
		if !strings.Contains(html, want) {
			t.Errorf("attempt view missing %q", want)
		}
	}
}

func TestPhase6AttemptViewRendersQueryTraceAndCompleteness(t *testing.T) {
	total := 12
	more := true
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultAttempt, "translation", &AttemptResultData{
		Tool: "jira_digest", Outcome: "failure", ErrorCategory: "upstream_http",
		Trace: &QueryResultData{
			Query: "my open bugs", NormalizedInput: "my open bugs", Route: "fast_path",
			ExecutedJQL:  `assignee = currentUser() AND statusCategory != Done`,
			GeneratedJQL: `assignee = currentUser() AND statusCategory != Done`,
			DisplayJQL:   `assignee = currentUser() AND statusCategory != Done ORDER BY created DESC`,
			Validation:   "local_valid", ErrorStage: "search",
			Search: &jira.SearchInfo{Returned: 2, Total: &total, TotalKind: jira.CountReported, HasMore: &more},
		},
	})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	html := renderDashboardTemplate(t, ws, "attempt.html", store.results[id])
	for _, want := range []string{
		"Query trace", "Original input:", "my open bugs", "Normalized input:", "fast_path",
		"Stage:", "search", "Validation:", "local_valid",
		"Exact executed JQL</summary>", "assignee = currentUser() AND statusCategory != Done",
		"Generated JQL (before execution)", "Display JQL (display only)",
		"Returned rows: 2", "Jira-reported matching total (unverified): 12",
		"Completeness: partial; more matches known",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("attempt trace missing %q", want)
		}
	}
	if !strings.Contains(html, "ORDER BY created DESC") {
		t.Fatalf("display-only JQL missing from trace: %s", html)
	}
}

func TestPhase6LayoutShowsReadinessAndOnlyPollsStatusAPI(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	ws.dependencies = newDependencyObservations()
	checked := time.Now().UTC().Truncate(time.Second)
	ws.dependencies.observe("jira", nil)
	html := renderDashboardTemplate(t, ws, "index.html", struct{ Results []*StoredResult }{})
	for _, want := range []string{
		"Alive", "Readiness", "unknown", "Dependencies", "mcp", "never observed", "jira", "checked",
		"fetch('/api/status'", "setInterval(refreshStatus, 15000)", "textContent",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("layout missing %q", want)
		}
	}
	if strings.Contains(html, "fetch('/api/jira") || strings.Contains(html, "fetch('/api/provider") || strings.Contains(html, "fetch('/api/cache") {
		t.Fatalf("layout actively probes a dependency: %s", html)
	}
	if !strings.Contains(html, checked.Format("2006-01-02 15:04")) {
		t.Errorf("observed dependency timestamp missing from rendered layout: %s", html)
	}
}
