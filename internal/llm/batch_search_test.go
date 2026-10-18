package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

func TestReviewedBatchSearchShapesAndDefaultOrdering(t *testing.T) {
	fixture := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	const hostilePlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Done"}},"sort":[{"field":"created","direction":"ASC"}]}`
	const fallbackNoSort = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"statusCategory","operator":"!=","value":{"kind":"string","text":"Done"}},"sort":[]}`
	queries := []struct {
		name, input, normalized, where, sort string
		fallback                             bool
	}{
		{"summary text alias", "list my open items order by the summary text", "list my open items order by the summary text", `statusCategory != Done AND assignee = currentUser()`, "summary ASC", false},
		{"title alias", "list my open items ordered by title", "list my open items ordered by title", `statusCategory != Done AND assignee = currentUser()`, "summary ASC", false},
		{"text alternatives", "find my open work items that contain the words atlas, gpu or namespace and order by priority", "find my open work items that contain the words atlas, gpu or namespace and order by priority", `statusCategory != Done AND assignee = currentUser() AND text ~ "atlas OR gpu OR namespace"`, "priority DESC", false},
		{"text literals bypass repair", "lsit my opne items that contain the words opne, origianl or Namespace and order by priorty", "list my open items that contain the words opne, origianl or Namespace and order by priority", `statusCategory != Done AND assignee = currentUser() AND text ~ "opne OR origianl OR Namespace"`, "priority DESC", false},
		{"literal tense words stay text", "find my open items that contain the words Were, closed or gpu", "find my open items that contain the words Were, closed or gpu", `statusCategory != Done AND assignee = currentUser() AND text ~ "Were OR closed OR gpu"`, "updated DESC", false},
		{"epics", "find all my open work epics", "find all my open work epics", `issuetype = Epic AND statusCategory != Done AND assignee = currentUser()`, "updated DESC", false},
		{"children of epics", "find all of my open work items that are child tasks of an epic", "find all of my open work items that are child tasks of an epic", `statusCategory != Done AND assignee = currentUser() AND parent IS NOT EMPTY AND issuetype NOT IN subtaskIssueTypes()`, "updated DESC", false},
		{"implicit list sort", "my open items", "my open items", `statusCategory != Done AND assignee = currentUser()`, "updated DESC", false},
		{"explicit sort remains sole key", "my open items ordered by created ascending", "my open items ordered by created ascending", `statusCategory != Done AND assignee = currentUser()`, "created ASC", false},
		{"fallback list gets implicit sort", "retrieve all open work", "retrieve all open work", `statusCategory != Done`, "updated DESC", true},
		{"count mode has no implicit sort", "how many of my open items", "how many of my open items", `statusCategory != Done AND assignee = currentUser()`, "", false},
	}
	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			response := hostilePlan
			if tc.fallback {
				response = fallbackNoSort
			}
			provider := &fixtureResponseProvider{response: response}
			cfg := &config.Config{JiraProject: "PROJ"}
			result, err := GenerateJQLWithFixture(context.Background(), cfg, tc.input, fixture, provider)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.where + ` AND project = PROJ`
			if tc.sort != "" {
				want += " ORDER BY " + tc.sort
			}
			if result.JQL != want || result.Input != tc.input || result.NormalizedInput != tc.normalized {
				t.Fatalf("result=%+v; want JQL %q, input %q, normalized %q", result, want, tc.input, tc.normalized)
			}
			if tc.fallback {
				if result.Route != RouteFallback || result.ProviderCalls != 1 || provider.prompt == "" {
					t.Fatalf("fallback diagnostics=%+v prompt=%q", result, provider.prompt)
				}
			} else if result.Route != RouteFastPath || result.ProviderCalls != 0 || provider.prompt != "" {
				t.Fatalf("unexpected fallback: result=%+v prompt=%q", result, provider.prompt)
			}
			parsed, err := jql.Parse(result.JQL)
			if err != nil {
				t.Fatalf("generated JQL failed to parse: %v", err)
			}
			if !parsed.HasField("statusCategory") || strings.Contains(tc.input, "my ") && !parsed.HasField("assignee") || !parsed.HasProjectScope() {
				t.Fatalf("generated JQL has wrong open/owner/project shape: %s", result.JQL)
			}
			if tc.name == "count mode has no implicit sort" && result.Mode != "count" {
				t.Fatalf("count query mode=%q, want count", result.Mode)
			}
			if len(parsed.OrderBy) != strings.Count(tc.sort, ",")+boolToInt(tc.sort != "") {
				t.Fatalf("parsed sort shape=%+v; sort expression=%q", parsed.OrderBy, tc.sort)
			}
			if tc.name == "text alternatives" && !parsed.HasField("text") {
				t.Fatalf("text search predicate missing: %s", result.JQL)
			}
			if tc.name == "epics" && !parsed.HasField("issuetype") {
				t.Fatalf("epic issue type missing: %s", result.JQL)
			}
			if tc.name == "children of epics" && (!parsed.HasField("parent") || !parsed.HasField("issuetype")) {
				t.Fatalf("child-of-epic structure missing: %s", result.JQL)
			}
		})
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
