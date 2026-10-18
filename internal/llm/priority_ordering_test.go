package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

func TestReviewedPriorityAlternativesAndOrderingStayDistinct(t *testing.T) {
	fixture := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	const hostilePlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	queries := []struct {
		name, input, normalized, owner, filter, sort string
	}{
		{
			name:       "corrected query with filter and two sort keys",
			input:      "list my open work items where the priority is either major or minor and order by priority, then updated",
			normalized: "list my open work items where the priority is either major or minor and order by priority, then updated",
			owner:      "assignee = currentUser()",
			filter:     "priority IN (Major, Minor)",
			sort:       "priority DESC, updated DESC",
		},
		{
			name:       "original query sorts only by updated",
			input:      "list my open work items where the priority is either major or minor and order by updated",
			normalized: "list my open work items where the priority is either major or minor and order by updated",
			owner:      "assignee = currentUser()",
			filter:     "priority IN (Major, Minor)",
			sort:       "updated DESC",
		},
		{
			name:       "mixed case",
			input:      "LiSt mY OpEn WoRk ItEmS WhErE tHe PrIoRiTy Is EiThEr MaJoR oR MiNoR aNd OrDeR bY PrIoRiTy, ThEn UpDaTeD",
			normalized: "LiSt mY OpEn WoRk ItEmS WhErE tHe PrIoRiTy Is EiThEr MaJoR oR MiNoR aNd OrDeR bY PrIoRiTy, ThEn UpDaTeD",
			owner:      "assignee = currentUser()",
			filter:     "priority IN (Major, Minor)",
			sort:       "priority DESC, updated DESC",
		},
		{
			name:       "explicit ascending overrides defaults",
			input:      "list my open work items where priority is either major or minor order by priority ascending, then updated ascending",
			normalized: "list my open work items where priority is either major or minor order by priority ascending, then updated ascending",
			owner:      "assignee = currentUser()",
			filter:     "priority IN (Major, Minor)",
			sort:       "priority ASC, updated ASC",
		},
		{
			name:       "configured user override",
			input:      "list my open work items where priority is either major or minor and order by priority, then updated",
			normalized: "list my open work items where priority is either major or minor and order by priority, then updated",
			owner:      `assignee = "reviewer@example.com"`,
			filter:     "priority IN (Major, Minor)",
			sort:       "priority DESC, updated DESC",
		},
		{
			name:       "priority filter uses default updated ordering",
			input:      "list my open work items where priority is either major or minor",
			normalized: "list my open work items where priority is either major or minor",
			owner:      "assignee = currentUser()",
			filter:     "priority IN (Major, Minor)",
			sort:       "updated DESC",
		},
	}
	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: hostilePlan}
			cfg := &config.Config{JiraProject: "PROJ"}
			if tc.name == "configured user override" {
				cfg.JiraUser = "reviewer@example.com"
			}
			result, err := GenerateJQLWithFixture(context.Background(), cfg, tc.input, fixture, provider)
			if err != nil {
				t.Fatal(err)
			}
			want := `statusCategory != Done AND ` + tc.owner
			if tc.filter != "" {
				want += " AND " + tc.filter
			}
			want += " AND project = PROJ"
			if tc.sort != "" {
				want += " ORDER BY " + tc.sort
			}
			if result.JQL != want || result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("result=%+v prompt=%q; want %s without fallback", result, provider.prompt, want)
			}
			if result.Input != tc.input || result.NormalizedInput != tc.normalized {
				t.Fatalf("input diagnostics=%+v; want original %q and normalized %q", result, tc.input, tc.normalized)
			}
		})
	}
}

func TestIncompletePriorityAlternativesRequireClarification(t *testing.T) {
	for _, input := range []string{
		"list my items where priority is either major",
		"list my items where priority is either major and minor",
		"list my items where not priority is either major or minor",
	} {
		t.Run(input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: `{"version":1,"outcome":"unsupported"}`}
			fixture := TranslationFixture{Now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
			result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, input, fixture, provider)
			if !errors.Is(err, nl2jql.ErrAmbiguousIntent) || provider.prompt != "" || result != nil && result.ProviderCalls != 0 {
				t.Fatalf("result=%+v err=%v prompt=%q; want clarification without a provider", result, err, provider.prompt)
			}
		})
	}
}
