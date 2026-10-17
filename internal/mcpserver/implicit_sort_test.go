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

type missingOwnerSortProvider struct{ calls atomic.Int32 }

func (p *missingOwnerSortProvider) Generate(context.Context, string, string) (string, error) {
	p.calls.Add(1)
	return `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"statusCategory","operator":"!=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`, nil
}

func TestQueryCreationSortExecutesOwnerOpenScopeAndAscendingOrder(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	const input = "my open issues ordered by date created"
	const want = "statusCategory != Done AND assignee = currentUser() AND project = TEST ORDER BY created ASC"
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != "/rest/api/3/search/jql" {
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
	provider := &missingOwnerSortProvider{}
	fixture := llm.TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"TEST": "TEST"}}
	h.translate = func(ctx context.Context, cfg *config.Config, query string) (*llm.QueryResult, error) {
		if query != input {
			t.Errorf("original wording changed: %q", query)
		}
		return llm.GenerateJQLWithFixture(ctx, cfg, query, fixture, provider)
	}
	result, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": input}))
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
	if trace.Query != input || trace.NormalizedInput != input || trace.ExecutedJQL != want || trace.GeneratedJQL != want || trace.Route != llm.RouteFastPath || trace.Outcome != "success" {
		t.Fatalf("incorrect query diagnostics: %+v", trace)
	}
}
