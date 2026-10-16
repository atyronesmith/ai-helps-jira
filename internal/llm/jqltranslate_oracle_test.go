//go:build oracle

// Corpus test for FIELDCTO-2811: validate every JQL shape the nl2jql fast
// path and ensureProjectScope can emit against Jira's own parser,
// POST /rest/api/3/jql/parse?validation=strict. See
// docs/jql-validation-plan.md and CONTRIBUTING.md ("Running the JQL
// oracle suite") for why this exists and how to run it.
//
// This file only compiles under -tags=oracle and every test here needs a
// live Jira instance (JIRA_SERVER / JIRA_EMAIL / JIRA_API_TOKEN). The
// default `go test ./...` never sees this file.
package llm

import (
	"os"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/jqloracle"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
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

// oracleClient builds the live Jira client once per test, skipping (not
// failing) when credentials are not exported. That keeps `go test
// -tags=oracle ./...` runnable locally without credentials -- it just
// skips -- while CI, which does export them, gets full coverage.
func oracleClient(t *testing.T) *jqloracle.Client {
	t.Helper()
	c, err := jqloracle.NewClientFromEnv()
	if err != nil {
		t.Skipf("skipping oracle test: %v", err)
	}
	return c
}

// assertValid posts jql and fails the test with Jira's own error list if
// strict validation rejects it. It never asserts on HTTP status -- the
// endpoint returns 200 either way -- only on errors[].
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

// assertRejected posts jql and fails the test if strict validation does
// NOT report at least one error. Used for the hallucination negatives and
// syntactically broken queries.
func assertRejected(t *testing.T, c *jqloracle.Client, jql string) {
	t.Helper()
	results, err := c.ParseStrict([]string{jql})
	if err != nil {
		t.Fatalf("ParseStrict transport error: %v", err)
	}
	if len(results[0].Errors) == 0 {
		t.Errorf("expected %q to be rejected by strict validation, got no errors", jql)
	}
}

// assertScopedCorrectly is the FIELDCTO-2807 structural assertion: the
// oracle must report zero errors, a top-level "and" operator, and a
// project predicate that sits directly at that top level (not buried
// inside a parenthesised OR branch it was supposed to scope). This is the
// check that actually catches the regression -- the pre-fix query is
// syntactically valid JQL with no errors[] at all, so only the parsed
// structure can tell the bug apart from correct output.
func assertScopedCorrectly(t *testing.T, c *jqloracle.Client, jql string) {
	t.Helper()
	r := assertValid(t, c, jql)
	if r.Structure == nil || r.Structure.Where == nil {
		t.Fatalf("expected a parsed structure for %q, got none", jql)
	}
	where := r.Structure.Where
	if where.Operator != "and" && where.IsCompound() {
		t.Errorf("%q: top-level where operator = %q, want \"and\" (a bare project predicate with no other AND-ed clause legitimately has no top-level operator)", jql, where.Operator)
	}
	if !where.HasTopLevelFieldPredicate("project") {
		t.Errorf("%q: no project predicate at the top level of the where-clause -- the OR precedence bug (FIELDCTO-2807) strands it under one branch instead", jql)
	}
}

// TestOracle_EnsureProjectScope_FIELDCTO2807 runs ensureProjectScope's own
// regression fixtures (the OR-precedence and ORDER BY placement cases)
// through the live oracle. It calls the real function, not a hardcoded
// string, so reverting the FIELDCTO-2807 fix in jqltranslate.go changes
// what this test submits and breaks the structural assertion above --
// verified manually by temporarily reverting ensureProjectScope's OR
// parenthesisation and confirming `go test -tags=oracle` goes red (see
// CONTRIBUTING.md).
func TestOracle_EnsureProjectScope_FIELDCTO2807(t *testing.T) {
	c := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}

	cases := []struct {
		name string
		raw  string
	}{
		{"top-level OR gets parenthesised before scoping", "issuetype = Bug OR issuetype = Story"},
		{"OR plus trailing ORDER BY", "issuetype = Bug OR issuetype = Story ORDER BY priority ASC"},
		{"existing parenthesised OR is untouched", "status = Open AND (priority = High OR priority = Highest)"},
		{"predicate inserted before ORDER BY, no OR involved", "status = Closed ORDER BY created DESC"},
		{"sort-only query with no where-clause", "ORDER BY created DESC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scoped, err := ensureProjectScope(tc.raw, cfg)
			if err != nil {
				t.Fatalf("ensureProjectScope: %v", err)
			}
			assertScopedCorrectly(t, c, scoped)
		})
	}
}

