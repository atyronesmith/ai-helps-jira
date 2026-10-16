package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func summaryIssueJSON(key, summary, assignee string) string {
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":%q,"updated":"2026-10-15T12:00:00.000+0000","project":{"key":"TEST"},"status":{"name":"In Progress"},"statusCategory":{"key":"indeterminate"},"priority":{"name":"Major"},"assignee":{"emailAddress":%q}}}`, key, summary, assignee)
}

func newSummaryServer(t *testing.T, current func() (string, string, bool), calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
			(*calls)++
			assignee, summary, present := current()
			w.Header().Set("Content-Type", "application/json")
			if present {
				_, _ = fmt.Fprintf(w, `{"issues":[%s],"isLast":true,"total":1}`, summaryIssueJSON("TEST-1", summary, assignee))
			} else {
				_, _ = w.Write([]byte(`{"issues":[],"isLast":true,"total":0}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"values":[],"isLast":true,"total":0}`))
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
}

func TestMCPSummaryReconcilesRemovalsAndCacheOnlyDoesNotSearch(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	present := true
	calls := 0
	ts := newSummaryServer(t, func() (string, string, bool) { return "test@test.com", "First", present }, &calls)
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)

	first, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{}))
	if err != nil || first.IsError {
		t.Fatalf("initial summary: result=%v err=%v", first, err)
	}
	if !strings.Contains(resultText(first), "TEST-1") {
		t.Fatalf("initial summary omitted issue: %s", resultText(first))
	}
	present = false // Models closure, reassignment, move, deletion, or visibility loss.
	second, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{}))
	if err != nil || second.IsError {
		t.Fatalf("reconciled summary: result=%v err=%v", second, err)
	}
	if strings.Contains(resultText(second), "TEST-1") {
		t.Fatalf("removed issue survived full reconciliation: %s", resultText(second))
	}
	beforeCacheOnly := calls
	cached, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{"cache_only": true}))
	if err != nil || cached.IsError {
		t.Fatalf("cache-only summary: result=%v err=%v", cached, err)
	}
	if strings.Contains(resultText(cached), "TEST-1") || calls != beforeCacheOnly {
		t.Fatalf("cache-only returned stale issue or searched Jira: calls %d→%d, result=%s", beforeCacheOnly, calls, resultText(cached))
	}
}

func TestMCPSummaryFailedRefreshPreservesPreviousSnapshot(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	failed := false
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
			calls++
			if failed {
				http.Error(w, "search unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"issues":[%s],"isLast":true,"total":1}`, summaryIssueJSON("TEST-1", "Original snapshot", "test@test.com"))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"values":[],"isLast":true,"total":0}`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	if result, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{})); err != nil || result.IsError {
		t.Fatalf("initial summary: result=%v err=%v", result, err)
	}
	cfg, err := config.Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := h.cacheForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, before, err := db.GetSummary(cfg.Assignee(), cfg.JiraProject)
	if err != nil {
		t.Fatal(err)
	}

	failed = true
	refresh, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{"refresh": true}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, refresh)
	cacheOnly, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{"cache_only": true}))
	if err != nil || cacheOnly.IsError {
		t.Fatalf("cache-only after failed refresh: result=%v err=%v", cacheOnly, err)
	}
	if !strings.Contains(resultText(cacheOnly), "TEST-1") || !strings.Contains(resultText(cacheOnly), "Original snapshot") {
		t.Fatalf("failed refresh discarded prior verified snapshot: %s", resultText(cacheOnly))
	}
	_, _, after, err := db.GetSummary(cfg.Assignee(), cfg.JiraProject)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) || calls != 2 {
		t.Fatalf("failed refresh changed checkpoint or cache-only searched: fetched_at %s→%s, search calls=%d", before, after, calls)
	}
}

