package llm

import (
	"context"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

func TestReviewedKeywordCaseAndMinimalRepairStayDeterministic(t *testing.T) {
	fixture := TranslationFixture{
		Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Metadata: TranslationMetadata{
			FetchedAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
			Fields:    []fieldmeta.Field{{ID: "customfield_10001", Name: "Story Points", Type: "number", Projects: []string{"PROJ"}, ApplicabilityKnown: true}},
		},
		ProjectMap: map[string]string{"PROJ": "PROJ"},
	}
	const hostilePlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	queries := []struct{ input, normalized, sort string }{
		{"LiSt mY OpEn ItEmS bY sToRy PoInTs", "LiSt mY OpEn ItEmS bY sToRy PoInTs", "cf[10001] DESC"},
		{"lsit my opne items by stroy pionts", "list my open items by story points", "cf[10001] DESC"},
		{"list my open items by origianl work estimate", "list my open items by original work estimate", "originalEstimate DESC"},
		{"list my open work items by the amount of work remaning", "list my open work items by the amount of work remaining", "remainingEstimate DESC"},
	}
	for _, tc := range queries {
		t.Run(tc.input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: hostilePlan}
			cfg := &config.Config{JiraProject: "PROJ", JiraUser: "reviewer@example.com"}
			result, err := GenerateJQLWithFixture(context.Background(), cfg, tc.input, fixture, provider)
			if err != nil {
				t.Fatal(err)
			}
			want := `statusCategory != Done AND assignee = "reviewer@example.com" AND project = PROJ ORDER BY ` + tc.sort
			if result.JQL != want || result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("result=%+v prompt=%q; want %s without fallback", result, provider.prompt, want)
			}
			if result.Input != tc.input || result.NormalizedInput != tc.normalized {
				t.Fatalf("input diagnostics=%+v; want original %q and normalized %q", result, tc.input, tc.normalized)
			}
		})
	}
}
