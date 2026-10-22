package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

func TestHandleQueryActivityUsesAuthoredEvidenceAndStoresReport(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	const input = "Which Jira issues did I work on between 2026-10-12 and 2026-10-16 in project ALT?"
	var translatorCalled bool
	h.translate = func(context.Context, *config.Config, string) (*llm.QueryResult, error) {
		translatorCalled = true
		return nil, errors.New("activity must not use JQL translation")
	}
	var gotContext context.Context
	var gotConfig *config.Config
	var gotOptions jira.WorkActivityOptions
	h.activityLookup = func(ctx context.Context, cfg *config.Config, opts jira.WorkActivityOptions) (jira.ActivityResult, error) {
		gotContext, gotConfig, gotOptions = ctx, cfg, opts
		return jira.ActivityResult{
			Project: "ALT", AccountID: "account-1", TimeZone: "UTC",
			StartDate: "2026-10-12", EndDate: "2026-10-16",
			CandidateJQL: `project = ALT`, CandidateExecuted: true,
			Candidates: 3, Scanned: 3, CandidateComplete: true, Complete: true,
			Matches: []jira.ActivityMatch{{
				Issue:  jira.Issue{Key: "ALT-1", Summary: "Authored evidence", Assignee: "someone-else", AssigneeID: "someone-else"},
				Events: []jira.ActivityEvent{{ID: "comment-1", Kind: jira.ActivityKindComment, AuthorID: "account-1", At: time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC), Detail: "reviewed implementation"}},
			}},
		}, nil
	}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "request-context")
	res, err := h.HandleQuery(ctx, makeRequest(map[string]any{"natural_query": input, "max_results": float64(1)}))
	if err != nil || res == nil || res.IsError {
		t.Fatalf("HandleQuery error=%v result=%+v", err, res)
	}
	if translatorCalled {
		t.Fatal("activity request reached JQL translation/provider path")
	}
	if gotContext.Value(ctxKey{}) != "request-context" {
		t.Fatal("activity lookup did not receive caller context")
	}
	if gotConfig.JiraProject != "ALT" || gotOptions.Project != "ALT" || gotOptions.StartDate != "2026-10-12" || gotOptions.EndDate != "2026-10-16" || gotOptions.MaxIssues != 1000 || gotOptions.MaxEventPages != 20 {
		t.Fatalf("activity config/options = project %q, %+v", gotConfig.JiraProject, gotOptions)
	}
	for _, want := range []string{"Activity report: ALT", input, "ALT-1", "reviewed implementation", "Matching issues: 1 (complete; exact count)"} {
		if !strings.Contains(resultText(res), want) {
			t.Errorf("activity result missing %q: %s", want, resultText(res))
		}
	}
	if strings.Contains(resultText(res), "status =") || strings.Contains(resultText(res), "Search summary") {
		t.Fatalf("activity output was presented as an issue-search result: %s", resultText(res))
	}
	stored := h.store.List()
	if len(stored) != 1 {
		t.Fatalf("stored query history count = %d, want 1", len(stored))
	}
	data, ok := stored[0].Data.(*QueryResultData)
	if !ok || data.Activity == nil {
		t.Fatalf("stored result data = %#v, want activity query data", stored[0].Data)
	}
	if data.Query != input || data.Route != "activity" || data.Outcome != "success" || data.ExecutedJQL != `project = ALT` || data.Search != nil || len(data.Issues) != 0 || data.ActivityDisplayLimit != 1 {
		t.Fatalf("stored activity trace = %+v", data)
	}
	if !strings.Contains(data.Summary(), "complete") || !strings.Contains(data.ActivityText(), "reviewed implementation") {
		t.Fatalf("stored activity summary/text missing evidence: summary=%q text=%q", data.Summary(), data.ActivityText())
	}
}

func TestHandleQueryActivityParsesBeforeProviderAndRejectsAmbiguity(t *testing.T) {
	for _, input := range []string{
		"I worked on during Oct 12 and Oct 16 assigned to Alice",
		"I worked on during Oct 16 and Oct 12",
	} {
		t.Run(input, func(t *testing.T) {
			h := newTestHandlers(t)
			setTestJIRAEnv(t)
			var lookupCalled, translatorCalled bool
			h.activityLookup = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
				lookupCalled = true
				return jira.ActivityResult{}, nil
			}
			h.translate = func(context.Context, *config.Config, string) (*llm.QueryResult, error) {
				translatorCalled = true
				return nil, errors.New("must not call provider")
			}
			res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": input}))
			if err != nil || res == nil || !res.IsError {
				t.Fatalf("HandleQuery error=%v result=%+v", err, res)
			}
			if lookupCalled || translatorCalled {
				t.Fatalf("unsupported/ambiguous request reached lookup=%v translator=%v", lookupCalled, translatorCalled)
			}
		})
	}
}

