package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

const weeklyValidOutput = `{"user_name":"Test User","projects":[{"project_name":"Test issue","issue_key":"TEST-1","bullets":["The current issue status is In Progress; no comment evidence is available."]}]}`

type weeklyFixture struct {
	jira        *httptest.Server
	provider    *httptest.Server
	searches    atomic.Int32
	details     atomic.Int32
	comments    atomic.Int32
	completions atomic.Int32
	confluence  atomic.Int32
}

func newWeeklyFixture(t *testing.T, completion func() (string, string), providerHandler ...http.HandlerFunc) *weeklyFixture {
	t.Helper()
	f := &weeklyFixture{}
	f.jira = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
			f.searches.Add(1)
			_, _ = w.Write([]byte(`{"issues":[{"key":"TEST-1","fields":{"summary":"Test issue","updated":"2026-10-15T12:00:00.000+0000","project":{"key":"TEST"},"statusCategory":{"key":"indeterminate"},"assignee":{"emailAddress":"test@test.com"}}}],"isLast":true,"total":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/TEST-1" && strings.Contains(r.URL.RawQuery, "fields=comment"):
			f.comments.Add(1)
			_, _ = w.Write([]byte(`{"fields":{"comment":{"comments":[]}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/TEST-1":
			f.details.Add(1)
			_, _ = w.Write([]byte(`{"key":"TEST-1","fields":{"summary":"Test issue","updated":"2026-10-15T12:00:00.000+0000","project":{"key":"TEST"},"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},"statusCategory":{"key":"indeterminate"},"assignee":{"emailAddress":"test@test.com"},"issuetype":{"name":"Task"}}}`))
		default:
			f.confluence.Add(1)
			http.Error(w, "unexpected Jira/Confluence request", http.StatusNotFound)
		}
	}))
	var provider http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.completions.Add(1)
		body, finish := completion()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": body}}}})
	})
	if len(providerHandler) > 0 {
		provider = providerHandler[0]
	}
	f.provider = httptest.NewServer(provider)
	t.Cleanup(f.jira.Close)
	t.Cleanup(f.provider.Close)
	return f
}

func configureWeeklyFixture(t *testing.T, f *weeklyFixture) (*Handlers, *config.Config) {
	t.Helper()
	h := newTestHandlers(t)
	setTestFullEnv(t)
	t.Setenv("JIRA_SERVER", f.jira.URL)
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("LLM_BASE_URL", f.provider.URL)
	t.Setenv("LLM_MODEL", "test-model")
	t.Setenv("LLM_API_KEY", "")
	cfg, err := config.Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func TestWeeklyGenerationRejectsInvalidOutputWithoutLeakingOrPersisting(t *testing.T) {
	repeated := strings.Repeat("A repeated fragment keeps appearing in this bullet. ", 4)
	cases := []struct{ name, body, finish string }{
		{"malformed", `{"user_name":"PRIVATE_WEEKLY_RESPONSE_MARKER"`, "stop"},
		{"repetitive", fmt.Sprintf(`{"user_name":"Test User","projects":[{"project_name":"Test Project","issue_key":"TEST-1","bullets":[%q]}]}`, repeated), "stop"},
		{"unknown_issue", `{"user_name":"Test User","projects":[{"project_name":"Test Project","issue_key":"TEST-999","bullets":["A safe bullet."]}]}`, "stop"},
		{"missing_group", `{"user_name":"Test User","projects":[]}`, "stop"},
		{"truncated", weeklyValidOutput, "length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWeeklyFixture(t, func() (string, string) { return tc.body, tc.finish })
			h, cfg := configureWeeklyFixture(t, f)
			private := "PRIVATE_WEEKLY_RESPONSE_MARKER"
			ws := NewWebServer(h.store, h.cache, 0, "127.0.0.1")
			h.dependencies = ws.dependencies
			res, err := h.withDiagnostics("jira_weekly_status", h.HandleWeeklyStatus)(context.Background(), makeRequest(map[string]any{"start_date": "2026-10-12", "end_date": "2026-10-16", "confluence": true, "confluence_parent_id": "123"}))
			if err != nil || res == nil || !res.IsError {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if strings.Contains(resultText(res), private) {
				t.Fatalf("raw provider content leaked: %s", resultText(res))
			}
			db, err := h.cacheForConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := db.Stats().WeeklyCache; got != 0 {
				t.Fatalf("weekly cache rows=%d", got)
			}
			for _, saved := range h.store.List() {
				if saved.Type == ResultWeeklyStatus {
					t.Fatal("invalid generation persisted as a weekly report")
				}
			}
			var attempts []*AttemptResultData
			for _, saved := range h.store.List() {
				if a, ok := saved.Data.(*AttemptResultData); ok && a.Tool == "jira_weekly_status" {
					attempts = append(attempts, a)
				}
			}
			if len(attempts) != 1 || attempts[0].Outcome != "failure" || attempts[0].ErrorCategory != "invalid_output" {
				t.Fatalf("weekly attempts=%+v", attempts)
			}
			if attempts[0].StartedAt.IsZero() || attempts[0].CompletedAt.Before(attempts[0].StartedAt) || attempts[0].Duration <= 0 {
				t.Fatalf("invalid attempt lifecycle: %+v", attempts[0])
			}
			if f.confluence.Load() != 0 {
				t.Fatalf("Confluence calls=%d", f.confluence.Load())
			}
			// The diagnostics wrapper records provider failure before returning.
			var providerObs *DependencyObservation
			for i := range ws.status().Dependencies {
				obs := ws.status().Dependencies[i]
				if obs.Name == "provider" {
					providerObs = &obs
				}
			}
			if providerObs == nil || providerObs.State != "failed" || providerObs.CheckedAt.IsZero() {
				t.Fatalf("provider observation=%+v", providerObs)
			}
			before := ws.status()
			for _, path := range []string{"/api/status", "/readyz", "/healthz", "/"} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				w := httptest.NewRecorder()
				switch path {
				case "/api/status":
					ws.handleStatus(w, r)
				case "/readyz":
					ws.handleReadyz(w, r)
				case "/healthz":
					ws.handleHealthz(w, r)
				default:
					ws.handleIndex(w, r)
				}
			}
			after := ws.status()
			if f.completions.Load() != 1 || f.searches.Load() != 1 {
				t.Fatalf("passive polling caused traffic: completions=%d searches=%d", f.completions.Load(), f.searches.Load())
			}
			for i, obs := range before.Dependencies {
				if !after.Dependencies[i].CheckedAt.Equal(obs.CheckedAt) {
					t.Fatalf("poll refreshed dependency %s", obs.Name)
				}
			}
		})
	}
}

