package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

func TestFinishQueryUsesBoundedExactCountOptionsAndReturnsPartialError(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "caller")
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	partialErr := errors.New("request deadline exceeded")
	called := false
	err := finishQuery(cmd, &config.Config{JiraProject: "SUP"}, &llm.QueryResult{
		Input: "how many open bugs", NormalizedInput: "how many open bugs", Route: llm.RouteFastPath,
		JQL: `project = "SUP" AND statusCategory != Done`,
	}, nil, false, 7, true, false, func(gotCtx context.Context, _ *config.Config, _ string, opts jira.SearchOptions) (jira.SearchResult, error) {
		called = true
		if gotCtx.Value(contextKey{}) != "caller" {
			t.Error("search did not receive command context")
		}
		if opts != (jira.SearchOptions{Limit: 7, PageSize: 100, ExactCount: true, MaxPages: 100}) {
			t.Errorf("search options = %+v", opts)
		}
		return jira.SearchResult{SearchInfo: jira.SearchInfo{Returned: 0}}, partialErr
	})
	if !called {
		t.Fatal("search was not called")
	}
	if !errors.Is(err, partialErr) {
		t.Fatalf("finishQuery() error = %v, want partial search error", err)
	}
}

func TestRunQuerySelectsExactCountAndPreservesOriginalInputAndContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     string
		explicit  bool
		wantExact bool
	}{
		{name: "automatic count", input: "How many open bugs?", wantExact: true},
		{name: "explicit count for list wording", input: "show open bugs", explicit: true, wantExact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareRunQuery(t)
			oldExact := flagExactCount
			flagExactCount = tc.explicit
			t.Cleanup(func() { flagExactCount = oldExact })
			type ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey{}, "request")
			cmd := &cobra.Command{}
			cmd.SetContext(ctx)
			var translatedInput string
			var translationContext, searchContext context.Context
			queryProjectLoader = func(context.Context, *config.Config, *cache.Cache) []jira.Project { return nil }
			queryTranslator = func(gotCtx context.Context, _ *config.Config, input string, _ []jira.Project) (*llm.QueryResult, error) {
				translationContext, translatedInput = gotCtx, input
				return &llm.QueryResult{Input: input, NormalizedInput: strings.ToLower(input), Route: llm.RouteFastPath, JQL: `project = "SUP" AND statusCategory != Done`}, nil
			}
			querySearcher = func(gotCtx context.Context, _ *config.Config, _ string, opts jira.SearchOptions) (jira.SearchResult, error) {
				searchContext = gotCtx
				if opts.ExactCount != tc.wantExact {
					t.Errorf("ExactCount = %t, want %t", opts.ExactCount, tc.wantExact)
				}
				return jira.SearchResult{SearchInfo: jira.SearchInfo{Returned: 0, Complete: true, Total: intPointer(0), TotalKind: jira.CountExact}}, nil
			}
			if err := runQuery(cmd, []string{tc.input}); err != nil {
				t.Fatal(err)
			}
			if translatedInput != tc.input {
				t.Errorf("translator input = %q, want exact original %q", translatedInput, tc.input)
			}
			if translationContext.Value(ctxKey{}) != "request" || searchContext.Value(ctxKey{}) != "request" || translationContext.Done() != ctx.Done() || searchContext.Done() != ctx.Done() {
				t.Error("command context was not passed to both translation and search")
			}
		})
	}
}

func TestDisplayQueryResultsDistinguishesUnknownEmptyFromVerifiedEmpty(t *testing.T) {
	unknown := captureStdout(t, func() {
		format.DisplayQueryResults(jira.SearchResult{SearchInfo: jira.SearchInfo{Returned: 0}}, "", "")
	})
	if !strings.Contains(unknown, "Matching total: unknown") || strings.Contains(unknown, "No issues found") {
		t.Fatalf("unknown empty output = %q", unknown)
	}
	verified := captureStdout(t, func() {
		format.DisplayQueryResults(jira.SearchResult{SearchInfo: jira.SearchInfo{Returned: 0, Complete: true, Total: intPointer(0), TotalKind: jira.CountExact}}, "", "")
	})
	if !strings.Contains(verified, "Exact matching total: 0") || !strings.Contains(verified, "No issues found") {
		t.Fatalf("verified empty output = %q", verified)
	}
}

