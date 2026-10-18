package llm

import (
	"context"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

func TestCreationSortWithoutDirectionPreservesScopedOwnership(t *testing.T) {
	fixture := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	// If this query ever falls back again, the fixture reproduces the reported
	// valid-but-wrong output. A passing test must preserve the complete meaning,
	// keep its scope, and avoid the provider entirely.
	const wrongPlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"statusCategory","operator":"!=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	queries := []struct{ input, field, direction string }{
		{"my open issues ordered by date created", "created", "ASC"},
		{"list my open issues ordered by date created", "created", "ASC"},
		{"my open issues ordered by creation date", "created", "ASC"},
		{"my open issues ordered by created", "created", "ASC"},
		{"list all my open jiras ordered by updated date", "updated", "DESC"},
		{"list all my open jiras ordered by updated date descending", "updated", "DESC"},
	}
	for _, user := range []string{"", "reviewer"} {
		for _, query := range queries {
			input := query.input
			t.Run(input+"/"+user, func(t *testing.T) {
				provider := &fixtureResponseProvider{response: wrongPlan}
				cfg := &config.Config{JiraProject: "PROJ", JiraUser: user}
				result, err := GenerateJQLWithFixture(context.Background(), cfg, input, fixture, provider)
				if err != nil {
					t.Fatal(err)
				}
				want := `statusCategory != Done AND assignee = ` + cfg.Assignee() + ` AND project = PROJ ORDER BY ` + query.field + ` ` + query.direction
				if result.JQL != want || result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
					t.Fatalf("result=%+v prompt=%q; want %s without fallback", result, provider.prompt, want)
				}
				if result.Input != input || result.NormalizedInput != input {
					t.Fatalf("input diagnostics changed: %+v", result)
				}
			})
		}
	}
}