// TestOracle_KnownBadScoping_FIELDCTO2807 submits the pre-fix shapes
// directly -- what ensureProjectScope used to produce by naive string
// concatenation -- as negative cases. They are valid, error-free JQL (Jira
// has no opinion on precedence intent), so the only way to catch them is
// the same structural check: this asserts the known-bad forms FAIL it,
// proving the check has teeth rather than trivially passing everything.
func TestOracle_KnownBadScoping_FIELDCTO2807(t *testing.T) {
	c := oracleClient(t)

	badForms := []string{
		// Unparenthesised OR + appended project scope: AND binds tighter
		// than OR, so this parses as
		// "issuetype = Bug OR (issuetype = Story AND project = PROJ)" --
		// the first branch escapes the scope entirely.
		"issuetype = Bug OR issuetype = Story AND project = " + jql.QuoteIdent(oracleProject(t)),
		"status = Open OR priority = High AND project = " + jql.QuoteIdent(oracleProject(t)),
	}
	for _, bad := range badForms {
		t.Run(bad, func(t *testing.T) {
			results, err := c.ParseStrict([]string{bad})
			if err != nil {
				t.Fatalf("ParseStrict transport error: %v", err)
			}
			r := results[0]
			if len(r.Errors) != 0 {
				t.Fatalf("known-bad form %q unexpectedly has syntax errors: %v (expected it to be syntactically VALID but wrongly scoped)", bad, r.Errors)
			}
			if r.Structure == nil || r.Structure.Where == nil {
				t.Fatalf("expected a parsed structure for %q", bad)
			}
			where := r.Structure.Where
			scopedCorrectly := where.Operator == "and" && where.HasTopLevelFieldPredicate("project")
			if scopedCorrectly {
				t.Errorf("known-bad form %q was scored as correctly scoped (operator=%q); the structural check would miss this regression", bad, where.Operator)
			}
		})
	}
}

// TestOracle_KnownBadOrderByPlacement_FIELDCTO2807 covers the other half of
// the 2807 defect: appending a predicate *after* an existing ORDER BY is a
// syntax error Jira rejects outright (HTTP 200, but errors[] populated),
// unlike the OR case above which is syntactically valid but wrongly scoped.
func TestOracle_KnownBadOrderByPlacement_FIELDCTO2807(t *testing.T) {
	c := oracleClient(t)
	assertRejected(t, c, "status = Closed ORDER BY created DESC AND project = "+jql.QuoteIdent(oracleProject(t)))
}

// TestOracle_FastPath_FIELDCTO2808 covers the same-field collapsing fix:
// "bugs and stories" must render as a single `issuetype IN (...)` clause,
// never two AND-ed equalities on the same field (which can never match
// anything). It calls the real nl2jql parser, not a hardcoded string, so a
// regression that reintroduces the AND-ing bug changes what reaches the
// oracle and fails the clause-count assertion.
func TestOracle_FastPath_FIELDCTO2808(t *testing.T) {
	c := oracleClient(t)

	jql, err := nl2jql.Parse("bugs and stories")
	if err != nil {
		t.Fatalf("nl2jql.Parse: %v", err)
	}
	r := assertValid(t, c, jql)
	if r.Structure == nil || r.Structure.Where == nil {
		t.Fatalf("expected a parsed structure for %q", jql)
	}
	if n := r.Structure.Where.CountFieldClauses("issuetype"); n != 1 {
		t.Errorf("%q: oracle sees %d separate issuetype clauses, want 1 (same-field predicates must collapse to IN (...), not AND-ed equalities that can never match)", jql, n)
	}
}

// TestOracle_KnownBadCollapsing_FIELDCTO2808 submits the pre-fix shape
// directly: two AND-ed equalities on the same field. It is valid,
// error-free JQL and simply never matches anything, which strict
// validation has no way to flag -- the only check available is counting
// field clauses, same as above. This proves that check actually
// distinguishes the two shapes.
func TestOracle_KnownBadCollapsing_FIELDCTO2808(t *testing.T) {
	c := oracleClient(t)
	r := assertValid(t, c, "issuetype = Bug AND issuetype = Story")
	if n := r.Structure.Where.CountFieldClauses("issuetype"); n != 2 {
		t.Fatalf("expected the known-bad AND-ed form to show 2 separate issuetype clauses (got %d) -- if this is 1, the clause-count check can no longer tell the bug apart from the fix", n)
	}
}

