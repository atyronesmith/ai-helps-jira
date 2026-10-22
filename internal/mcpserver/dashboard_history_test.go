package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func dashboardHistoryRequest(method, target string, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("X-Jira-Clear-History", "1")
	r.Header.Set("Origin", "http://dashboard.example")
	r.Host = "dashboard.example"
	return r
}

func assertHistoryJSONResponse(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	if got := res.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := res.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestDashboardHistoryClearMemoryAndPostClearSave(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	types := []ResultType{ResultSummary, ResultQuery, ResultDigest, ResultEnrich, ResultCreateEpic, ResultWeeklyStatus, ResultFindSimilar, ResultConfluenceAnalytics, ResultAttempt}
	for _, typ := range types {
		store.Save(typ, string(typ), &QueryResultData{Query: "preserved raw API keys"})
	}
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	res := httptest.NewRecorder()
	ws.handleClearHistory(res, dashboardHistoryRequest(http.MethodDelete, "/api/history", ""))
	assertHistoryJSONResponse(t, res)
	if res.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body %s", res.Code, res.Body)
	}
	var result struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil || result.Deleted != len(types) {
		t.Fatalf("DELETE response = %s, err=%v; want deleted=%d", res.Body, err, len(types))
	}
	if rows := store.List(); len(rows) != 0 {
		t.Fatalf("rows after clear = %d", len(rows))
	}
	id := store.Save(ResultQuery, "after clear", &QueryResultData{Query: "still works"})
	if _, ok := store.Get(id); !ok {
		t.Fatal("new result was not available after clear")
	}
	res = httptest.NewRecorder()
	ws.handleClearHistory(res, dashboardHistoryRequest(http.MethodDelete, "/api/history", ""))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"deleted":1`) {
		t.Fatalf("second clear = %d %s", res.Code, res.Body)
	}
	res = httptest.NewRecorder()
	ws.handleClearHistory(res, dashboardHistoryRequest(http.MethodDelete, "/api/history", ""))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"deleted":0`) {
		t.Fatalf("empty clear = %d %s", res.Code, res.Body)
	}
}

