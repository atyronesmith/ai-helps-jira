package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nlfeedback"
)

func TestRunQueryDryRunUsesTokenlessConfigAndCachedProjectsOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "SUP")

	oldDryRun, oldProject, oldMax, oldShow := flagDryRun, flagProject, flagMaxResults, flagShowJQL
	oldTranslator, oldSearch, oldProjectLoader := queryTranslator, querySearcher, queryProjectLoader
	t.Cleanup(func() {
		flagDryRun, flagProject, flagMaxResults, flagShowJQL = oldDryRun, oldProject, oldMax, oldShow
		queryTranslator, querySearcher, queryProjectLoader = oldTranslator, oldSearch, oldProjectLoader
	})
	flagDryRun, flagProject, flagMaxResults, flagShowJQL = true, "", 10, false
	queryProjectLoader = func(*config.Config, *cache.Cache) []jira.Project {
		t.Fatal("dry-run invoked the project warm-up/fetch path")
		return nil
	}
	querySearcher = func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
		t.Fatal("dry-run invoked Jira search")
		return jira.SearchResult{}, nil
	}
	queryTranslator = func(_ context.Context, cfg *config.Config, input string, projects []jira.Project) (*llm.QueryResult, error) {
		if cfg.JiraAPIToken != "" || cfg.JiraServer != "" || cfg.JiraEmail != "" {
			t.Errorf("translation config unexpectedly requires Jira credentials: %+v", cfg)
		}
		if input != "open support bugs" {
			t.Errorf("input = %q", input)
		}
		if len(projects) != 0 {
			t.Errorf("projects = %+v, want empty cold cache", projects)
		}
		return &llm.QueryResult{Input: input, NormalizedInput: input, Route: llm.RouteFastPath, JQL: `project = "SUP" AND statusCategory != Done`}, nil
	}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runQuery(cmd, []string{"open support bugs"}); err != nil {
		t.Fatalf("runQuery() error = %v", err)
	}
	if !strings.Contains(output.String(), "Local validation: valid") {
		t.Fatalf("unexpected dry-run output: %s", output.String())
	}
}

func TestCachedProjectListReadsCacheWithoutWarmup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	db, err := cache.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	want := []jira.Project{{Key: "SUP", Name: "Support"}}
	if err := db.UpsertProjects(want); err != nil {
		t.Fatal(err)
	}
	got := cachedProjectList(db)
	if len(got) != 1 || got[0].Key != "SUP" || got[0].Name != "Support" {
		t.Fatalf("cachedProjectList() = %+v, want %+v", got, want)
	}
}

func TestRunTrainJQLUsesTokenlessConfigAndCachedProjectsOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "SUP")
	oldProject, oldTranslator, oldFeedback := flagProject, queryTranslator, flagFeedbackFile
	t.Cleanup(func() {
		flagProject, queryTranslator, flagFeedbackFile = oldProject, oldTranslator, oldFeedback
	})
	flagProject = ""
	flagFeedbackFile = t.TempDir() + "/feedback.jsonl"
	called := false
	queryTranslator = func(_ context.Context, cfg *config.Config, input string, projects []jira.Project) (*llm.QueryResult, error) {
		called = true
		if cfg.JiraAPIToken != "" || cfg.JiraServer != "" || cfg.JiraEmail != "" {
			t.Errorf("trainer config unexpectedly requires Jira credentials: %+v", cfg)
		}
		if len(projects) != 0 {
			t.Errorf("trainer projects = %+v, want empty cold cache", projects)
		}
		return &llm.QueryResult{Input: input, NormalizedInput: input, Route: llm.RouteFastPath, JQL: `project = "SUP" AND status = Open`}, nil
	}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("open support issues\nquit\n"))
	cmd.SetOut(&bytes.Buffer{})
	if err := runTrainJQL(cmd, nil); err != nil {
		t.Fatalf("runTrainJQL() error = %v", err)
	}
	if !called {
		t.Fatal("trainer did not invoke the translator")
	}
}

func TestFinishQueryDryRunDoesNotSearch(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	called := false
	err := finishQuery(cmd, &config.Config{JiraProject: "SUP"}, &llm.QueryResult{
		Input: "show open bugs", NormalizedInput: "show open bugs", Route: llm.RouteFastPath,
		ParseError: "unexpected token: bugs", JQL: `project = "SUP" AND statusCategory != Done`,
	}, nil, true, 10, false, false, func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
		called = true
		return jira.SearchResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("dry-run called Jira search")
	}
	for _, want := range []string{"Input: show open bugs", "Normalized input: show open bugs", "Route: fast_path", "Parse error: unexpected token: bugs", "JQL: project = \"SUP\" AND statusCategory != Done", "Local validation: valid"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, output.String())
		}
	}
}

