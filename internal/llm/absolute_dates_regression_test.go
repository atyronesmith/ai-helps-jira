package llm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

func TestAbsoluteDateRangesPreserveOwnershipScopeAndInclusiveEnd(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	fixture := TranslationFixture{
		Now:        now,
		ProjectMap: map[string]string{"PROJ": "PROJ"},
	}
	provider := &fakeJQLProvider{response: `{"version":1,"outcome":"unsupported"}`}
	cfg := &config.Config{JiraProject: "PROJ", JiraUser: "reviewer@example.test"}
	cases := []struct {
		name, input string
		want        string
		absent      []string
	}{
		{
			name:   "assigned to me with updated range",
			input:  "issues assigned to me updated between 2026-10-12 and 2026-10-16 in PROJ",
			want:   `project = PROJ AND assignee = "reviewer@example.test" AND updated >= "2026-10-12" AND updated < "2026-10-17" ORDER BY updated DESC`,
			absent: []string{"status =", "statusCategory"},
		},
		{
			name:   "were updated phrasing",
			input:  "issues assigned to me that were updated between 2026-10-12 and 2026-10-16 in PROJ",
			want:   `project = PROJ AND assignee = "reviewer@example.test" AND updated >= "2026-10-12" AND updated < "2026-10-17" ORDER BY updated DESC`,
			absent: []string{"status =", "statusCategory"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := GenerateJQLWithFixture(context.Background(), cfg, tc.input, fixture, provider)
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			if result.Input != tc.input {
				t.Errorf("original input = %q, want %q", result.Input, tc.input)
			}
			if result.Route != RouteFastPath || result.ProviderCalls != 0 {
				t.Errorf("translation route/calls = %q/%d, want fast_path/0", result.Route, result.ProviderCalls)
			}
			if result.JQL != tc.want {
				t.Errorf("JQL = %q, want %q", result.JQL, tc.want)
			}
			for _, absent := range tc.absent {
				if strings.Contains(result.JQL, absent) {
					t.Errorf("JQL %q unexpectedly contains %q", result.JQL, absent)
				}
			}
		})
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("provider called %d times for explicit date ranges, want 0", got)
	}
}

func TestSameDayAbsoluteRangeIncludesTheWholeDay(t *testing.T) {
	input := "my issues created between 2026-10-16 and 2026-10-16"
	fixture := TranslationFixture{
		Now:        time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC),
		ProjectMap: map[string]string{"PROJ": "PROJ"},
	}
	provider := &fakeJQLProvider{response: `{"version":1,"outcome":"unsupported"}`}
	result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, input, fixture, provider)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if result.Input != input {
		t.Errorf("original input = %q, want %q", result.Input, input)
	}
	want := `assignee = currentUser() AND created >= "2026-10-16" AND created < "2026-10-17" AND project = PROJ ORDER BY updated DESC`
	if result.JQL != want {
		t.Errorf("JQL = %q, want %q", result.JQL, want)
	}
	if result.ProviderCalls != 0 || atomic.LoadInt32(&provider.calls) != 0 {
		t.Fatalf("provider calls = result %d, provider %d; want zero", result.ProviderCalls, atomic.LoadInt32(&provider.calls))
	}
}

func TestAmbiguousWorkActivityDoesNotUseProviderFallback(t *testing.T) {
	input := "what jira items did I work on during Oct 12 - Oct 16"
	fixture := TranslationFixture{
		Now:        time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC),
		ProjectMap: map[string]string{"PROJ": "PROJ"},
	}
	provider := &fakeJQLProvider{response: `{"version":1,"outcome":"supported","mode":"list"}`}
	result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, input, fixture, provider)
	if err == nil {
		t.Fatal("ambiguous activity query unexpectedly translated")
	}
	if !errors.Is(err, nl2jql.ErrAmbiguousIntent) {
		t.Errorf("error = %v, want ErrAmbiguousIntent", err)
	}
	if result == nil || result.Input != input {
		t.Errorf("result did not preserve original input: %+v", result)
	}
	if result != nil && result.ProviderCalls != 0 {
		t.Errorf("reported provider calls = %d, want 0", result.ProviderCalls)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("provider called %d times for ambiguous activity, want 0", got)
	}
}
