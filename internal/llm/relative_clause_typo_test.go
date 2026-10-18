package llm

import (
	"context"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

func TestRelativePriorityTypoStaysOnDeterministicPath(t *testing.T) {
	const input = "list all my open jiras thare are priority critical and order by priority"
	const normalized = "list all my open jiras that are priority critical and order by priority"
	const hostilePlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	fixture := TranslationFixture{
		Now:        time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		ProjectMap: map[string]string{"PROJ": "PROJ"},
	}
	provider := &fixtureResponseProvider{response: hostilePlan}
	cfg := &config.Config{JiraProject: "PROJ"}
	result, err := GenerateJQLWithFixture(context.Background(), cfg, input, fixture, provider)
	if err != nil {
		t.Fatal(err)
	}
	want := `statusCategory != Done AND assignee = currentUser() AND priority = Critical AND project = PROJ ORDER BY priority DESC`
	if result.JQL != want || result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
		t.Fatalf("result=%+v prompt=%q; want %q without fallback", result, provider.prompt, want)
	}
	if result.Input != input || result.NormalizedInput != normalized {
		t.Fatalf("diagnostics=%+v; want original %q and normalized %q", result, input, normalized)
	}
}