func TestMCPSummarySeparatesUserAndSiteMembership(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	var callsA, callsB int
	activeUser := "user@example.com"
	serverA := newSummaryServer(t, func() (string, string, bool) { return activeUser, "Site A", true }, &callsA)
	serverB := newSummaryServer(t, func() (string, string, bool) { return activeUser, "Site B", true }, &callsB)
	t.Cleanup(serverA.Close)
	t.Cleanup(serverB.Close)

	call := func(site, user string, cacheOnly bool) *mcp.CallToolResult {
		t.Setenv("JIRA_SERVER", site)
		activeUser = user
		result, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{"user": user, "cache_only": cacheOnly}))
		if err != nil {
			t.Fatalf("summary for %s at %s: %v", user, site, err)
		}
		return result
	}
	if got := resultText(call(serverA.URL, "user@example.com", false)); !strings.Contains(got, "Site A") {
		t.Fatalf("site A summary missing: %s", got)
	}
	if got := resultText(call(serverA.URL, "other@example.com", false)); !strings.Contains(got, "Site A") {
		t.Fatalf("other user summary missing: %s", got)
	}
	if got := resultText(call(serverB.URL, "user@example.com", false)); !strings.Contains(got, "Site B") {
		t.Fatalf("site B summary missing: %s", got)
	}

	beforeA, beforeB := callsA, callsB
	if got := resultText(call(serverA.URL, "other@example.com", true)); !strings.Contains(got, "Site A") {
		t.Fatalf("isolated user cache missing: %s", got)
	}
	if got := resultText(call(serverB.URL, "user@example.com", true)); !strings.Contains(got, "Site B") {
		t.Fatalf("isolated site cache missing: %s", got)
	}
	if callsA != beforeA || callsB != beforeB {
		t.Fatalf("cache-only reads should not search: site A %d→%d, site B %d→%d", beforeA, callsA, beforeB, callsB)
	}
}

func TestMCPWeeklyEmptyAndIncompleteSearchCannotReuseCachedReport(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	searchCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		searchCalls++
		w.Header().Set("Content-Type", "application/json")
		if searchCalls == 1 {
			_, _ = w.Write([]byte(`{"issues":[],"isLast":true,"total":0}`))
		} else {
			_, _ = w.Write([]byte(`{"issues":[],"total":0}`)) // No terminal metadata: completeness is unknown.
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	issues := []jira.Issue{{Key: "TEST-1", Project: "TEST", Summary: "Prior", StatusCategory: "indeterminate", Updated: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}}
	cfg, err := config.Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := h.cacheForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := cache.WeeklyCacheKey(cfg, "2026-10-12", "2026-10-16", issues)
	if err := db.SetWeeklyCache(key, `{"user_name":"Cached User","projects":[{"project_name":"Stale report"}]}`); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"start_date": "2026-10-12", "end_date": "2026-10-16"}
	empty, err := h.HandleWeeklyStatus(context.Background(), makeRequest(args))
	if err != nil || empty.IsError {
		t.Fatalf("empty weekly search: result=%v err=%v", empty, err)
	}
	if !strings.Contains(resultText(empty), "No issues found") || strings.Contains(resultText(empty), "Stale report") {
		t.Fatalf("empty current result reused stale weekly report: %s", resultText(empty))
	}
	incomplete, err := h.HandleWeeklyStatus(context.Background(), makeRequest(args))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, incomplete)
	assertContains(t, resultText(incomplete), "incomplete")
	if searchCalls != 2 {
		t.Fatalf("weekly search calls = %d, want 2", searchCalls)
	}
}

func TestMCPTransitionSucceedsWhenScopedCacheUnavailable(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	var transitioned bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/TEST-1/transitions" {
			transitioned = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "unexpected request", http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	blockedHome := t.TempDir() + "/home-file"
	if err := os.WriteFile(blockedHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blockedHome)
	result, err := h.HandleTransition(context.Background(), makeRequest(map[string]any{"issue_key": "TEST-1", "transition_id": "81"}))
	if err != nil || result.IsError || !transitioned {
		t.Fatalf("transition was affected by unavailable cache: result=%v err=%v transitioned=%t", result, err, transitioned)
	}
}

