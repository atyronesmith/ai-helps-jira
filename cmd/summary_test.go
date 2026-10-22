package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/spf13/cobra"
)

func TestRunSummaryCacheOnlyDoesNotInvokeFetch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JIRA_SERVER", "http://127.0.0.1:1")
	t.Setenv("JIRA_EMAIL", "summary-test@example.invalid")
	t.Setenv("JIRA_API_TOKEN", "not-a-real-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	oldUser, oldProject := flagUser, flagProject
	oldOnly, oldRefresh, oldVerbose := flagCacheOnly, flagRefresh, flagVerbose
	oldFormat, oldOutfile := flagFormat, flagOutfile
	t.Cleanup(func() {
		flagUser, flagProject = oldUser, oldProject
		flagCacheOnly, flagRefresh, flagVerbose = oldOnly, oldRefresh, oldVerbose
		flagFormat, flagOutfile = oldFormat, oldOutfile
	})
	flagUser, flagProject = "", ""
	flagCacheOnly, flagRefresh, flagVerbose = true, false, 0
	flagFormat, flagOutfile = "text", filepath.Join(t.TempDir(), "summary.md")

	oldLoader := summaryLoader
	t.Cleanup(func() { summaryLoader = oldLoader })
	calledFetch := false
	summaryLoader = func(ctx context.Context, db *cache.Cache, cfg *config.Config, cacheOnly bool, fetch func(context.Context, []string) ([]jira.BoardInfo, []jira.Issue, error)) ([]jira.BoardInfo, []jira.Issue, time.Time, error) {
		return oldLoader(ctx, db, cfg, cacheOnly, func(ctx context.Context, keys []string) ([]jira.BoardInfo, []jira.Issue, error) {
			calledFetch = true
			return fetch(ctx, keys)
		})
	}

	err := runSummary(summaryCmd, nil)
	if err == nil {
		t.Fatal("expected cache-only summary without a snapshot to fail")
	}
	if calledFetch {
		t.Fatal("cache-only summary invoked the Jira fetch closure")
	}
}

type summaryCLIHarness struct {
	mu       sync.Mutex
	mode     string
	requests atomic.Int32
	server   *httptest.Server
}

