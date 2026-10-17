package cmd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nlfeedback"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
	"github.com/spf13/cobra"
)

func TestTrainerAcceptsUnscopedCorrectionAsExactReviewData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"JIRA_SERVER", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_FIELD_ALIASES", "JIRA_PROJECT_ALIASES", "JIRA_PROJECT_KEYS"} {
		t.Setenv(name, "")
	}
	t.Setenv("JIRA_PROJECT", "PROJ")
	t.Setenv("LLM_PROVIDER", "must-not-be-called")
	oldProject, oldUser, oldFeedback := flagProject, flagUser, flagFeedbackFile
	oldTranslate, oldSearch, oldProjects := queryTranslator, querySearcher, queryProjectLoader
	t.Cleanup(func() {
		flagProject, flagUser, flagFeedbackFile = oldProject, oldUser, oldFeedback
		queryTranslator, querySearcher, queryProjectLoader = oldTranslate, oldSearch, oldProjects
	})
	flagProject, flagUser = "", ""
	flagFeedbackFile = filepath.Join(t.TempDir(), "feedback.jsonl")
	const original = "list all my open jiras ordered by updated date"
	const corrected = "  assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC  "
	calls := 0
	queryTranslator = func(_ context.Context, _ *config.Config, input string, _ []jira.Project) (*llm.QueryResult, error) {
		calls++
		if input != original {
			t.Errorf("translation received %q, want original wording", input)
		}
		return nil, translationerror.New(translationerror.Unsupported, errors.New("synthetic translation failure"))
	}
	querySearcher = func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
		t.Fatal("trainer executed a search or correction")
		return jira.SearchResult{}, nil
	}
	queryProjectLoader = func(context.Context, *config.Config, *cache.Cache) []jira.Project {
		t.Fatal("trainer fetched metadata")
		return nil
	}
	command := &cobra.Command{}
	command.SetIn(strings.NewReader(original + "\ncorrected\n" + corrected + "\n\nquit\n"))
	var output bytes.Buffer
	command.SetOut(&output)
	if err := runTrainJQL(command, nil); err != nil {
		t.Fatal(err)
	}
	records, err := nlfeedback.Read(flagFeedbackFile)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(records) != 1 {
		t.Fatalf("translation calls=%d records=%d", calls, len(records))
	}
	record := records[0]
	if record.Verdict != nlfeedback.VerdictCorrected || record.CorrectedJQL != corrected || record.Input != original || record.DefaultProject != "PROJ" || record.Outcome != "failure" || record.Replay != nil {
		t.Fatalf("correction changed scope/text or became execution data: %+v", record)
	}
	if strings.Contains(output.String(), "Corrected JQL is invalid") || !strings.Contains(output.String(), "Recorded.") {
		t.Fatalf("valid correction rejected: %s", output.String())
	}
	if err := jql.Validate(corrected, jql.Options{}); err != nil {
		t.Fatalf("fixture must be structurally valid: %v", err)
	}
	if err := jql.Validate(corrected, jql.Options{DefaultProject: "PROJ"}); err == nil {
		t.Fatal("execution project-scope validation was weakened")
	}
}

func TestTrainerRejectsMalformedCorrectionsWithReasonBeforeAcceptingRetry(t *testing.T) {
	for _, bad := range []string{" ", "ORDER BY updated DESC", "statusCategory != Done AND"} {
		t.Run(bad, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "feedback.jsonl")
			const corrected = "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"
			input := "original\ncorrected\n" + bad + "\ncorrected\n" + corrected + "\n\nquit\n"
			var output bytes.Buffer
			calls := 0
			err := runInteractiveTrainer(strings.NewReader(input), &output, path, "PROJ", "test", func(string) (*llm.QueryResult, error) { calls++; return nil, errors.New("synthetic failure") })
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(output.String(), "Corrected JQL is invalid: invalid JQL (rule ") || !strings.Contains(output.String(), "Recorded.") {
				t.Fatalf("retry/diagnostic failed: calls=%d output=%s", calls, output.String())
			}
			records, err := nlfeedback.Read(path)
			if err != nil || len(records) != 1 || records[0].CorrectedJQL != corrected || records[0].Verdict != nlfeedback.VerdictCorrected {
				t.Fatalf("valid retry not retained: records=%+v error=%v", records, err)
			}
		})
	}
}

type trainerNoFallback struct{ calls int }

func (p *trainerNoFallback) Generate(context.Context, string, string) (string, error) {
	p.calls++
	return "", errors.New("supported training input reached fallback")
}

func TestTrainerUpdatedDateReviewUsesProductionTranslationWithoutProvider(t *testing.T) {
	const input = "list all my open jiras ordered by updated date"
	const corrected = "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	provider := &trainerNoFallback{}
	fixture := llm.TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	translator := func(query string) (*llm.QueryResult, error) {
		return llm.GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, query, fixture, provider)
	}
	var output bytes.Buffer
	if err := runInteractiveTrainer(strings.NewReader(input+"\ncorrected\n"+corrected+"\n\nquit\n"), &output, path, "PROJ", "test", translator); err != nil {
		t.Fatal(err)
	}
	records, err := nlfeedback.Read(path)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	record := records[0]
	const generated = "statusCategory != Done AND assignee = currentUser() AND project = PROJ ORDER BY updated ASC"
	if provider.calls != 0 || record.ProviderCalls != 0 || record.GeneratedJQL != generated || record.Route != llm.RouteFastPath || record.Outcome != "success" || record.CorrectedJQL != corrected || record.Verdict != nlfeedback.VerdictCorrected || record.Replay != nil {
		t.Fatalf("training review changed meaning or executed correction: %+v calls=%d", record, provider.calls)
	}
	if strings.Contains(output.String(), "Translation failed") || strings.Contains(output.String(), "Corrected JQL is invalid") {
		t.Fatalf("reported failure on a valid review: %s", output.String())
	}
}
