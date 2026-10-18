package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

// A 400 from JIRA's search endpoint (a malformed query) must surface as an
// error from runWeeklyStatus rather than being treated as "no issues found",
// and Jira's own diagnostic text must survive into it along with the
// offending JQL (FIELDCTO-2814). This pins the CLI path; the MCP path is
// covered in internal/mcpserver.
func TestRunWeeklyStatus_SearchErrorSurfacesJiraMessageAndJQL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errorMessages": []string{"Error in the JQL Query: Expecting ',' but got 'AND'. (line 1, character 39)"},
		})
	}))
	t.Cleanup(ts.Close)

	t.Setenv("JIRA_SERVER", ts.URL)
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	// Reset package-level flags that other tests/commands might have mutated.
	flagUser = ""
	flagProject = ""
	flagWSStartDate = "2026-01-01"
	flagWSEndDate = "2026-01-07"
	flagWSConfluence = false

	err := runWeeklyStatus(weeklyTestCommand(), nil)
	if err == nil {
		t.Fatal("expected an error for a 400 search response, got nil")
	}
	if !strings.Contains(err.Error(), "Expecting ',' but got 'AND'") {
		t.Errorf("Jira's errorMessages text did not survive into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "project = TEST") {
		t.Errorf("the offending JQL did not survive into the error: %v", err)
	}
}

type weeklyCLIHarness struct {
	mu          sync.Mutex
	search      func(request int) (rows []map[string]any, complete bool)
	searches    atomic.Int32
	completions atomic.Int32
	failDetail  atomic.Bool
	failComment atomic.Bool
	llmOutput   string
	server      *httptest.Server
}

func newWeeklyCLIHarness(t *testing.T) *weeklyCLIHarness {
	t.Helper()
	h := &weeklyCLIHarness{}
	h.search = func(int) ([]map[string]any, bool) { return nil, true }
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/api/3/search/jql":
			request := int(h.searches.Add(1))
			h.mu.Lock()
			rows, complete := h.search(request)
			h.mu.Unlock()
			response := map[string]any{"issues": rows}
			if complete {
				response["isLast"] = true
				response["total"] = len(rows)
			}
			_ = json.NewEncoder(w).Encode(response)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/") && r.URL.Query().Get("fields") == "comment":
			if h.failComment.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errorMessages":["comments unavailable"]}`))
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": map[string]any{"comment": map[string]any{"comments": []any{map[string]any{
				"id": "comment-" + key, "created": "2026-01-04T10:00:00.000+0000",
				"author": map[string]any{"displayName": "Test User"},
				"body":   map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "completed work"}}}}},
			}}}}})
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			if h.failDetail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errorMessages":["details unavailable"]}`))
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": map[string]any{
				"summary": "Details for " + key, "status": map[string]any{"name": "In Progress"},
				"statusCategory": map[string]any{"key": "indeterminate"}, "project": map[string]any{"key": "TEST"},
				"updated": "2026-01-03T12:00:00.000+0000", "issuetype": map[string]any{"name": "Task"}, "labels": []string{},
			}})
		case r.URL.Path == "/v1/chat/completions":
			h.completions.Add(1)
			var request struct {
				Messages []struct{ Role, Content string }
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			var payload struct {
				Items []struct {
					Key       string `json:"key"`
					ParentKey string `json:"parent_key"`
				} `json:"items"`
			}
			if len(request.Messages) > 1 {
				_ = json.Unmarshal([]byte(request.Messages[1].Content), &payload)
			}
			output := h.llmOutput
			if output == "" {
				projects := make([]map[string]any, 0, len(payload.Items))
				seen := make(map[string]bool)
				for _, item := range payload.Items {
					key := item.Key
					if item.ParentKey != "" {
						key = item.ParentKey
					}
					if seen[key] {
						continue
					}
					seen[key] = true
					projects = append(projects, map[string]any{"project_name": "Test", "issue_key": key, "bullets": []string{"Updated work"}, "commentary": "Progress made."})
				}
				body, _ := json.Marshal(map[string]any{"user_name": "Test User", "projects": projects})
				output = string(body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": output}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.server.Close)
	return h
}

func (h *weeklyCLIHarness) setSearch(fn func(request int) ([]map[string]any, bool)) {
	h.mu.Lock()
	h.search = fn
	h.mu.Unlock()
}

func (h *weeklyCLIHarness) setRows(keys ...string) {
	h.setSearch(func(int) ([]map[string]any, bool) {
		rows := make([]map[string]any, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, map[string]any{"key": key, "fields": map[string]any{
				"summary": "Search summary " + key, "status": map[string]any{"name": "In Progress"},
				"statusCategory": map[string]any{"key": "indeterminate"}, "project": map[string]any{"key": "TEST"},
				"updated": "2026-01-03T12:00:00.000+0000", "assignee": map[string]any{"emailAddress": "test@example.com", "accountId": "test-account"},
			}})
		}
		return rows, true
	})
}

func setupWeeklyCLI(t *testing.T, h *weeklyCLIHarness, user string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JIRA_SERVER", h.server.URL)
	t.Setenv("JIRA_EMAIL", "test@example.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("LLM_BASE_URL", h.server.URL)
	t.Setenv("LLM_MODEL", "test-model")
	t.Setenv("LLM_API_KEY", "fake-key")
	oldUser, oldProject := flagUser, flagProject
	oldStart, oldEnd := flagWSStartDate, flagWSEndDate
	oldConfluence, oldParent := flagWSConfluence, flagWSConfluenceParent
	oldFormat, oldVerbose := flagFormat, flagVerbose
	flagUser = user
	flagProject = ""
	flagWSStartDate = "2026-01-01"
	flagWSEndDate = "2026-01-07"
	flagWSConfluence = false
	flagWSConfluenceParent = ""
	flagFormat = "text"
	flagVerbose = 0
	t.Cleanup(func() {
		flagUser, flagProject = oldUser, oldProject
		flagWSStartDate, flagWSEndDate = oldStart, oldEnd
		flagWSConfluence, flagWSConfluenceParent = oldConfluence, oldParent
		flagFormat, flagVerbose = oldFormat, oldVerbose
	})
}

func weeklyTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func runWeeklyCLI(t *testing.T) error {
	t.Helper()
	return runWeeklyStatus(weeklyTestCommand(), nil)
}

func TestRunWeeklyStatusFingerprintInvalidatesRemovalAndSameTimestampReplacement(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []string
	}{
		{name: "issue removed", before: []string{"TEST-1", "TEST-2"}, after: []string{"TEST-1"}},
		{name: "membership replaced at same timestamp", before: []string{"TEST-1"}, after: []string{"TEST-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWeeklyCLIHarness(t)
			setupWeeklyCLI(t, h, "")
			h.setRows(tc.before...)
			if err := runWeeklyCLI(t); err != nil {
				t.Fatalf("first run: %v", err)
			}
			if got := h.completions.Load(); got != 1 {
				t.Fatalf("first run completions = %d, want 1", got)
			}
			h.setRows(tc.after...)
			if err := runWeeklyCLI(t); err != nil {
				t.Fatalf("second run: %v", err)
			}
			if got := h.completions.Load(); got != 2 {
				t.Fatalf("completion calls = %d, want regeneration after changed issue set", got)
			}
		})
	}
}

func TestRunWeeklyStatusDifferentOverridesDoNotShareReport(t *testing.T) {
	h := newWeeklyCLIHarness(t)
	setupWeeklyCLI(t, h, "alice@example.test")
	h.setRows("TEST-1")
	if err := runWeeklyCLI(t); err != nil {
		t.Fatal(err)
	}
	flagUser = "bob@example.test"
	if err := runWeeklyCLI(t); err != nil {
		t.Fatal(err)
	}
	if got := h.completions.Load(); got != 2 {
		t.Fatalf("completion calls = %d, want separate reports for each override", got)
	}
}

func TestRunWeeklyStatusIncompleteSearchCannotReuseCachedReport(t *testing.T) {
	h := newWeeklyCLIHarness(t)
	setupWeeklyCLI(t, h, "")
	h.setRows("TEST-1")
	if err := runWeeklyCLI(t); err != nil {
		t.Fatal(err)
	}
	h.setSearch(func(int) ([]map[string]any, bool) {
		return []map[string]any{{"key": "TEST-1", "fields": map[string]any{"summary": "Search summary TEST-1", "updated": "2026-01-03T12:00:00.000+0000"}}}, false
	})
	err := runWeeklyCLI(t)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete-search error, got %v", err)
	}
	if got := h.completions.Load(); got != 1 {
		t.Fatalf("incomplete search triggered report generation/reuse; completions=%d", got)
	}
}

