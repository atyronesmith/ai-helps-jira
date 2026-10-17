package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestQueryFailedAttemptLifecycle(t *testing.T) {
	for _, stage := range []string{"input", "configuration", "translation", "validation", "search"} {
		t.Run(stage, func(t *testing.T) {
			h := newTestHandlers(t)
			setTestJIRAEnv(t)
			var searches atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				searches.Add(1)
				http.Error(w, "secret-upstream-response", http.StatusUnauthorized)
			}))
			defer srv.Close()
			t.Setenv("JIRA_SERVER", srv.URL)
			original := "  my open bugs  "
			category := "invalid_input"
			h.translate = func(_ context.Context, _ *config.Config, input string) (*llm.QueryResult, error) {
				if input != original {
					t.Errorf("original wording changed: %q", input)
				}
				partial := &llm.QueryResult{NormalizedInput: "my open bugs", Route: llm.RouteFastPath, JQL: "project = TEST"}
				if stage == "translation" {
					partial.Route, partial.ProviderCalls = llm.RouteFallback, 1
					return partial, translationerror.New(translationerror.Authentication, errors.New("secret-provider-response"))
				}
				if stage == "validation" {
					partial.JQL = "status = Open"
				}
				return partial, nil
			}
			switch stage {
			case "input":
				original = "project = TEST"
			case "configuration":
				t.Setenv("JIRA_PROJECT", "")
				category = "configuration"
			case "translation":
				category = "authentication"
			case "validation":
				category = "invalid_jql"
			case "search":
				category = "authentication"
			}
			res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
			if err != nil || res == nil || !res.IsError {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			history := h.store.List()
			if len(history) != 1 {
				t.Fatalf("history=%d", len(history))
			}
			q := history[0].Data.(*QueryResultData)
			if q.Query != original || q.Outcome != "failure" || q.ErrorStage != stage || q.ErrorCategory != category {
				t.Fatalf("attempt=%+v", q)
			}
			if q.StartedAt.IsZero() || q.CompletedAt.Before(q.StartedAt) || q.Duration <= 0 || q.Duration != q.CompletedAt.Sub(q.StartedAt) {
				t.Fatalf("invalid lifecycle: %+v", q)
			}
			if q.Metadata().Complete || q.Metadata().Total != nil {
				t.Fatalf("failed attempt claimed completeness: %+v", q.Metadata())
			}
			if stage == "translation" && (q.NormalizedInput != "my open bugs" || q.Route != llm.RouteFallback || q.GeneratedJQL == "") {
				t.Fatalf("partial diagnostics lost: %+v", q)
			}
			if stage != "search" && (q.ExecutedJQL != "" || searches.Load() != 0) {
				t.Fatalf("failed preflight executed search: %+v calls=%d", q, searches.Load())
			}
			if stage == "search" && (q.ExecutedJQL != "project = TEST" || q.Validation != "local_valid" || searches.Load() != 1) {
				t.Fatalf("search trace=%+v calls=%d", q, searches.Load())
			}
			encoded, _ := json.Marshal(q)
			if strings.Contains(string(encoded), "secret-") || strings.Contains(string(encoded), "fake-token") {
				t.Fatalf("raw upstream secret persisted: %s", encoded)
			}
		})
	}
}