// TestOracle_FastPathShapes exercises every remaining nl2jql fast-path
// rendering shape not already covered by the 2807/2808 regressions above:
// a lone predicate for each field, the "open" status-category comparison, and a
// multi-word captured value. All are scoped through ensureProjectScope
// first, exactly as the real pipeline does in translateCascade.
func TestOracle_FastPathShapes(t *testing.T) {
	c := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}

	cases := []struct {
		name  string
		query string
	}{
		{"project alone", "in " + oracleProject(t)},
		{"issuetype alone", "bugs"},
		{"status done", "done"},
		{"status in progress", "in progress"},
		{"status open (status category)", "open"},
		{"assignee self", "my issues"},
		{"assignee assigned to me", "assigned to me"},
		{"combined", "my open bugs in " + oracleProject(t)},
		{"due week", "List jiras that are due this week?"},
		{"closed month history", "How many jiras did I close in June 2026?"},
		{"grouped Boolean alternatives", "my (open bugs or done tasks)"},
		{"same-field status alternatives", "my open or done bugs"},
		{"multiple sort keys", "my bugs ordered by priority descending, created ascending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jql, err := nl2jql.Parse(tc.query)
			if err != nil {
				t.Fatalf("nl2jql.Parse(%q): %v", tc.query, err)
			}
			scoped, err := ensureProjectScope(jql, cfg)
			if err != nil {
				t.Fatalf("ensureProjectScope: %v", err)
			}
			assertValid(t, c, scoped)
		})
	}
}

// TestOracle_Phase3Shapes posts the real fast-path output after structural
// project scoping. It covers strict Jira parsing and parsed-tree shape for the
// new date, history, Boolean, and multiple-sort forms.
func TestOracle_Phase3Shapes(t *testing.T) {
	c := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject(t)}
	for _, query := range []string{
		"List jiras that are due this week?",
		"How many jiras did I close in June 2026?",
		"my (open bugs or done tasks)",
		"my open or done bugs",
		"my bugs ordered by priority descending, created ascending",
	} {
		t.Run(query, func(t *testing.T) {
			result, err := GenerateJQLWithProjects(t.Context(), cfg, query, nil)
			if err != nil {
				t.Fatalf("GenerateJQLWithProjects: %v", err)
			}
			if result.Route != RouteFastPath {
				t.Fatalf("route = %q, want %q", result.Route, RouteFastPath)
			}
			r := assertValid(t, c, result.JQL)
			if r.Structure == nil || r.Structure.Where == nil {
				t.Fatalf("expected parsed structure for %q", result.JQL)
			}
			if query == "List jiras that are due this week?" && (strings.Contains(result.JQL, "assignee") || strings.Contains(result.JQL, "ORDER BY")) {
				t.Errorf("due-week output added unrequested owner/order: %s", result.JQL)
			}
			if query == "How many jiras did I close in June 2026?" && (!strings.Contains(result.JQL, "CHANGED TO Closed") || !strings.Contains(result.JQL, `DURING ("2026-06-01 00:00", "2026-07-01 00:00") BEFORE "2026-07-01 00:00"`)) {
				t.Errorf("history output lost CHANGED or exclusive month boundary: %s", result.JQL)
			}
		})
	}
}

// TestOracle_Hallucinations covers the two characteristic LLM hallucination
// modes strict validation exists to catch: a field that does not exist on
// this tenant, and a status value that does not exist for the (real)
// status field. Both are verified live against this tenant's actual
// vocabulary, not asserted from the ticket's examples.
func TestOracle_Hallucinations(t *testing.T) {
	c := oracleClient(t)

	t.Run("nonexistent field", func(t *testing.T) {
		assertRejected(t, c, "project = "+jql.QuoteIdent(oracleProject(t))+` AND sprintName = "Q3" AND storyPoints > 3`)
	})
	t.Run("nonexistent status value", func(t *testing.T) {
		assertRejected(t, c, "project = "+jql.QuoteIdent(oracleProject(t))+` AND status = "Pending Zorblax"`)
	})
}

// TestOracle_SyntaxErrors covers plain grammar violations, independent of
// any field or value vocabulary -- the baseline the "validation=strict"
// mode shares with the default parse mode.
func TestOracle_SyntaxErrors(t *testing.T) {
	c := oracleClient(t)
	assertRejected(t, c, "project = "+jql.QuoteIdent(oracleProject(t))+" AND status = Open AND")
	assertRejected(t, c, "project = "+jql.QuoteIdent(oracleProject(t))+" AND ( ) AND")
}
