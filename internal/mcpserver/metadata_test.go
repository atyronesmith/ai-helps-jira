package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

func metadataMCPFixture(t *testing.T) (*Handlers, *cache.Cache, *atomic.Int32) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected Jira request", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("JIRA_SERVER", srv.URL)
	t.Setenv("JIRA_EMAIL", "tokenless@example.test")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "TEST")
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_FIELD_ALIASES", "")
	cfg, err := config.LoadTranslation("", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := cache.OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	scoped, err := db.ForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.ReplaceFields(context.Background(), []fieldmeta.Field{{ID: "customfield_123", Name: "Story Points", Type: "number"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = scoped.Close()
	store := &ResultStore{results: make(map[string]*StoredResult)}
	return NewHandlers(store, db, 18080, "127.0.0.1"), db, &requests
}

func TestHandleTranslateQueryUsesCachedMetadataWithoutJiraAccess(t *testing.T) {
	h, _, requests := metadataMCPFixture(t)
	for _, tc := range []struct{ query, want string }{{"my open bugs in TEST", `issuetype = Bug`}, {"my work items with story points > 3", "cf[123] > 3"}} {
		res, err := h.HandleTranslateQuery(context.Background(), makeRequest(map[string]any{"natural_query": tc.query}))
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("translation error: %s", resultText(res))
		}
		var got struct {
			Input      string `json:"input"`
			JQL        string `json:"jql"`
			Validation struct {
				Valid bool `json:"valid"`
			} `json:"validation"`
		}
		if err := json.Unmarshal([]byte(resultText(res)), &got); err != nil {
			t.Fatalf("decode response: %v: %s", err, resultText(res))
		}
		if got.Input != tc.query {
			t.Errorf("input=%q, want original wording %q", got.Input, tc.query)
		}
		if !strings.Contains(got.JQL, tc.want) {
			t.Errorf("JQL=%q missing %q", got.JQL, tc.want)
		}
		if !got.Validation.Valid {
			t.Errorf("JQL failed local validation: %s", resultText(res))
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("translate-only path made %d Jira HTTP requests", requests.Load())
	}
	if got := len(h.store.List()); got != 0 {
		t.Fatalf("translation stored %d issue results", got)
	}
}

func TestHandleTranslateQueryPropagatesCanceledContextWithoutJiraAccess(t *testing.T) {
	h, _, requests := metadataMCPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := h.HandleTranslateQuery(ctx, makeRequest(map[string]any{"natural_query": "my open bugs in TEST"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("canceled request returned success: %s", resultText(res))
	}
	if requests.Load() != 0 {
		t.Fatalf("canceled translation made %d Jira HTTP requests", requests.Load())
	}
}
