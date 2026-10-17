//go:build oracle

package llm

import (
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

// This test requires explicit oracle credentials/fixtures and is never part of
// the hermetic suite. Check Jira's errors and parsed scope/owner/order shape,
// rather than treating HTTP 200 as evidence of a correct translation.
func TestOracle_CreationSortWithoutDirection(t *testing.T) {
	client := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}
	for _, input := range []string{"my open issues ordered by date created", "list my open issues ordered by date created"} {
		t.Run(input, func(t *testing.T) {
			result, err := GenerateJQLWithProjects(t.Context(), cfg, input, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != RouteFastPath || result.ProviderCalls != 0 {
				t.Fatalf("expected deterministic translation: %+v", result)
			}
			parsed := assertValid(t, client, result.JQL)
			where := parsed.Structure.Where
			for _, field := range []string{"assignee", "project", "statusCategory"} {
				if where.CountFieldClauses(field) != 1 || !where.HasTopLevelFieldPredicate(field) {
					t.Errorf("lost top-level %s constraint: %+v", field, where)
				}
			}
			order := parsed.Structure.OrderBy
			if order == nil || len(order.Fields) != 1 || !strings.EqualFold(order.Fields[0].Field.Name, "created") || !strings.EqualFold(order.Fields[0].Direction, "asc") {
				t.Fatalf("unexpected parsed order: %+v", order)
			}
		})
	}
}
