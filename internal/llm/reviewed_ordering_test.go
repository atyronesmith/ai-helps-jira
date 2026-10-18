package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

func TestReviewedLastUpdatedOrderingPreservesOwnershipAndPriority(t *testing.T) {
	fixture := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	const wrongPlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"statusCategory","operator":"!=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	queries := []struct{ input, filter, sort string }{
		{"list my work items ordered by last updated", `assignee = "reviewer@example.com"`, "updated DESC"},
		{"list my work items in order of when last updated", `assignee = "reviewer@example.com"`, "updated DESC"},
		{"list my Critical work items ordered by last updated", `assignee = "reviewer@example.com" AND priority = Critical`, "updated DESC"},
		{"list my work items ordered by last updated ascending", `assignee = "reviewer@example.com"`, "updated ASC"},
		{"list my work items ordered by updated date", `assignee = "reviewer@example.com"`, "updated DESC"},
	}
	for _, tc := range queries {
		t.Run(tc.input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: wrongPlan}
			cfg := &config.Config{JiraProject: "PROJ", JiraUser: "reviewer@example.com"}
			result, err := GenerateJQLWithFixture(context.Background(), cfg, tc.input, fixture, provider)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.filter + ` AND project = PROJ ORDER BY ` + tc.sort
			if result.JQL != want || result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("result=%+v prompt=%q; want %s without fallback", result, provider.prompt, want)
			}
			if result.Input != tc.input || result.NormalizedInput != tc.input {
				t.Fatalf("input diagnostics changed: %+v", result)
			}
		})
	}
}

func TestReviewedChronologicalOrderingUsesDateFieldsAndRejectsAmbiguity(t *testing.T) {
	fixture := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	const wrongPlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"statusCategory","operator":"!=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	queries := []struct{ input, sort string }{
		{"list my work items ordered by created from oldest to newest", "created ASC"},
		{"list my work items ordered by last updated from oldest to newest", "updated ASC"},
		{"list my work items ordered by due date from newest to oldest", "due DESC"},
		{"list my work items ordered by resolution date oldest to newest", "resolved ASC"},
	}
	for _, tc := range queries {
		t.Run(tc.input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: wrongPlan}
			result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, tc.input, fixture, provider)
			if err != nil {
				t.Fatal(err)
			}
			want := `assignee = currentUser() AND project = PROJ ORDER BY ` + tc.sort
			if result.JQL != want || result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("result=%+v prompt=%q; want %s without fallback", result, provider.prompt, want)
			}
			if result.Input != tc.input || result.NormalizedInput != tc.input {
				t.Fatalf("input diagnostics changed: %+v", result)
			}
		})
	}
	for _, input := range []string{
		"list my work items from oldest to newest",
		"list my work items ordered by summary from oldest to newest",
	} {
		t.Run(input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: wrongPlan}
			result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, input, fixture, provider)
			if !errors.Is(err, nl2jql.ErrAmbiguousIntent) {
				t.Fatalf("result=%+v; want ambiguous unsupported intent", result)
			}
			if provider.prompt != "" || result != nil && result.ProviderCalls != 0 {
				t.Fatalf("provider called for ambiguous ordering: result=%+v prompt=%q", result, provider.prompt)
			}
		})
	}
}