func TestMCPWeeklyFingerprintSeparatesOverrides(t *testing.T) {
	setTestJIRAEnv(t)
	h := newTestHandlers(t)
	setTestFullEnv(t)
	searchCalls, detailCalls := 0, 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			if r.Method == http.MethodGet {
				detailCalls++
			}
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		searchCalls++
		var request struct {
			JQL string `json:"jql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		user, summary := "first@example.com", "First work"
		if strings.Contains(request.JQL, "second@example.com") {
			user, summary = "second@example.com", "Second work"
		}
		issue := fmt.Sprintf(`{"key":"TEST-1","fields":{"summary":%q,"updated":"2026-10-15T12:00:00.000+0000","project":{"key":"TEST"},"statusCategory":{"key":"indeterminate"},"assignee":{"emailAddress":%q}}}`, summary, user)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"issues":[%s],"isLast":true,"total":1}`, issue)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	for _, user := range []string{"first@example.com", "second@example.com"} {
		cfg, err := config.Load(user, "TEST")
		if err != nil {
			t.Fatal(err)
		}
		issueSummary, reportTitle := "First work", "FIRST REPORT"
		if user == "second@example.com" {
			issueSummary, reportTitle = "Second work", "SECOND REPORT"
		}
		issues := []jira.Issue{{Key: "TEST-1", Project: "TEST", Summary: issueSummary, StatusCategory: "indeterminate", Assignee: user, Updated: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}}
		key := cache.WeeklyCacheKey(cfg, "2026-10-12", "2026-10-16", issues)
		db, err := h.cacheForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		report := fmt.Sprintf(`{"user_name":%q,"projects":[{"project_name":%q,"issue_key":"TEST-1","bullets":["cached report"]}]}`, reportTitle, reportTitle)
		if err := db.SetWeeklyCache(key, report); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ user, report string }{{"first@example.com", "FIRST REPORT"}, {"second@example.com", "SECOND REPORT"}} {
		result, err := h.HandleWeeklyStatus(context.Background(), makeRequest(map[string]any{"user": tc.user, "start_date": "2026-10-12", "end_date": "2026-10-16"}))
		if err != nil || result.IsError {
			t.Fatalf("weekly status for %s: result=%v err=%v", tc.user, result, err)
		}
		if !strings.Contains(resultText(result), tc.report) {
			t.Fatalf("user %s got wrong cached report: %s", tc.user, resultText(result))
		}
	}
	if searchCalls != 2 || detailCalls != 0 {
		t.Fatalf("weekly cache should reuse each exact user fingerprint: searches=%d detail/comment requests=%d", searchCalls, detailCalls)
	}
}

func TestMCPWeeklyDetailAndCommentFailuresDoNotCachePartialReport(t *testing.T) {
	for _, failAt := range []string{"detail", "comments"} {
		t.Run(failAt, func(t *testing.T) {
			h := newTestHandlers(t)
			setTestFullEnv(t)
			issueUpdated := "2026-10-15T12:00:00.000+0000"
			var searchIssue = fmt.Sprintf(`{"key":"TEST-1","fields":{"summary":"Issue","updated":%q,"project":{"key":"TEST"},"statusCategory":{"key":"indeterminate"},"assignee":{"emailAddress":"test@test.com"}}}`, issueUpdated)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
					_, _ = fmt.Fprintf(w, `{"issues":[%s],"isLast":true,"total":1}`, searchIssue)
				case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/TEST-1" && strings.Contains(r.URL.RawQuery, "fields=comment"):
					if failAt == "comments" {
						http.Error(w, "comments unavailable", http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"fields":{"comment":{"comments":[]}}}`))
				case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/TEST-1":
					if failAt == "detail" {
						http.Error(w, "detail unavailable", http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"key":"TEST-1","fields":{"summary":"Issue","updated":"2026-10-15T12:00:00.000+0000","project":{"key":"TEST"},"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},"statusCategory":{"key":"indeterminate"},"assignee":{"emailAddress":"test@test.com"},"issuetype":{"name":"Task"}}}`))
				default:
					http.Error(w, "unexpected request", http.StatusNotFound)
				}
			}))
			t.Cleanup(ts.Close)
			t.Setenv("JIRA_SERVER", ts.URL)
			cfg, err := config.Load("", "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.HandleWeeklyStatus(context.Background(), makeRequest(map[string]any{"start_date": "2026-10-12", "end_date": "2026-10-16"}))
			if err != nil {
				t.Fatal(err)
			}
			assertIsError(t, result)
			assertContains(t, resultText(result), failAt)
			db, err := h.cacheForConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if stats := db.Stats(); stats.WeeklyCache != 0 {
				t.Fatalf("partial weekly generation was cached: stats=%+v", stats)
			}
		})
	}
}
