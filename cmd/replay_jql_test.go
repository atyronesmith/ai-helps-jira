package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nlfeedback"
)

func TestReplayCommandUsesProductionFixturePathWithoutJiraOrLiveConfig(t *testing.T) {
	oldTranslate, oldSearch, oldProjects := queryTranslator, querySearcher, queryProjectLoader
	t.Cleanup(func() { queryTranslator, querySearcher, queryProjectLoader = oldTranslate, oldSearch, oldProjects })
	queryTranslator = func(context.Context, *config.Config, string, []jira.Project) (*llm.QueryResult, error) {
		t.Fatal("replay called the live translator adapter")
		return nil, nil
	}
	querySearcher = func(context.Context, *config.Config, string, jira.SearchOptions) (jira.SearchResult, error) {
		t.Fatal("replay searched Jira")
		return jira.SearchResult{}, nil
	}
	queryProjectLoader = func(context.Context, *config.Config, *cache.Cache) []jira.Project {
		t.Fatal("replay fetched project metadata")
		return nil
	}
	for _, key := range []string{"JIRA_PROJECT", "JIRA_SERVER", "JIRA_EMAIL", "JIRA_API_TOKEN", "LLM_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("JIRA_FIELD_ALIASES", "invalid")
	t.Setenv("LLM_PROVIDER", "must-not-use")
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runReplayJQL(cmd, []string{"../internal/nlfeedback/testdata/regressions.jsonl"}); err != nil {
		t.Fatal(err)
	}
	var report nlfeedback.ReplayReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Passed != 20 || report.Total != 20 {
		t.Fatalf("unexpected replay: %+v", report)
	}
}

func TestReplayCommandFailsForRegressionUnreviewedMissingAndEmpty(t *testing.T) {
	records, err := nlfeedback.Read("../internal/nlfeedback/testdata/regressions.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*nlfeedback.Record)
	}{
		{"regression", func(r *nlfeedback.Record) { r.Replay.Expect.JQL = "project = WRONG" }},
		{"unreviewed", func(r *nlfeedback.Record) { r.Replay = nil }},
		{"missing clock", func(r *nlfeedback.Record) { r.Fixture = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Roundtrip makes the pointer fields independent for each mutation.
			data, _ := json.Marshal(records[0])
			var record nlfeedback.Record
			_ = json.Unmarshal(data, &record)
			tc.edit(&record)
			data, _ = json.Marshal(record)
			path := filepath.Join(t.TempDir(), "corpus.jsonl")
			if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := runReplayJQL(cmd, []string{path}); err == nil || !strings.Contains(err.Error(), "corpus replay failed") {
				t.Fatalf("regression did not fail: %v", err)
			}
			var report nlfeedback.ReplayReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Total != 1 || report.Passed != 0 {
				t.Fatalf("failure report missing: %s, %v", out.String(), err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	cmd := &cobra.Command{}
	if err := runReplayJQL(cmd, []string{path}); err == nil {
		t.Fatal("missing corpus passed")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runReplayJQL(cmd, []string{path}); err == nil {
		t.Fatal("empty corpus passed")
	}
}

func TestTrainerRefusesTrackableOutputBeforeTranslation(t *testing.T) {
	old := flagFeedbackFile
	flagFeedbackFile = "private-corpus-guard-test.data"
	t.Cleanup(func() { flagFeedbackFile = old })
	err := runTrainJQL(&cobra.Command{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not ignored by Git") {
		t.Fatalf("trainer did not refuse trackable output before loading configuration: %v", err)
	}
}
