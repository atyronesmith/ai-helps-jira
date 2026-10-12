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
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jqloracle"
)

// oracleProject mirrors internal/llm's constant of the same purpose: the
// one real, live project key this corpus scopes positive cases to, so
// strict validation's value-existence check has something true to check
// against. See JIRA_PROJECT in .env.
const oracleProject = "FIELDCTO"

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
		t.Errorf("expected %q to be valid, got errors: %v", jql, r.Errors)
	}
	return &r
}

// assertFieldRecognized is a weaker check than assertValid, needed for the
// SearchByRelease template below: this tenant has no fixVersion/release
// defined on FIELDCTO at all (verified live -- `fixVersion is not EMPTY`
// matches zero issues), so no literal release name can pass full strict
// validation without first mutating the tenant to create one, which this
// suite must not do. The next best live check is that the field name
// itself is recognised -- i.e. the only error is a value-existence error
// naming the field, never a field-existence error -- which still catches
// the hallucination mode this corpus cares about (a model inventing a
// field name) even though it cannot assert the release name is real.
func assertFieldRecognized(t *testing.T, c *jqloracle.Client, jql, field string) {
	t.Helper()
	results, err := c.ParseStrict([]string{jql})
	if err != nil {
		t.Fatalf("ParseStrict transport error: %v", err)
	}
	for _, e := range results[0].Errors {
		if strings.Contains(e, fmt.Sprintf("Field '%s' does not exist", field)) {
			t.Errorf("%q: field %q was not recognised by this tenant: %v", jql, field, e)
		}
	}
}

// TestOracle_WeeklyActivityJQL validates the shared weekly-status query
// (internal/jira/jql.go's WeeklyActivityJQL, used by both cmd/weekly_status.go
// and the MCP jira_weekly_status tool -- see FIELDCTO-2816, which replaced
// the nonexistent `updatedBy` field with this). It calls the real function,
// not a reconstruction, since it already lives in this package.
func TestOracle_WeeklyActivityJQL(t *testing.T) {
	c := oracleClient(t)

	jql, err := WeeklyActivityJQL(oracleProject, "currentUser()", "2026-07-01", "2026-07-31")
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

// TestOracle_WeeklyActivityJQL_InvalidEndDate locks in the error path: an
// unparseable end date must fail before ever reaching Jira (WeeklyActivityJQL
// itself rejects it), so there is nothing to submit to the oracle here --
// this documents that the input is validated locally, not remotely.
func TestOracle_WeeklyActivityJQL_InvalidEndDate(t *testing.T) {
	if _, err := WeeklyActivityJQL(oracleProject, "currentUser()", "2026-07-01", "not-a-date"); err == nil {
		t.Fatal("expected WeeklyActivityJQL to reject an invalid end date locally")
	}
}

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
					"AND status NOT IN (Done, Closed, Resolved) "+
					"ORDER BY priority ASC, status ASC",
				assignee, oracleProject,
			),
		},
		{
			"GetOpenIssuesSince",
			"internal/jira/client.go Client.GetOpenIssuesSince",
			fmt.Sprintf(
				"assignee = %s AND project = %s "+
					"AND status NOT IN (Done, Closed, Resolved) "+
					"AND updated >= \"%s\" "+
					"ORDER BY priority ASC, status ASC",
				assignee, oracleProject, time.Now().Add(-7*24*time.Hour).Format("2006-01-02 15:04"),
			),
		},
		{
			"GetSprintIssues (getSprintIssues)",
			"internal/jira/client.go Client.getSprintIssues",
			fmt.Sprintf(
				"assignee = %s AND sprint = %d AND project = %s ORDER BY priority ASC",
				assignee, 123, oracleProject,
			),
		},
		{
			"getKanbanIssues",
			"internal/jira/client.go Client.getKanbanIssues (note: no project predicate at all -- see report)",
			fmt.Sprintf(
				"assignee = %s AND status NOT IN (Done, Closed, Resolved) "+
					"ORDER BY priority ASC, status ASC",
				assignee,
			),
		},
		{
			"find-similar candidate fetch",
			"cmd/find_similar.go runFindSimilar and internal/mcpserver/tools.go find-similar handler (identical template)",
			fmt.Sprintf(
				"project = %s AND status NOT IN (Done, Closed, Resolved) ORDER BY updated DESC",
				oracleProject,
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("source: %s", tc.source)
			assertValid(t, c, tc.jql)
		})
	}

	// SearchByRelease is checked separately with the weaker
	// assertFieldRecognized: this tenant has no fixVersion/release defined
	// on FIELDCTO at all (confirmed live -- `fixVersion is not EMPTY`
	// matches zero issues), so no literal release name can satisfy full
	// strict validation here without mutating the tenant to create one.
	t.Run("SearchByRelease", func(t *testing.T) {
		t.Logf("source: internal/jira/client.go Client.SearchByRelease")
		jql := fmt.Sprintf(
			"fixVersion = %q AND project = %s ORDER BY issuetype ASC, priority ASC",
			"2026.1", oracleProject,
		)
		assertFieldRecognized(t, c, jql, "fixVersion")
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
	// FIELDCTO-2816 is a real issue (verified live), so this exercises a
	// genuine identity lookup rather than hitting "issue does not exist".
	jql := fmt.Sprintf("key = %s", oracleProject+"-2816")
	r := assertValid(t, c, jql)
	if r.Structure == nil || r.Structure.Where == nil {
		t.Fatalf("expected a parsed structure for %q", jql)
	}
	if r.Structure.Where.HasTopLevelFieldPredicate("project") {
		t.Errorf("did not expect a project predicate in a key-identity query %q", jql)
	}
}