func TestRunWeeklyStatusDetailOrCommentFailureDoesNotCacheReport(t *testing.T) {
	for _, failAt := range []string{"detail", "comment"} {
		t.Run(failAt, func(t *testing.T) {
			h := newWeeklyCLIHarness(t)
			setupWeeklyCLI(t, h, "")
			h.setRows("TEST-1")
			if failAt == "detail" {
				h.failDetail.Store(true)
			} else {
				h.failComment.Store(true)
			}
			err := runWeeklyCLI(t)
			if err == nil {
				t.Fatal("expected Jira fetch failure")
			}
			if got := h.completions.Load(); got != 0 {
				t.Fatalf("LLM called %d times after fetch failure", got)
			}
			cfg := &config.Config{JiraServer: h.server.URL, JiraEmail: "test@example.com", JiraAPIToken: "fake-token", JiraProject: "TEST"}
			db, openErr := cache.OpenForConfig(cfg)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer db.Close()
			if got := db.Stats().WeeklyCache; got != 0 {
				t.Fatalf("weekly cache contains %d reports after %s failure", got, failAt)
			}
		})
	}
}

func TestRunWeeklyStatusInvalidGenerationDoesNotCacheOrExposeOutput(t *testing.T) {
	h := newWeeklyCLIHarness(t)
	setupWeeklyCLI(t, h, "")
	h.setRows("TEST-2")
	h.llmOutput = `SYNTHETIC-PRIVATE-MARKER`
	err := runWeeklyCLI(t)
	if err == nil {
		t.Fatal("expected invalid generated report to fail")
	}
	if strings.Contains(err.Error(), "SYNTHETIC-PRIVATE-MARKER") {
		t.Fatalf("provider output leaked in error: %v", err)
	}
	cfg := &config.Config{JiraServer: h.server.URL, JiraEmail: "test@example.com", JiraAPIToken: "fake-token", JiraProject: "TEST"}
	db, openErr := cache.OpenForConfig(cfg)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	if got := db.Stats().WeeklyCache; got != 0 {
		t.Fatalf("weekly cache contains %d reports after invalid generation", got)
	}
}