func TestWeeklyGenerationSuccessCachesAndReusesReport(t *testing.T) {
	f := newWeeklyFixture(t, func() (string, string) { return weeklyValidOutput, "stop" })
	h, _ := configureWeeklyFixture(t, f)
	call := h.withDiagnostics("jira_weekly_status", h.HandleWeeklyStatus)
	args := makeRequest(map[string]any{"start_date": "2026-10-12", "end_date": "2026-10-16"})
	for i := 0; i < 2; i++ {
		res, err := call(context.Background(), args)
		if err != nil || res == nil || res.IsError {
			t.Fatalf("result=%+v err=%v", res, err)
		}
	}
	if f.searches.Load() != 2 || f.completions.Load() != 1 {
		t.Fatalf("searches=%d completions=%d", f.searches.Load(), f.completions.Load())
	}
	weekly, attempts := 0, 0
	for _, saved := range h.store.List() {
		if saved.Type == ResultWeeklyStatus {
			weekly++
		}
		if a, ok := saved.Data.(*AttemptResultData); ok && a.Tool == "jira_weekly_status" && a.Outcome == "success" {
			attempts++
		}
	}
	if weekly != 2 || attempts != 2 {
		t.Fatalf("weekly reports=%d attempts=%d", weekly, attempts)
	}
}

func TestWeeklyGenerationInvalidCachedReportRegenerates(t *testing.T) {
	f := newWeeklyFixture(t, func() (string, string) { return weeklyValidOutput, "stop" })
	h, cfg := configureWeeklyFixture(t, f)
	db, err := h.cacheForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	issues := []jira.Issue{{Key: "TEST-1", Summary: "Test issue", Project: "TEST", StatusCategory: "indeterminate", Assignee: "test@test.com", Updated: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}}
	key := cache.WeeklyCacheKey(cfg, "2026-10-12", "2026-10-16", issues)
	if err := db.SetWeeklyCache(key, `null`); err != nil {
		t.Fatal(err)
	}
	res, err := h.HandleWeeklyStatus(context.Background(), makeRequest(map[string]any{"start_date": "2026-10-12", "end_date": "2026-10-16"}))
	if err != nil || res == nil || res.IsError || f.completions.Load() != 1 {
		t.Fatalf("result=%+v err=%v completions=%d", res, err, f.completions.Load())
	}
	stored, _, ok := db.GetWeeklyCache(key)
	if !ok || !strings.Contains(stored, "no comment evidence") {
		t.Fatal("invalid cache entry was not replaced by the valid generated report")
	}
}

func TestWeeklyGenerationCallerCancellationReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var completionCalls atomic.Int32
	f := newWeeklyFixture(t, func() (string, string) { return weeklyValidOutput, "stop" }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		completionCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release) })
	h, _ := configureWeeklyFixture(t, f)
	ws := NewWebServer(h.store, h.cache, 0, "127.0.0.1")
	h.dependencies = ws.dependencies
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	resultCh := make(chan bool, 1)
	go func() {
		defer close(done)
		res, err := h.withDiagnostics("jira_weekly_status", h.HandleWeeklyStatus)(ctx, makeRequest(map[string]any{"start_date": "2026-10-12", "end_date": "2026-10-16"}))
		resultCh <- err == nil && res != nil && res.IsError
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("weekly request ignored caller cancellation")
	}
	if !<-resultCh {
		t.Fatal("canceled weekly call did not return an MCP error")
	}
	if completionCalls.Load() != 1 {
		t.Fatalf("provider retries=%d", completionCalls.Load())
	}
	if f.confluence.Load() != 0 {
		t.Fatalf("canceled weekly generation made Confluence calls=%d", f.confluence.Load())
	}
	var attempt *AttemptResultData
	for _, saved := range h.store.List() {
		if a, ok := saved.Data.(*AttemptResultData); ok && a.Tool == "jira_weekly_status" {
			attempt = a
		}
	}
	if attempt == nil || attempt.Outcome != "failure" || attempt.ErrorCategory != "cancellation" || attempt.StartedAt.IsZero() || attempt.CompletedAt.Before(attempt.StartedAt) || attempt.Duration <= 0 {
		t.Fatalf("canceled attempt=%+v", attempt)
	}
	for _, saved := range h.store.List() {
		if saved.Type == ResultWeeklyStatus {
			t.Fatal("canceled generation persisted a weekly report")
		}
	}
}
