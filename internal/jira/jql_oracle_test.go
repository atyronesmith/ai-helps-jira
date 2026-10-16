//go:build oracle

// Corpus test for FIELDCTO-2811, covering the hand-built fmt.Sprintf JQL
// templates that live in this package and in cmd/ and internal/mcpserver/
// (which this package cannot import without a cycle, so their shapes are
// reconstructed here verbatim -- see the comment on each case below for its
// source). See docs/jql-validation-plan.md and CONTRIBUTING.md.
//
// This file only compiles under -tags=oracle and every test here needs a
// live Jira instance (JIRA_SERVER / JIRA_EMAIL / JIRA_API_TOKEN).
package jira

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/jqloracle"
)

// oracleProject requires an explicitly configured, real tenant project. Oracle
// fixtures must never depend on a developer's local tenant or load .env files.
func oracleProject(t *testing.T) string {
	t.Helper()
	project := os.Getenv("JIRA_PROJECT")
	if project == "" {
		t.Skip("oracle SKIPPED: JIRA_PROJECT must name a real tenant project")
	}
	return project
}

func oracleClient(t *testing.T) *jqloracle.Client {
	t.Helper()
	c, err := jqloracle.NewClientFromEnv()
	if err != nil {
		t.Skipf("skipping oracle test: %v", err)
	}
	return c
}

func assertValid(t *testing.T, c *jqloracle.Client, jql string) *jqloracle.QueryResult {
	t.Helper()
	results, err := c.ParseStrict([]string{jql})
	if err != nil {
		t.Fatalf("ParseStrict transport error: %v", err)
	}
	r := results[0]
	if len(r.Errors) > 0 {
		t.Fatalf("expected %q to be valid, got errors: %v", jql, r.Errors)
	}
	if r.Structure == nil || r.Structure.Where == nil {
		t.Fatalf("expected a parsed WHERE structure for %q", jql)
	}
	return &r
}

// oracleFixture reads only explicitly supplied non-secret tenant fixture values.
func oracleFixture(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Skipf("oracle SKIPPED: %s must name a real tenant fixture", name)
	}
	return value
}

// TestOracle_WeeklyActivityJQL validates the shared weekly-status query
// (internal/jira/jql.go's WeeklyActivityJQL, used by both cmd/weekly_status.go
// and the MCP jira_weekly_status tool -- see FIELDCTO-2816, which replaced
// the nonexistent `updatedBy` field with this). It calls the real function,
// not a reconstruction, since it already lives in this package.
func TestOracle_WeeklyActivityJQL(t *testing.T) {
	c := oracleClient(t)

	jql, err := WeeklyActivityJQL(oracleProject(t), "currentUser()", "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("WeeklyActivityJQL: %v", err)
	}
	r := assertValid(t, c, jql)

	if r.Structure == nil || r.Structure.Where == nil {
		t.Fatalf("expected a parsed structure for %q", jql)
	}
	if r.Structure.Where.Operator != "and" {
		t.Errorf("top-level operator = %q, want \"and\"", r.Structure.Where.Operator)
	}
	if !r.Structure.Where.HasTopLevelFieldPredicate("project") {
		t.Errorf("no top-level project predicate in %q", jql)
	}
	if r.Structure.OrderBy == nil || len(r.Structure.OrderBy.Fields) == 0 {
		t.Errorf("expected an ORDER BY clause in %q", jql)
	}
}

// Local date rejection is covered by TestWeeklyActivityJQLRejectsBadEndDate
// in the ordinary suite; only tests that contact Jira use TestOracle_ names.

