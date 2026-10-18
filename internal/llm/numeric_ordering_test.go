package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

func TestReviewedNumericOrderingAliasesPreserveScopeAndOverride(t *testing.T) {
	fixture := TranslationFixture{
		Now:        time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Metadata:   TranslationMetadata{FetchedAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), Fields: []fieldmeta.Field{{ID: "customfield_10001", Name: "Story Points", Type: "number", Projects: []string{"PROJ"}, ApplicabilityKnown: true}}},
		ProjectMap: map[string]string{"PROJ": "PROJ"},
	}
	const hostilePlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	queries := []struct{ input, sort, normalized string }{
		{"list my open items by Story Points", "cf[10001] DESC", "list my open items by Story Points"},
		{"list my open items by original work estimate", "originalEstimate DESC", "list my open items by original work estimate"},
		{"ist my open work items by the logged work time", "timespent DESC", "list my open work items by the logged work time"},
		{"list my open work items by the amount of work remaining", "remainingEstimate DESC", "list my open work items by the amount of work remaining"},
		{"list my open items by Story Points ascending", "cf[10001] ASC", "list my open items by Story Points ascending"},
		{"list my open work items by original work estimate ascending", "originalEstimate ASC", "list my open work items by original work estimate ascending"},
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

func TestReviewedStoryPointsOrderingRequiresApplicableNumericMetadata(t *testing.T) {
	base := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	for _, tc := range []struct {
		name     string
		metadata TranslationMetadata
	}{
		{name: "missing"},
		{name: "wrong type", metadata: TranslationMetadata{Fields: []fieldmeta.Field{{ID: "customfield_10001", Name: "Story Points", Type: "string", Projects: []string{"PROJ"}, ApplicabilityKnown: true}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := base
			fixture.Metadata = tc.metadata
			provider := &fixtureResponseProvider{response: `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Done"}},"sort":[]}`}
			result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "PROJ"}, "list my open items by Story Points", fixture, provider)
			if !errors.Is(err, fieldmeta.ErrMissingMetadata) {
				t.Fatalf("result=%+v err=%v; want ErrMissingMetadata", result, err)
			}
			if provider.prompt != "" || result != nil && result.ProviderCalls != 0 {
				t.Fatalf("provider called for invalid field metadata: result=%+v prompt=%q", result, provider.prompt)
			}
		})
	}
}