func TestQueryExactExecutionSurvivesRestart(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	dir := t.TempDir()
	h.store = openTestStore(t, dir, "execution", 0, 0)
	const original = "  show my recently updated bugs  "
	const exact = "project = TEST  AND updated >= -7d ORDER BY updated DESC"
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JQL string `json:"jql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = body.JQL
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()
	t.Setenv("JIRA_SERVER", srv.URL)
	h.translate = func(_ context.Context, _ *config.Config, input string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: exact, Route: llm.RouteFastPath, NormalizedInput: "show my recently updated bugs"}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	saved := h.store.List()[0]
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, dir, "execution", 0, 0)
	stored, ok := reopened.Get(saved.ID)
	if !ok {
		t.Fatal("missing restarted query")
	}
	q := stored.Data.(*QueryResultData)
	if received != exact || q.ExecutedJQL != received || q.GeneratedJQL != exact || q.DisplayJQL == exact || q.Query != original || q.Outcome != "success" || !q.Metadata().Complete {
		t.Fatalf("received=%q data=%+v", received, q)
	}
	if q.StartedAt.IsZero() || q.CompletedAt.IsZero() || q.Duration <= 0 {
		t.Fatalf("missing persisted timing: %+v", q)
	}
}

func TestDependenciesAndPassivePolling(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	var jiraCalls, providerCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jiraCalls.Add(1); http.Error(w, "not ready", 503) }))
	defer srv.Close()
	t.Setenv("JIRA_SERVER", srv.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		providerCalls.Add(1)
		return &llm.QueryResult{JQL: "project = TEST", Route: llm.RouteFallback, ProviderCalls: 1}, nil
	}
	ws := NewWebServer(h.store, h.cache, 0, "127.0.0.1")
	h.dependencies = ws.dependencies
	res, err := h.withDiagnostics("jira_query", h.HandleQuery)(context.Background(), makeRequest(map[string]any{"natural_query": "show open bugs"}))
	if err != nil || !res.IsError {
		t.Fatalf("query=%+v err=%v", res, err)
	}
	before := ws.status()
	if before.Status != "ok" || before.Readiness != "not_ready" {
		t.Fatalf("status=%+v", before)
	}
	observed := map[string]DependencyObservation{}
	for _, obs := range before.Dependencies {
		observed[obs.Name] = obs
	}
	if observed["jira"].State != "failed" || observed["provider"].State != "ok" || observed["jira"].CheckedAt.IsZero() {
		t.Fatalf("observations=%+v", observed)
	}
	for i := 0; i < 10; i++ {
		for _, poll := range []func(http.ResponseWriter, *http.Request){ws.handleStatus, ws.handleReadyz, ws.handleHealthz, ws.handleIndex} {
			recorder := httptest.NewRecorder()
			poll(recorder, httptest.NewRequest("GET", "/", nil))
		}
	}
	after := ws.status()
	if jiraCalls.Load() != 1 || providerCalls.Load() != 1 {
		t.Fatalf("passive polling called dependencies: jira=%d provider=%d", jiraCalls.Load(), providerCalls.Load())
	}
	for i, obs := range before.Dependencies {
		if !after.Dependencies[i].CheckedAt.Equal(obs.CheckedAt) {
			t.Fatalf("poll refreshed checked-at for %s", obs.Name)
		}
	}
	ready := httptest.NewRecorder()
	ws.handleReadyz(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 503 {
		t.Fatalf("failed dependency readyz=%d", ready.Code)
	}
	live := httptest.NewRecorder()
	ws.handleHealthz(live, httptest.NewRequest("GET", "/healthz", nil))
	if live.Code != 200 {
		t.Fatal("dependency failure broke liveness")
	}
}

func TestProviderFailureIsObservedWithoutSearch(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	h.translate = func(context.Context, *config.Config, string) (*llm.QueryResult, error) {
		return &llm.QueryResult{Route: llm.RouteFallback, ProviderCalls: 1}, translationerror.New(translationerror.Connectivity, errors.New("private upstream detail"))
	}
	_, _ = h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "show unusual work"}))
	observations, readiness := h.dependencies.snapshot(time.Now())
	if readiness != "not_ready" {
		t.Fatalf("readiness=%s observations=%+v", readiness, observations)
	}
	for _, obs := range observations {
		if obs.Name == "provider" && (obs.State != "failed" || obs.CheckedAt.IsZero()) {
			t.Fatalf("provider=%+v", obs)
		}
	}
}

func TestReadinessUnknownStaleAndConcurrentObservations(t *testing.T) {
	d := newDependencyObservations()
	if _, state := d.snapshot(time.Now()); state != "unknown" {
		t.Fatal(state)
	}
	for _, name := range []string{"mcp", "history", "cache", "jira", "provider"} {
		d.observe(name, nil)
	}
	if _, state := d.snapshot(time.Now()); state != "ready" {
		t.Fatal(state)
	}
	if _, state := d.snapshot(time.Now().Add(observationMaxAge + time.Second)); state != "unknown" {
		t.Fatal(state)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.observe("jira", errors.New("failed"))
				d.snapshot(time.Now())
			}
		}()
	}
	wg.Wait()
}

func TestGenericToolFailuresAreRecordedWithoutArgumentsOrErrors(t *testing.T) {
	h := newTestHandlers(t)
	next := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return errorResult("secret-response"), nil
	}
	_, _ = h.withDiagnostics("jira_edit_issue", next)(context.Background(), makeRequest(map[string]any{"token": "private-argument"}))
	list := h.store.List()
	if len(list) != 1 {
		t.Fatalf("history=%d", len(list))
	}
	attempt := list[0].Data.(*AttemptResultData)
	if attempt.Tool != "jira_edit_issue" || attempt.Outcome != "failure" || attempt.ErrorCategory != "tool_error" || attempt.Duration <= 0 {
		t.Fatalf("attempt=%+v", attempt)
	}
	data, _ := json.Marshal(attempt)
	if strings.Contains(string(data), "secret-response") || strings.Contains(string(data), "private-argument") {
		t.Fatalf("unsafe diagnostics: %s", data)
	}
}

func TestReadinessRequiresRecentSuccessfulStorageAndDependencies(t *testing.T) {
	store := openTestStore(t, t.TempDir(), "readiness", 0, 0)
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	for _, name := range []string{"mcp", "cache", "jira", "provider"} {
		ws.dependencies.observe(name, nil)
	}
	ready := httptest.NewRecorder()
	ws.handleReadyz(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 200 || ws.status().Readiness != "ready" {
		t.Fatalf("recent successful observations not ready: %s", ready.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store.Save(ResultQuery, "must not silently become volatile", &QueryResultData{})
	if err := store.PersistenceError(); err == nil {
		t.Fatal("closed durable store reported successful volatile save")
	}
	failed := httptest.NewRecorder()
	ws.handleReadyz(failed, httptest.NewRequest("GET", "/readyz", nil))
	if failed.Code != 503 || ws.status().Readiness != "not_ready" {
		t.Fatalf("closed storage reported ready: %s", failed.Body.String())
	}
}

func TestDigestAttemptTracePreservesInputAndSearchAcrossRestart(t *testing.T) {
	for _, translationFails := range []bool{true, false} {
		t.Run(map[bool]string{true: "translation_failure", false: "empty_search_success"}[translationFails], func(t *testing.T) {
			h := newTestHandlers(t)
			setTestJIRAEnv(t)
			dir := t.TempDir()
			h.store = openTestStore(t, dir, "digest-trace", 0, 0)
			const original = "  open    bugs  "
			const exact = "project = TEST AND updated >= -7d"
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					JQL string `json:"jql"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.JQL != exact {
					t.Errorf("search jql=%q err=%v", body.JQL, err)
				}
				_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
			}))
			defer srv.Close()
			t.Setenv("JIRA_SERVER", srv.URL)
			h.digestTranslate = func(_ context.Context, _ *config.Config, input string) (*llm.QueryResult, error) {
				if input != "open bugs" {
					t.Errorf("digest translation behavior changed: %q", input)
				}
				result := &llm.QueryResult{JQL: exact, NormalizedInput: "open bugs", Route: llm.RouteFastPath}
				if translationFails {
					return result, translationerror.New(translationerror.Unsupported, errors.New("private body"))
				}
				return result, nil
			}
			result, err := h.withDiagnostics("jira_digest", h.HandleDigest)(context.Background(), makeRequest(map[string]any{"issues": original}))
			if err != nil || result.IsError != translationFails {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			id := h.store.List()[0].ID
			if err := h.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTestStore(t, dir, "digest-trace", 0, 0)
			saved, ok := reopened.Get(id)
			if !ok {
				t.Fatal("missing digest attempt after restart")
			}
			attempt := saved.Data.(*AttemptResultData)
			trace := attempt.Trace
			if trace == nil || trace.Query != original || trace.NormalizedInput != "open bugs" || trace.GeneratedJQL != exact || trace.Duration <= 0 {
				t.Fatalf("trace=%+v", trace)
			}
			if translationFails {
				if attempt.Outcome != "failure" || trace.ErrorStage != "translation" || trace.ExecutedJQL != "" || calls.Load() != 0 {
					t.Fatalf("failed digest=%+v trace=%+v calls=%d", attempt, trace, calls.Load())
				}
			} else if attempt.Outcome != "success" || trace.ExecutedJQL != exact || trace.DisplayJQL == exact || !trace.Metadata().Complete || calls.Load() != 1 {
				t.Fatalf("successful digest=%+v trace=%+v calls=%d", attempt, trace, calls.Load())
			}
		})
	}
}

