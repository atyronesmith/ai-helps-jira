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
func TestOracle_DateSortWithoutDirection(t *testing.T) {
	client := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}
	for _, tc := range []struct{ input, field, direction string }{
		{"my open issues ordered by date created", "created", "asc"},
		{"list my open issues ordered by date created", "created", "asc"},
		{"list all my open jiras ordered by updated date", "updated", "desc"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			result, err := GenerateJQLWithProjects(t.Context(), cfg, tc.input, nil)
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
			if order == nil || len(order.Fields) != 1 || !strings.EqualFold(order.Fields[0].Field.Name, tc.field) || !strings.EqualFold(order.Fields[0].Direction, tc.direction) {
				t.Fatalf("unexpected parsed order: %+v", order)
			}
		})
	}
}

func TestOracle_ReviewedOrdering(t *testing.T) {
	client := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}
	for _, tc := range []struct{ input, field string }{
		{"list my work items ordered by last updated", "updated"},
		{"list my work items in order of when last updated", "updated"},
		{"list my work items by original work estimate", "originalEstimate"},
		{"list my work items by the logged work time", "timespent"},
		{"list my work items by the amount of work remaining", "remainingEstimate"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: `{"version":1,"outcome":"unsupported"}`}
			result, err := GenerateJQLWithFixture(t.Context(), cfg, tc.input, TranslationFixture{}, provider)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("expected deterministic translation: %+v", result)
			}
			parsed := assertValid(t, client, result.JQL)
			where := parsed.Structure.Where
			for _, field := range []string{"assignee", "project"} {
				if where.CountFieldClauses(field) != 1 || !where.HasTopLevelFieldPredicate(field) {
					t.Errorf("lost top-level %s constraint: %+v", field, where)
				}
			}
			if where.CountFieldClauses("statusCategory") != 0 || where.CountFieldClauses("status") != 0 {
				t.Errorf("unrequested status constraint: %+v", where)
			}
			order := parsed.Structure.OrderBy
			if order == nil || len(order.Fields) != 1 || !strings.EqualFold(order.Fields[0].Field.Name, tc.field) || !strings.EqualFold(order.Fields[0].Direction, "desc") {
				t.Fatalf("unexpected parsed order: %+v", order)
			}
		})
	}
}

func TestOracle_ReviewedBatchSearches(t *testing.T) {
	client := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}
	for _, tc := range []struct {
		input, field, direction string
		predicates              []string
	}{
		{"list my open items order by the summary text", "summary", "asc", nil},
		{"find my open work items that contain the words atlas, gpu or namespace and order by priority", "priority", "desc", []string{"text"}},
		{"find all my open work epics", "updated", "desc", []string{"issuetype"}},
		{"find all of my open work items that are child tasks of an epic", "updated", "desc", []string{"parent", "issuetype"}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			provider := &fixtureResponseProvider{response: `{"version":1,"outcome":"unsupported"}`}
			result, err := GenerateJQLWithFixture(t.Context(), cfg, tc.input, TranslationFixture{}, provider)
			if err != nil {
				t.Fatal(err)
			}
			if result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("unexpected fallback: %+v", result)
			}
			parsed := assertValid(t, client, result.JQL)
			for _, field := range append([]string{"project", "assignee", "statusCategory"}, tc.predicates...) {
				if parsed.Structure.Where.CountFieldClauses(field) != 1 {
					t.Errorf("wrong %s predicate shape: %+v", field, parsed.Structure.Where)
				}
			}
			order := parsed.Structure.OrderBy
			if order == nil || len(order.Fields) != 1 || !strings.EqualFold(order.Fields[0].Field.Name, tc.field) || !strings.EqualFold(order.Fields[0].Direction, tc.direction) {
				t.Fatalf("unexpected order: %+v", order)
			}
		})
	}
}