func newSummaryCLIHarness(t *testing.T) *summaryCLIHarness {
	t.Helper()
	h := &summaryCLIHarness{mode: "alice"}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			h.mu.Lock()
			mode := h.mode
			h.mu.Unlock()
			if mode == "fail" {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"errorMessages":["temporary Jira failure"]}`))
				return
			}
			var req struct {
				JQL string `json:"jql"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			owner := "alice@example.test"
			if strings.Contains(req.JQL, "bob@example.test") {
				owner = "bob@example.test"
			}
			rows := []any{}
			switch mode {
			case "empty":
			case "closed":
				rows = append(rows, summaryIssue("TEST-1", owner, "done"))
			case "reassigned":
				rows = append(rows, summaryIssue("TEST-1", "other@example.test", "indeterminate"))
			default:
				key := "TEST-1"
				if owner == "bob@example.test" {
					key = "TEST-2"
				}
				rows = append(rows, summaryIssue(key, owner, "indeterminate"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": rows, "isLast": true, "total": len(rows)})
		case "/rest/api/3/myself":
			_ = json.NewEncoder(w).Encode(map[string]any{"accountId": "alice@example.test"})
		case "/rest/agile/1.0/board":
			_ = json.NewEncoder(w).Encode(map[string]any{"values": []any{}, "startAt": 0, "total": 0, "isLast": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.server.Close)
	return h
}

func summaryIssue(key, assignee, category string) map[string]any {
	return map[string]any{"key": key, "fields": map[string]any{
		"summary": "Summary for " + key, "status": map[string]any{"name": "In Progress"},
		"statusCategory": map[string]any{"key": category}, "project": map[string]any{"key": "TEST"},
		"updated": "2026-10-15T12:00:00.000+0000", "assignee": map[string]any{"emailAddress": assignee, "accountId": assignee},
	}}
}

func setupSummaryCLI(t *testing.T, h *summaryCLIHarness, user string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JIRA_SERVER", h.server.URL)
	t.Setenv("JIRA_EMAIL", "alice@example.test")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")
	oldUser, oldProject := flagUser, flagProject
	oldOnly, oldRefresh := flagCacheOnly, flagRefresh
	oldVerbose, oldFormat, oldOutfile := flagVerbose, flagFormat, flagOutfile
	t.Cleanup(func() {
		flagUser, flagProject = oldUser, oldProject
		flagCacheOnly, flagRefresh = oldOnly, oldRefresh
		flagVerbose, flagFormat, flagOutfile = oldVerbose, oldFormat, oldOutfile
	})
	flagUser, flagProject = user, ""
	flagCacheOnly, flagRefresh = false, false
	flagVerbose, flagFormat = 0, "text"
	flagOutfile = filepath.Join(t.TempDir(), "summary.md")
	return flagOutfile
}

func summaryCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func runSummaryCLI() error { return runSummary(summaryCommand(), nil) }

func summaryIssues(t *testing.T, user string) ([]jira.Issue, time.Time) {
	t.Helper()
	cfg := &config.Config{JiraServer: os.Getenv("JIRA_SERVER"), JiraEmail: os.Getenv("JIRA_EMAIL"), JiraAPIToken: os.Getenv("JIRA_API_TOKEN"), JiraProject: "TEST", JiraUser: user}
	db, err := cache.OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, issues, fetchedAt, err := db.GetSummary(cfg.Assignee(), cfg.JiraProject)
	if err != nil {
		t.Fatal(err)
	}
	return issues, fetchedAt
}

func TestRunSummaryOnlineReconcilesOmittedClosedAndReassignedIssues(t *testing.T) {
	for _, removedBy := range []string{"empty", "closed", "reassigned"} {
		t.Run(removedBy, func(t *testing.T) {
			h := newSummaryCLIHarness(t)
			outfile := setupSummaryCLI(t, h, "")
			h.mode = "alice"
			if err := runSummaryCLI(); err != nil {
				t.Fatalf("initial online summary: %v", err)
			}
			if issues, _ := summaryIssues(t, ""); len(issues) != 1 || issues[0].Key != "TEST-1" {
				t.Fatalf("initial cached issues = %#v", issues)
			}
			output, err := os.ReadFile(outfile)
			if err != nil {
				t.Fatalf("summary output file: %v", err)
			}
			if !strings.Contains(string(output), "TEST-1") {
				t.Fatalf("summary output omitted cached issue: %s", output)
			}

			h.mu.Lock()
			h.mode = removedBy
			h.mu.Unlock()
			if err := runSummaryCLI(); err != nil {
				t.Fatalf("reconciliation refresh: %v", err)
			}
			flagCacheOnly = true
			before := h.requests.Load()
			if err := runSummaryCLI(); err != nil {
				t.Fatalf("cache-only summary: %v", err)
			}
			if got := h.requests.Load(); got != before {
				t.Fatalf("cache-only made HTTP requests: before=%d after=%d", before, got)
			}
			if issues, _ := summaryIssues(t, ""); len(issues) != 0 {
				t.Fatalf("removed issue remained in cache-only result: %#v", issues)
			}
		})
	}
}

func TestRunSummaryFailedRefreshPreservesSnapshotAndCheckpoint(t *testing.T) {
	h := newSummaryCLIHarness(t)
	setupSummaryCLI(t, h, "")
	h.mu.Lock()
	h.mode = "alice"
	h.mu.Unlock()
	if err := runSummaryCLI(); err != nil {
		t.Fatal(err)
	}
	beforeIssues, beforeAt := summaryIssues(t, "")
	if len(beforeIssues) != 1 {
		t.Fatalf("seeded issues = %#v", beforeIssues)
	}
	h.mu.Lock()
	h.mode = "fail"
	h.mu.Unlock()
	if err := runSummaryCLI(); err == nil {
		t.Fatal("expected failed refresh")
	}
	flagCacheOnly = true
	if err := runSummaryCLI(); err != nil {
		t.Fatalf("cache-only after failed refresh: %v", err)
	}
	afterIssues, afterAt := summaryIssues(t, "")
	if len(afterIssues) != 1 || afterIssues[0].Key != beforeIssues[0].Key {
		t.Fatalf("failed refresh changed snapshot: %#v", afterIssues)
	}
	if !afterAt.Equal(beforeAt) {
		t.Fatalf("failed refresh changed checkpoint: before %s, after %s", beforeAt, afterAt)
	}
}

func TestRunSummaryOverridesHaveSeparateMembershipAndCacheOnlyDoesNotFetch(t *testing.T) {
	h := newSummaryCLIHarness(t)
	setupSummaryCLI(t, h, "alice@example.test")
	if err := runSummaryCLI(); err != nil {
		t.Fatal(err)
	}
	flagUser = "bob@example.test"
	if err := runSummaryCLI(); err != nil {
		t.Fatal(err)
	}
	if issues, _ := summaryIssues(t, "alice@example.test"); len(issues) != 1 || issues[0].Key != "TEST-1" {
		t.Fatalf("alice snapshot = %#v", issues)
	}
	if issues, _ := summaryIssues(t, "bob@example.test"); len(issues) != 1 || issues[0].Key != "TEST-2" {
		t.Fatalf("bob snapshot = %#v", issues)
	}
	flagCacheOnly = true
	before := h.requests.Load()
	if err := runSummaryCLI(); err != nil {
		t.Fatalf("cache-only bob summary: %v", err)
	}
	if got := h.requests.Load(); got != before {
		t.Fatalf("cache-only made HTTP requests: before=%d after=%d", before, got)
	}
}