func TestFailedTranslateOnlyAttemptRetainsDiagnosticsWithoutSearch(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	const original = "  unusual work  "
	h.translate = func(_ context.Context, _ *config.Config, input string) (*llm.QueryResult, error) {
		if input != original {
			t.Errorf("input=%q", input)
		}
		return &llm.QueryResult{NormalizedInput: "unusual work", Route: llm.RouteFallback, ProviderCalls: 1}, translationerror.New(translationerror.Timeout, errors.New("private upstream error"))
	}
	result, err := h.withDiagnostics("jira_translate_query", h.HandleTranslateQuery)(context.Background(), makeRequest(map[string]any{"natural_query": original}))
	if err != nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	list := h.store.List()
	if len(list) != 1 {
		t.Fatalf("history=%d", len(list))
	}
	a := list[0].Data.(*AttemptResultData)
	if a.Trace == nil || a.Trace.Query != original || a.Trace.NormalizedInput != "unusual work" || a.Trace.ExecutedJQL != "" || a.ErrorCategory != "timeout" {
		t.Fatalf("attempt=%+v trace=%+v", a, a.Trace)
	}
}

func TestAttemptStorageFailurePreservesCompletedToolResponse(t *testing.T) {
	h := newTestHandlers(t)
	h.store = openTestStore(t, t.TempDir(), "closed", 0, 0)
	_ = h.store.Close()
	expected := textResult("remote operation completed")
	result, err := h.withDiagnostics("jira_edit_issue", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) { return expected, nil })(context.Background(), makeRequest(nil))
	if err != nil || result != expected || result.IsError {
		t.Fatalf("storage changed completed result: %+v err=%v", result, err)
	}
	if h.store.PersistenceError() == nil {
		t.Fatal("storage failure missing")
	}
}