func TestHandleQueryActivityRecommendationRemainsOrdinaryQuery(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	var lookupCalled, translatorCalled bool
	h.activityLookup = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		lookupCalled = true
		return jira.ActivityResult{}, nil
	}
	h.translate = func(context.Context, *config.Config, string) (*llm.QueryResult, error) {
		translatorCalled = true
		return nil, errors.New("ordinary query test stop")
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "Which issues should I work on next?"}))
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("HandleQuery error=%v result=%+v", err, res)
	}
	if lookupCalled || !translatorCalled {
		t.Fatalf("recommendation routed to activity=%v translator=%v", lookupCalled, translatorCalled)
	}
}

func TestHandleQueryActivityPreservesPartialEvidenceOnError(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	activityErr := errors.New("event page unavailable")
	h.activityLookup = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		return jira.ActivityResult{
			Project: "TEST", AccountID: "account-1", TimeZone: "UTC",
			StartDate: "2026-10-12", EndDate: "2026-10-16",
			CandidateJQL: `project = TEST`, CandidateExecuted: true,
			Candidates: 2, Scanned: 1, CandidateComplete: false, Complete: false,
			Matches: []jira.ActivityMatch{{Issue: jira.Issue{Key: "TEST-1", Summary: "Partial match"}}},
		}, activityErr
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "I worked on during 2026-10-12 and 2026-10-16",
	}))
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("HandleQuery error=%v result=%+v", err, res)
	}
	if !strings.Contains(resultText(res), "Partial match") || !strings.Contains(resultText(res), "partial scan") || !strings.Contains(resultText(res), activityErr.Error()) {
		t.Fatalf("partial activity evidence missing: %s", resultText(res))
	}
	stored := h.store.List()
	if len(stored) != 1 {
		t.Fatalf("stored query history count = %d, want 1", len(stored))
	}
	data := stored[0].Data.(*QueryResultData)
	if data.Outcome != "partial" || data.Activity == nil || len(data.Activity.Matches) != 1 || data.ExecutedJQL != `project = TEST` || data.ErrorStage != "activity" {
		t.Fatalf("partial activity trace = %+v", data)
	}
}

func TestHandleQueryActivityIncompleteWithoutErrorReturnsPartialError(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	h.activityLookup = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		return jira.ActivityResult{
			Project: "TEST", AccountID: "account", TimeZone: "UTC",
			StartDate: "2026-10-12", EndDate: "2026-10-16",
			CandidateJQL: `project = TEST`, CandidateExecuted: false,
			Candidates: 2, Scanned: 1, CandidateComplete: false, Complete: false,
			Matches: []jira.ActivityMatch{{Issue: jira.Issue{Key: "TEST-1", Summary: "Partial evidence"}}},
		}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "I worked on during 2026-10-12 and 2026-10-16",
	}))
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("HandleQuery error=%v result=%+v", err, res)
	}
	if !strings.Contains(resultText(res), "Partial evidence") || !strings.Contains(resultText(res), "partial scan") || !strings.Contains(resultText(res), "did not complete") {
		t.Fatalf("incomplete activity report missing details: %s", resultText(res))
	}
	stored := h.store.List()
	if len(stored) != 1 || stored[0].Data.(*QueryResultData).Outcome != "partial" || stored[0].Data.(*QueryResultData).ExecutedJQL != "" {
		t.Fatalf("incomplete query history = %+v", stored)
	}
}

func TestHandleQueryActivityProjectConflictDoesNotLookup(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	called := false
	h.activityLookup = func(context.Context, *config.Config, jira.WorkActivityOptions) (jira.ActivityResult, error) {
		called = true
		return jira.ActivityResult{}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "I worked on during 2026-10-12 and 2026-10-16 in project ALT",
		"project":       "TEST",
	}))
	if err != nil || res == nil || !res.IsError || called {
		t.Fatalf("project conflict error=%v result=%+v lookupCalled=%v", err, res, called)
	}
}

func TestHandleQueryActivityProjectSuffixNeedsNoDefaultProject(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	t.Setenv("JIRA_PROJECT", "")
	var gotProject string
	h.activityLookup = func(_ context.Context, cfg *config.Config, opts jira.WorkActivityOptions) (jira.ActivityResult, error) {
		gotProject = cfg.JiraProject
		if opts.Project != "ALT" {
			t.Errorf("activity project option = %q, want ALT", opts.Project)
		}
		return jira.ActivityResult{Project: "ALT", AccountID: "acct", TimeZone: "UTC", StartDate: "2026-10-12", EndDate: "2026-10-16", CandidateComplete: true, Complete: true}, nil
	}
	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "I worked on during 2026-10-12 and 2026-10-16 in project ALT",
	}))
	if err != nil || res == nil || res.IsError {
		t.Fatalf("HandleQuery error=%v result=%+v", err, res)
	}
	if gotProject != "ALT" {
		t.Fatalf("loaded config project = %q, want ALT", gotProject)
	}
}

func TestActivityTranslationErrorWrapsAmbiguity(t *testing.T) {
	_, _, err := nl2jql.ParseActivityRequest("I worked on during Oct 12 and Oct 16 assigned to Alice", time.Now())
	if !errors.Is(err, nl2jql.ErrAmbiguousIntent) {
		t.Fatalf("activity parse error = %v, want ErrAmbiguousIntent", err)
	}
}