func TestFinishQueryDisplaysPartialRowsBeforeReturningSearchError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	searchErr := errors.New("request canceled")
	output := captureStdout(t, func() {
		err := finishQuery(cmd, &config.Config{JiraProject: "SUP"}, &llm.QueryResult{
			Input: "show bugs", NormalizedInput: "show bugs", Route: llm.RouteFastPath,
			JQL: `project = "SUP" AND issuetype = Bug`,
		}, nil, false, 10, false, true, func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
			more := true
			return jira.SearchResult{Issues: []jira.Issue{{Key: "SUP-1", Summary: "Partial bug", Status: "Open"}}, SearchInfo: jira.SearchInfo{Returned: 1, HasMore: &more, StopReason: "cancelled"}}, searchErr
		})
		if !errors.Is(err, searchErr) {
			t.Errorf("finishQuery() error = %v, want wrapped search error", err)
		}
	})
	for _, want := range []string{"Returned rows: 1", "partial; more matches known", "stopped: cancelled", "SUP-1", "JIRA search stopped before completion.", "JQL: project = \"SUP\" AND issuetype = Bug"} {
		if !strings.Contains(output, want) {
			t.Errorf("partial output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "No issues found") {
		t.Errorf("partial results were described as empty: %s", output)
	}
}

func TestFinishQueryShowsExactCountWhenPreviewIsCapped(t *testing.T) {
	cmd := &cobra.Command{}
	output := captureStdout(t, func() {
		err := finishQuery(cmd, &config.Config{JiraProject: "SUP"}, &llm.QueryResult{
			Input: "how many bugs", NormalizedInput: "how many bugs", Route: llm.RouteFastPath,
			JQL: `project = "SUP" AND issuetype = Bug`,
		}, nil, false, 1, true, false, func(_ context.Context, _ *config.Config, _ string, opts jira.SearchOptions) (jira.SearchResult, error) {
			if opts.Limit != 1 || !opts.ExactCount {
				t.Errorf("options = %+v, want one-row preview and exact enumeration", opts)
			}
			more := true
			return jira.SearchResult{Issues: []jira.Issue{{Key: "SUP-1", Summary: "First bug", Status: "Open"}}, SearchInfo: jira.SearchInfo{Returned: 1, Enumerated: 3, Complete: false, HasMore: &more, Total: intPointer(3), TotalKind: jira.CountExact, StopReason: "result_limit"}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Returned rows: 1", "Exact matching total: 3", "partial; more matches known", "SUP-1"} {
		if !strings.Contains(output, want) {
			t.Errorf("capped exact output missing %q: %s", want, output)
		}
	}
}

func TestRunQueryRejectsExplicitNonpositiveMaxBeforeConfigOrNetwork(t *testing.T) {
	oldMax, oldDry := flagMaxResults, flagDryRun
	t.Cleanup(func() { flagMaxResults, flagDryRun = oldMax, oldDry })
	flagMaxResults, flagDryRun = 0, false
	cmd := &cobra.Command{}
	cmd.Flags().Int("max", 0, "")
	if err := cmd.Flags().Set("max", "0"); err != nil {
		t.Fatal(err)
	}
	err := runQuery(cmd, []string{"show open issues"})
	if err == nil || err.Error() != "--max must be greater than zero" {
		t.Fatalf("runQuery() error = %v", err)
	}
}

func prepareRunQuery(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JIRA_SERVER", "https://example.invalid")
	t.Setenv("JIRA_EMAIL", "user@example.invalid")
	t.Setenv("JIRA_API_TOKEN", "test-token")
	t.Setenv("JIRA_PROJECT", "SUP")
	old := struct {
		dry, show  bool
		max        int
		translator func(context.Context, *config.Config, string, []jira.Project) (*llm.QueryResult, error)
		loader     func(context.Context, *config.Config, *cache.Cache) []jira.Project
		searcher   querySearchFunc
	}{flagDryRun, flagShowJQL, flagMaxResults, queryTranslator, queryProjectLoader, querySearcher}
	flagDryRun, flagShowJQL, flagMaxResults = false, false, 50
	t.Cleanup(func() {
		flagDryRun, flagShowJQL, flagMaxResults = old.dry, old.show, old.max
		queryTranslator, queryProjectLoader, querySearcher = old.translator, old.loader, old.searcher
	})
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	pterm.SetDefaultOutput(w)
	func() {
		defer func() {
			_ = w.Close()
			os.Stdout = old
			pterm.SetDefaultOutput(old)
		}()
		fn()
	}()
	var output bytes.Buffer
	if _, err := io.Copy(&output, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return output.String()
}

func intPointer(value int) *int { return &value }
