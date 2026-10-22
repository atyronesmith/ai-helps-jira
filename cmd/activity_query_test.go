package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/spf13/cobra"
)

func TestRunQueryActivityUsesEvidenceScanAndDisplayCap(t *testing.T) {
	setupActivityQuery(t)
	t.Setenv("JIRA_SERVER", "https://example.invalid")
	t.Setenv("JIRA_EMAIL", "person@example.invalid")
	t.Setenv("JIRA_API_TOKEN", "test-token")
	t.Setenv("JIRA_PROJECT", "DEFAULT")
	flagMaxResults = 1
	const input = "Which issues did I work on between 2026-10-12 and 2026-10-16?"
	var gotOptions jira.WorkActivityOptions
	var gotContext context.Context
	queryActivity = func(ctx context.Context, cfg *config.Config, opts jira.WorkActivityOptions) (jira.ActivityResult, error) {
		gotContext, gotOptions = ctx, opts
		if cfg.JiraProject != "DEFAULT" {
			t.Errorf("config project = %q, want DEFAULT", cfg.JiraProject)
		}
		return jira.ActivityResult{
			Project: "DEFAULT", AccountID: "account-1", TimeZone: "UTC",
			StartDate: opts.StartDate, EndDate: opts.EndDate,
			Candidates: 3, Scanned: 3, CandidateComplete: true, Complete: true,
			Matches: []jira.ActivityMatch{
				{Issue: jira.Issue{Key: "DEFAULT-1", Summary: "First"}},
				{Issue: jira.Issue{Key: "DEFAULT-2", Summary: "Second"}},
				{Issue: jira.Issue{Key: "DEFAULT-3", Summary: "Third"}},
			},
		}, nil
	}
	queryTranslator = func(context.Context, *config.Config, string, []jira.Project) (*llm.QueryResult, error) {
		return nil, errors.New("activity request must not translate")
	}
	querySearcher = func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
		return jira.SearchResult{}, errors.New("activity request must not use SearchJQL")
	}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runQuery(cmd, []string{input}); err != nil {
		t.Fatal(err)
	}
	if gotContext.Value(ctxKey{}) != "caller" {
		t.Fatal("activity scan did not receive caller context")
	}
	if gotOptions.Project != "DEFAULT" || gotOptions.StartDate != "2026-10-12" || gotOptions.EndDate != "2026-10-16" || gotOptions.MaxIssues != 1000 || gotOptions.MaxEventPages != 20 {
		t.Fatalf("activity options = %+v", gotOptions)
	}
	for _, want := range []string{"Original request: " + input, "Date range: 2026-10-12 through 2026-10-16", "Matching issues: 3 (complete; exact count)", "Displaying 1 of 3 observed matching issues", "DEFAULT-1"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("activity output missing %q: %s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "DEFAULT-2") || strings.Contains(output.String(), "DEFAULT-3") {
		t.Fatalf("display cap did not limit issue rows: %s", output.String())
	}
}

func TestRunQueryActivityDryRunNeedsNoJiraCredentialsOrHTTP(t *testing.T) {
	setupActivityQuery(t)
	t.Setenv("JIRA_PROJECT", "DEFAULT")
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	flagDryRun = true
	flagProject = ""
	var lookupCalled, translatorCalled, searchCalled bool
	queryActivity = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		lookupCalled = true
		return jira.ActivityResult{}, nil
	}
	queryTranslator = func(context.Context, *config.Config, string, []jira.Project) (*llm.QueryResult, error) {
		translatorCalled = true
		return nil, nil
	}
	querySearcher = func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
		searchCalled = true
		return jira.SearchResult{}, nil
	}
	const input = "Show my work activity during Oct 12 and Oct 16 for TEST"
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runQuery(cmd, []string{input}); err != nil {
		t.Fatal(err)
	}
	if lookupCalled || translatorCalled || searchCalled {
		t.Fatalf("dry run called activity=%v translator=%v search=%v", lookupCalled, translatorCalled, searchCalled)
	}
	for _, want := range []string{"dry run; no Jira request was made", "Original request: " + input, "Project: TEST", "Date range: " + time.Now().Format("2006") + "-10-12 through " + time.Now().Format("2006") + "-10-16", "Years omitted"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("dry-run output missing %q: %s", want, output.String())
		}
	}
}

func TestRunQueryActivityRejectsConflictingProjectFlags(t *testing.T) {
	setupActivityQuery(t)
	t.Setenv("JIRA_SERVER", "https://example.invalid")
	t.Setenv("JIRA_EMAIL", "person@example.invalid")
	t.Setenv("JIRA_API_TOKEN", "test-token")
	t.Setenv("JIRA_PROJECT", "DEFAULT")
	flagProject = "OTHER"
	called := false
	queryActivity = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		called = true
		return jira.ActivityResult{}, nil
	}
	cmd := &cobra.Command{}
	err := runQuery(cmd, []string{"I worked on during 2026-10-12 and 2026-10-16 in project ALT"})
	if err == nil || called || !strings.Contains(err.Error(), "clarify") {
		t.Fatalf("conflicting project error=%v lookupCalled=%v", err, called)
	}
}

func TestRunQueryActivityIncompleteScanReturnsErrorAfterPartialOutput(t *testing.T) {
	setupActivityQuery(t)
	t.Setenv("JIRA_SERVER", "https://example.invalid")
	t.Setenv("JIRA_EMAIL", "person@example.invalid")
	t.Setenv("JIRA_API_TOKEN", "test-token")
	t.Setenv("JIRA_PROJECT", "TEST")
	queryActivity = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		return jira.ActivityResult{
			Project: "TEST", AccountID: "account", TimeZone: "UTC",
			StartDate: "2026-10-12", EndDate: "2026-10-16",
			Candidates: 2, Scanned: 1, CandidateComplete: false, Complete: false,
			Matches: []jira.ActivityMatch{{Issue: jira.Issue{Key: "TEST-1", Summary: "Partial evidence"}}},
		}, nil
	}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	err := runQuery(cmd, []string{"I worked on during 2026-10-12 and 2026-10-16"})
	if !errors.Is(err, jira.ErrIncompleteSearch) {
		t.Fatalf("runQuery error = %v, want ErrIncompleteSearch", err)
	}
	if !strings.Contains(output.String(), "Partial evidence") || !strings.Contains(output.String(), "partial scan") {
		t.Fatalf("partial report was not printed before error: %s", output.String())
	}
}

func setupActivityQuery(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	old := struct {
		dry, show  bool
		max        int
		project    string
		user       string
		activity   func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error)
		translator func(context.Context, *config.Config, string, []jira.Project) (*llm.QueryResult, error)
		searcher   querySearchFunc
	}{flagDryRun, flagShowJQL, flagMaxResults, flagProject, flagUser, queryActivity, queryTranslator, querySearcher}
	flagDryRun, flagShowJQL, flagMaxResults, flagProject, flagUser = false, false, 50, "", ""
	t.Cleanup(func() {
		flagDryRun, flagShowJQL, flagMaxResults, flagProject, flagUser = old.dry, old.show, old.max, old.project, old.user
		queryActivity, queryTranslator, querySearcher = old.activity, old.translator, old.searcher
	})
}
