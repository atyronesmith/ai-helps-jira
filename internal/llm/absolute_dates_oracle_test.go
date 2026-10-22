//go:build oracle

package llm

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

func TestOracle_ExplicitAbsoluteDateRanges(t *testing.T) {
	client := oracleClient(t)
	project := oracleProject(t)
	fixture := TranslationFixture{
		Now:        time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC),
		ProjectMap: map[string]string{project: project},
	}
	cases := []struct {
		name, query, dateField string
	}{
		{
			name:      "updated range ends exclusively on next day",
			query:     "issues assigned to me updated between 2026-10-12 and 2026-10-16 in " + project,
			dateField: "updated",
		},
		{
			name:      "created range retains caller ownership",
			query:     "my issues created between 2026-10-12 and 2026-10-16",
			dateField: "created",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeJQLProvider{response: `{"version":1,"outcome":"unsupported"}`}
			translated, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: project}, tc.query, fixture, provider)
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			if translated.Route != RouteFastPath || translated.ProviderCalls != 0 || atomic.LoadInt32(&provider.calls) != 0 {
				t.Fatal("explicit date ranges must translate deterministically without any provider call")
			}
			parsed := assertValid(t, client, translated.JQL)
			where := parsed.Structure.Where
			if !where.IsCompound() || !strings.EqualFold(where.Operator, "and") {
				t.Errorf("top-level WHERE operator = %q, want AND", where.Operator)
			}
			if !where.HasTopLevelFieldPredicate("project") || !where.HasTopLevelFieldPredicate("assignee") {
				t.Errorf("project and owner must be top-level predicates: %+v", where)
			}
			if where.CountFieldClauses(tc.dateField) != 2 {
				t.Errorf("date range has %d %s predicates, want 2", where.CountFieldClauses(tc.dateField), tc.dateField)
			}
			if where.CountFieldClauses("status") != 0 || where.CountFieldClauses("statusCategory") != 0 {
				t.Errorf("range query unexpectedly restricts status: %+v", where)
			}
			if parsed.Structure.OrderBy == nil || len(parsed.Structure.OrderBy.Fields) != 1 ||
				!strings.EqualFold(parsed.Structure.OrderBy.Fields[0].Field.Name, "updated") ||
				!strings.EqualFold(parsed.Structure.OrderBy.Fields[0].Direction, "DESC") {
				t.Errorf("default ordering = %+v, want updated DESC", parsed.Structure.OrderBy)
			}
		})
	}
}