func TestInteractiveTrainerRecordsVerdictsAndSkips(t *testing.T) {
	path := t.TempDir() + "/feedback.jsonl"
	inputs := strings.Join([]string{
		"good example", "good",
		"fixed example", "corrected", `project = "SUP" AND status = Open`, "preserve priority",
		"rejected example", "reject",
		"skipped example", "skip",
		"quit",
	}, "\n") + "\n"
	translate := func(input string) (*llm.QueryResult, error) {
		return &llm.QueryResult{Input: input, NormalizedInput: strings.ToLower(input), Route: llm.RouteFastPath, ParseError: "normalizer repaired date", JQL: `project = "SUP" AND status = Open`}, nil
	}
	var output bytes.Buffer
	if err := runInteractiveTrainer(strings.NewReader(inputs), &output, path, "SUP", "test-version", translate); err != nil {
		t.Fatal(err)
	}
	records, err := nlfeedback.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	want := []nlfeedback.Verdict{nlfeedback.VerdictGood, nlfeedback.VerdictCorrected, nlfeedback.VerdictRejected}
	for i := range want {
		if records[i].Verdict != want[i] {
			t.Errorf("record %d verdict = %q, want %q", i, records[i].Verdict, want[i])
		}
		if records[i].TranslatorVersion != "test-version" || records[i].DefaultProject != "SUP" {
			t.Errorf("record metadata missing: %+v", records[i])
		}
		if records[i].ParseError != "normalizer repaired date" {
			t.Errorf("parse error missing: %+v", records[i])
		}
	}
	if records[1].CorrectedJQL == "" || records[1].Correction != "preserve priority" {
		t.Errorf("corrected data missing: %+v", records[1])
	}
}

func TestInteractiveTrainerNaturalLanguageCorrectionAndEOF(t *testing.T) {
	path := t.TempDir() + "/feedback.jsonl"
	input := "query\ncorrection\nI meant unresolved tasks assigned to me\n"
	translator := func(query string) (*llm.QueryResult, error) {
		return &llm.QueryResult{Input: query, Route: llm.RouteFallback, JQL: "status = Open"}, nil
	}
	if err := runInteractiveTrainer(strings.NewReader(input), &bytes.Buffer{}, path, "", "v", translator); err != nil {
		t.Fatal(err)
	}
	records, err := nlfeedback.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Verdict != nlfeedback.VerdictCorrected || records[0].CorrectedJQL != "" || records[0].Correction != "I meant unresolved tasks assigned to me" {
		t.Fatalf("unexpected correction record: %+v", records)
	}

	partial := t.TempDir() + "/partial.jsonl"
	if err := runInteractiveTrainer(strings.NewReader("unfinished query\n"), &bytes.Buffer{}, partial, "", "v", translator); err != nil {
		t.Fatal(err)
	}
	records, err = nlfeedback.Read(partial)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("EOF after query created partial record: %+v", records)
	}
}

func TestInteractiveTrainerInvalidCorrectedJQLCanRetryWithCorrection(t *testing.T) {
	path := t.TempDir() + "/feedback.jsonl"
	input := "original query\ncorrected\nstatus = Open AND\ncorrection\nI meant open issues assigned to me\nquit\n"
	translationCalls := 0
	translator := func(query string) (*llm.QueryResult, error) {
		translationCalls++
		return &llm.QueryResult{Input: query, Route: llm.RouteFastPath, JQL: "status = Open"}, nil
	}
	var output bytes.Buffer
	if err := runInteractiveTrainer(strings.NewReader(input), &output, path, "", "v", translator); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Corrected JQL is invalid") {
		t.Fatalf("expected validation diagnostic, got %s", output.String())
	}
	records, err := nlfeedback.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one record after retrying with natural-language correction, got %+v", records)
	}
	if records[0].Input != "original query" || records[0].Correction != "I meant open issues assigned to me" || records[0].CorrectedJQL != "" {
		t.Fatalf("retry was not recorded against the original query: %+v", records[0])
	}
	if translationCalls != 1 {
		t.Fatalf("translation calls = %d, want 1 for one reviewed input", translationCalls)
	}
	if err := jql.Validate("status = Open AND", jql.Options{}); err == nil {
		t.Fatal("test JQL unexpectedly valid")
	}
}

func TestTrainJQLReportUsesCustomPath(t *testing.T) {
	path := t.TempDir() + "/custom.jsonl"
	for _, record := range []nlfeedback.Record{
		{Input: "one", Route: "fast_path", Verdict: nlfeedback.VerdictGood},
		{Input: "two", Route: "fallback", Verdict: nlfeedback.VerdictRejected},
	} {
		if err := nlfeedback.Append(path, record); err != nil {
			t.Fatal(err)
		}
	}
	old := flagFeedbackFile
	flagFeedbackFile = path
	t.Cleanup(func() { flagFeedbackFile = old })
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runTrainJQLReport(cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Feedback records: 2", "fast_path: 1", "fallback: 1", "good: 1", "rejected: 1"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("report missing %q: %s", want, output.String())
		}
	}
}
