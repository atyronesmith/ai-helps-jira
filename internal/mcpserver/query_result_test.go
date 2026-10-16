package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

func TestHandleQueryRejectsInvalidMaxResultsBeforeTranslation(t *testing.T) {
	for _, value := range []any{float64(0), float64(-1), float64(1.5), "2"} {
		t.Run("invalid", func(t *testing.T) {
			h := newTestHandlers(t)
			setTestJIRAEnv(t)
			called := false
			h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
				called = true
				return nil, nil
			}
			res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "open bugs", "max_results": value}))
			if err != nil || !res.IsError || called {
				t.Fatalf("result=%+v err=%v translate_called=%v", res, err, called)
			}
		})
	}
}

func TestHandleQueryInterruptedSearchReturnsAndStoresPartialPreview(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": sampleJQLIssues("TEST-1"), "nextPageToken": "next"})
			return
		}
		http.Error(w, "page two unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "count open bugs", "max_results": float64(1)}))
	if err != nil || !res.IsError {
		t.Fatalf("result=%+v err=%v; want MCP error with partial data", res, err)
	}
	assertContains(t, resultText(res), "TEST-1")
	assertContains(t, resultText(res), "page two unavailable")
	assertContains(t, resultText(res), "Completeness:")
	list := h.store.List()
	if len(list) != 1 {
		t.Fatalf("saved partial results=%d, want 1", len(list))
	}
	data := list[0].Data.(*QueryResultData)
	if data.Search == nil || data.Search.TotalKind != jira.CountUnknown || data.Search.Total != nil || len(data.Issues) != 1 || data.SearchError == "" {
		t.Fatalf("saved interrupted metadata=%+v error=%q rows=%d", data.Search, data.SearchError, len(data.Issues))
	}
	var dashboard strings.Builder
	ws := NewWebServer(h.store, nil, 0, "127.0.0.1")
	if err := ws.tmpls["query.html"].ExecuteTemplate(&dashboard, "layout", list[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dashboard.String(), "page two unavailable") {
		t.Fatalf("dashboard omitted search error: %s", dashboard.String())
	}
}

func TestHandleQueryEmptyIncompleteDoesNotClaimNoIssues(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": 0})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "show open bugs"}))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	assertContains(t, resultText(res), "Jira-reported matching total (unverified): 0")
	assertContains(t, resultText(res), "Completeness: partial")
	if strings.Contains(resultText(res), "No issues found") {
		t.Fatalf("incomplete empty response claimed no matches: %s", resultText(res))
	}
}

func TestHandleQueryCountQuestionEnumeratesBeyondPreviewCap(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page++
		if page == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": sampleJQLIssues("TEST-1"), "nextPageToken": "next"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": sampleJQLIssues("TEST-2"), "isLast": true})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "how many open bugs", "max_results": float64(1)}))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if page != 2 {
		t.Fatalf("search pages=%d, want 2 despite one-row preview cap", page)
	}
	assertContains(t, resultText(res), "Exact matching total: 2")
	assertContains(t, resultText(res), "Returned rows: 1")
	assertContains(t, resultText(res), "TEST-1")
	if strings.Contains(resultText(res), "TEST-2") {
		t.Fatalf("enumerated row beyond preview cap was displayed: %s", resultText(res))
	}
	data := h.store.List()[0].Data.(*QueryResultData)
	if data.Search == nil || data.Search.Total == nil || *data.Search.Total != 2 || data.Search.TotalKind != jira.CountExact || len(data.Issues) != 1 {
		t.Fatalf("stored search metadata=%+v issues=%d", data.Search, len(data.Issues))
	}
}

func TestHandleQueryExactCountFlagCountsBeyondPreviewCap(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		var request struct {
			JQL string `json:"jql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.JQL != "project = TEST" {
			t.Errorf("request JQL=%q decode=%v", request.JQL, err)
		}
		if page == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": sampleJQLIssues("TEST-1"), "nextPageToken": "next"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": sampleJQLIssues("TEST-2", "TEST-3"), "isLast": true})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "show open bugs", "max_results": float64(1), "exact_count": true}))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if page != 2 {
		t.Fatalf("search pages=%d, want 2", page)
	}
	data := h.store.List()[0].Data.(*QueryResultData)
	if data.Search == nil || data.Search.Total == nil || *data.Search.Total != 3 || data.Search.TotalKind != jira.CountExact || data.Search.Complete || data.Search.HasMore == nil || !*data.Search.HasMore || len(data.Issues) != 1 {
		t.Fatalf("search=%+v preview rows=%d", data.Search, len(data.Issues))
	}
	assertContains(t, resultText(res), "Exact matching total: 3")
	assertContains(t, resultText(res), "more matches known")
}

func TestHandleQueryListCapRetainsKnownMoreUnknownTotal(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": sampleJQLIssues("TEST-1"), "nextPageToken": "next"})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "show open bugs", "max_results": float64(1)}))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	data := h.store.List()[0].Data.(*QueryResultData)
	if data.Search == nil || data.Search.Total != nil || data.Search.TotalKind != jira.CountUnknown || data.Search.Complete || data.Search.HasMore == nil || !*data.Search.HasMore {
		t.Fatalf("search metadata=%+v; want incomplete, more known, total unknown", data.Search)
	}
	assertContains(t, resultText(res), "Matching total: unknown")
}

func TestHandleQueryRejectsNonBooleanExactCount(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	called := false
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		called = true
		return nil, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "show open bugs", "exact_count": "true"}))
	if err != nil || !res.IsError || called {
		t.Fatalf("result=%+v err=%v translate_called=%v", res, err, called)
	}
}

func TestHandleQueryEmptyExactZero(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	var recorded string
	ts := jqlSearchServer(t, nil, &recorded)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "show open bugs"}))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	data := h.store.List()[0].Data.(*QueryResultData)
	if data.Search == nil || data.Search.Total == nil || *data.Search.Total != 0 || data.Search.TotalKind != jira.CountExact || !data.Search.Complete {
		t.Fatalf("empty terminal search metadata=%+v", data.Search)
	}
	assertContains(t, resultText(res), "Exact matching total: 0")
}

func TestHandleQueryCanceledSearchDisclosesUnknownCount(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	tss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request unexpectedly reached server despite canceled context")
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(tss.Close)
	t.Setenv("JIRA_SERVER", tss.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := h.HandleQuery(ctx, makeRequest(map[string]any{"natural_query": "show open bugs"}))
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("cancel result=%+v; want error carrying partial metadata", res)
	}
	assertContains(t, resultText(res), "Matching total: unknown")
	assertContains(t, resultText(res), "context canceled")
}