// TestOracle_HandBuiltTemplates reconstructs every remaining fmt.Sprintf
// JQL template in the tree (see docs/jql-validation-plan.md's inventory,
// regenerated via `grep -rn 'project = %s\|key = %s\|assignee = %s\|fixVersion
// = %q' --include='*.go' cmd internal | grep -v _test` rather than trusted
// from the ticket) and validates the rendered JQL. The cmd/ and
// internal/mcpserver/ sites are reconstructed verbatim as literals rather
// than invoked directly, because calling them would require a live
// *jira.Client doing a real search, which is out of scope for a parser
// check; the shape is what is under test, not the plumbing around it.
func TestOracle_HandBuiltTemplates(t *testing.T) {
	c := oracleClient(t)
	const assignee = "currentUser()"
	project := jql.QuoteIdent(oracleProject(t))
	sprintID, err := strconv.Atoi(oracleFixture(t, "JIRA_ORACLE_SPRINT_ID"))
	if err != nil || sprintID <= 0 {
		t.Fatal("JIRA_ORACLE_SPRINT_ID must be a positive integer")
	}

	cases := []struct {
		name   string
		source string
		jql    string
	}{
		{
			"GetOpenIssues",
			"internal/jira/client.go Client.GetOpenIssues",
			fmt.Sprintf(
				"assignee = %s AND project = %s "+
					"AND statusCategory != Done "+
					"ORDER BY priority ASC, status ASC",
				assignee, project,
			),
		},
		{
			"GetOpenIssuesSince",
			"internal/jira/client.go Client.GetOpenIssuesSince",
			fmt.Sprintf(
				"assignee = %s AND project = %s "+
					"AND statusCategory != Done "+
					"AND updated >= \"%s\" "+
					"ORDER BY priority ASC, status ASC",
				assignee, project, "2026-07-01 00:00",
			),
		},
		{
			"GetSprintIssues (getSprintIssues)",
			"internal/jira/client.go Client.getSprintIssues",
			fmt.Sprintf(
				"assignee = %s AND sprint = %d AND project = %s AND statusCategory != Done ORDER BY priority ASC",
				assignee, sprintID, project,
			),
		},
		{
			"getKanbanIssues",
			"internal/jira/client.go Client.getKanbanIssues (note: no project predicate at all -- see report)",
			fmt.Sprintf(
				"assignee = %s AND statusCategory != Done "+
					"ORDER BY priority ASC, status ASC",
				assignee,
			),
		},
		{
			"summary open-work snapshot",
			"internal/jira/summary.go Client.FetchSummary",
			fmt.Sprintf("project = %s AND assignee = %s AND statusCategory != Done ORDER BY priority ASC, status ASC", project, assignee),
		},
		{
			"summary sprint membership",
			"internal/jira/summary.go Client.fetchSummaryBoards",
			fmt.Sprintf("project = %s AND assignee = %s AND sprint = %d AND statusCategory != Done ORDER BY priority ASC", project, assignee, sprintID),
		},
		{
			"find-similar candidate fetch",
			"cmd/find_similar.go runFindSimilar and internal/mcpserver/tools.go find-similar handler (identical template)",
			fmt.Sprintf(
				"project = %s AND statusCategory != Done ORDER BY updated DESC",
				project,
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("source: %s", tc.source)
			assertValid(t, c, tc.jql)
		})
	}

	// A real release fixture lets this case assert strict validity and parsed
	// shape, rather than accepting every error except an unknown-field error.
	t.Run("SearchByRelease", func(t *testing.T) {
		t.Logf("source: internal/jira/client.go Client.SearchByRelease")
		query := fmt.Sprintf(
			"fixVersion = %s AND project = %s ORDER BY issuetype ASC, priority ASC",
			jql.Quote(oracleFixture(t, "JIRA_ORACLE_RELEASE")), project,
		)
		r := assertValid(t, c, query)
		if !r.Structure.Where.HasTopLevelFieldPredicate("fixVersion") ||
			!r.Structure.Where.HasTopLevelFieldPredicate("project") {
			t.Fatalf("expected release and project predicates in %q", query)
		}
	})
}

// TestOracle_CrudRefetch_KeyIsIdentityScoped validates the post-mutation
// refetch in internal/mcpserver/crud_tools.go
// (`client.SearchJQL(fmt.Sprintf("key = %s", key), 1)`), the one hand-built
// site that intentionally carries no project predicate: an issue key is
// globally unique, so internal/jql.Validate's rule 5 exempts key/id-only
// queries from the scope invariant rather than requiring (and failing) a
// project predicate here.
func TestOracle_CrudRefetch_KeyIsIdentityScoped(t *testing.T) {
	c := oracleClient(t)
	// Strict parsing checks value existence, so use a configured visible issue.
	query := fmt.Sprintf("key = %s", jql.QuoteIdent(oracleFixture(t, "JIRA_ORACLE_ISSUE_KEY")))
	r := assertValid(t, c, query)
	if r.Structure == nil || r.Structure.Where == nil {
		t.Fatalf("expected a parsed structure for %q", query)
	}
	if !r.Structure.Where.HasTopLevelFieldPredicate("key") && !r.Structure.Where.HasTopLevelFieldPredicate("issuekey") {
		t.Fatalf("expected a key predicate in %q", query)
	}
	if r.Structure.Where.HasTopLevelFieldPredicate("project") {
		t.Errorf("did not expect a project predicate in a key-identity query %q", query)
	}
}