func TestDashboardHistoryClearDurableOwnerScope(t *testing.T) {
	dir := t.TempDir()
	owner := openTestStore(t, dir, "dashboard-owner", 0, 0)
	shared := openTestStore(t, dir, "dashboard-owner", 0, 0)
	other := openTestStore(t, dir, "different-dashboard", 0, 0)
	owner.Save(ResultQuery, "owner one", &QueryResultData{})
	owner.Save(ResultAttempt, "owner two", &AttemptResultData{})
	otherID := other.Save(ResultQuery, "other", &QueryResultData{})
	ws := NewWebServer(owner, nil, 0, "127.0.0.1")
	res := httptest.NewRecorder()
	ws.handleClearHistory(res, dashboardHistoryRequest(http.MethodDelete, "/api/history", ""))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"deleted":2`) {
		t.Fatalf("clear = %d %s", res.Code, res.Body)
	}
	if len(shared.List()) != 0 {
		t.Fatal("second handle still sees cleared owner rows")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, dir, "dashboard-owner", 0, 0)
	if len(reopened.List()) != 0 {
		t.Fatal("owner rows returned after reopen")
	}
	if _, ok := other.Get(otherID); !ok {
		t.Fatal("clear changed a different instance")
	}
}

func TestDashboardHistoryRejectsRequestsWithoutMutationOrObservation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		setup  func(*http.Request)
		want   int
	}{
		{name: "method", method: http.MethodGet, target: "/api/history", want: http.StatusMethodNotAllowed},
		{name: "missing header", method: http.MethodDelete, target: "/api/history", setup: func(r *http.Request) { r.Header.Del("X-Jira-Clear-History") }, want: http.StatusForbidden},
		{name: "wrong header", method: http.MethodDelete, target: "/api/history", setup: func(r *http.Request) { r.Header.Set("X-Jira-Clear-History", "true") }, want: http.StatusForbidden},
		{name: "cross origin", method: http.MethodDelete, target: "/api/history", setup: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") }, want: http.StatusForbidden},
		{name: "query", method: http.MethodDelete, target: "/api/history?all=1", want: http.StatusBadRequest},
		{name: "body", method: http.MethodDelete, target: "/api/history", body: "clear", want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &ResultStore{results: make(map[string]*StoredResult)}
			store.Save(ResultQuery, "keep", &QueryResultData{})
			ws := NewWebServer(store, nil, 0, "127.0.0.1")
			beforeErr, beforeAt := store.PersistenceObservation()
			r := dashboardHistoryRequest(tt.method, tt.target, tt.body)
			if tt.setup != nil {
				tt.setup(r)
			}
			res := httptest.NewRecorder()
			ws.handleClearHistory(res, r)
			assertHistoryJSONResponse(t, res)
			if res.Code != tt.want {
				t.Fatalf("status = %d, body=%s; want %d", res.Code, res.Body, tt.want)
			}
			if tt.method == http.MethodGet && res.Header().Get("Allow") != http.MethodDelete {
				t.Errorf("Allow = %q", res.Header().Get("Allow"))
			}
			if len(store.List()) != 1 {
				t.Fatal("rejected request mutated history")
			}
			afterErr, afterAt := store.PersistenceObservation()
			if beforeAt != afterAt || (beforeErr == nil) != (afterErr == nil) {
				t.Fatal("rejected request changed the persistence observation")
			}
		})
	}
}

func TestDashboardHistoryClearClosedStoreIsSanitized(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, "closed-dashboard", 0, 0)
	store.Save(ResultQuery, "retain", &QueryResultData{})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	res := httptest.NewRecorder()
	ws.handleClearHistory(res, dashboardHistoryRequest(http.MethodDelete, "/api/history", ""))
	assertHistoryJSONResponse(t, res)
	if res.Code != http.StatusServiceUnavailable || strings.Contains(res.Body.String(), "sqlite") || strings.Contains(res.Body.String(), dir) {
		t.Fatalf("closed store response = %d %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "closed-dashboard") || !strings.Contains(res.Body.String(), "Could not confirm") {
		t.Fatalf("unsanitized or unexpected failure body: %s", res.Body)
	}
}

func TestDashboardHistoryClearCancelledRequestRetainsRows(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "keep after cancel", &QueryResultData{})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := dashboardHistoryRequest(http.MethodDelete, "/api/history", "").WithContext(ctx)
	res := httptest.NewRecorder()
	ws.handleClearHistory(res, r)
	if res.Code != http.StatusServiceUnavailable || len(store.List()) != 1 {
		t.Fatalf("cancelled clear status=%d rows=%d body=%s", res.Code, len(store.List()), res.Body)
	}
}

func TestDashboardHistoryRegisteredRouteAndPassiveViews(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "before", &QueryResultData{Query: "my issue"})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	port, err := ws.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	get, err := client.Get(base + "/api/history")
	if err != nil {
		t.Fatalf("GET registered route: %v", err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed || get.Header.Get("Allow") != http.MethodDelete {
		t.Fatalf("GET /api/history = %d Allow=%q", get.StatusCode, get.Header.Get("Allow"))
	}
	req, _ := http.NewRequest(http.MethodDelete, base+"/api/history", nil)
	req.Header.Set("X-Jira-Clear-History", "1")
	delete, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE registered route: %v", err)
	}
	body, _ := io.ReadAll(delete.Body)
	delete.Body.Close()
	if delete.StatusCode != http.StatusOK || !strings.Contains(string(body), `"deleted":1`) {
		t.Fatalf("DELETE /api/history = %d %s", delete.StatusCode, body)
	}
	retainedID := store.Save(ResultQuery, "passive observation", &QueryResultData{Query: "my issue"})
	for _, path := range []string{"/", "/api/status", "/readyz", "/healthz"} {
		response, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK && !(path == "/readyz" && response.StatusCode == http.StatusServiceUnavailable) {
			t.Errorf("GET %s status = %d", path, response.StatusCode)
		}
		if _, ok := store.Get(retainedID); !ok {
			t.Fatalf("GET %s changed saved history", path)
		}
	}
}

func TestDashboardStatusHelpExplainsObservationSemantics(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "query", &QueryResultData{Query: "do not change raw API keys"})
	ws := NewWebServer(store, nil, 0, "127.0.0.1")
	var html strings.Builder
	data := struct{ Results []*StoredResult }{Results: store.List()}
	if err := ws.tmpls["index.html"].ExecuteTemplate(&html, "layout", data); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MCP tool calls", "LLM provider", "Unknown", "Stale", "over five minutes old", "Clear log", "do not change raw API keys"} {
		if !strings.Contains(html.String(), want) {
			t.Errorf("dashboard help missing %q", want)
		}
	}
}
