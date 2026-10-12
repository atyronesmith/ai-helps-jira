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
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jqloracle"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

// oracleProject is the real, live project key used for every positive case
// in this corpus. Strict validation checks not just field names but value
// existence, so a positive case naming a project must name one that
// actually exists on the tenant -- a fictitious "Support" or "ENG" would
// fail for the wrong reason (hallucinated value) and make the corpus
// untrustworthy. FIELDCTO is this tenant's own project (see .env /
// JIRA_PROJECT), so every GenerateJQL* call site already scopes to it by
// default and it is guaranteed to exist.
const oracleProject = "FIELDCTO"

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
		t.Errorf("expected %q to be valid, got errors: %v", jql, r.Errors)
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
	cfg := &config.Config{JiraProject: oracleProject}

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
			scoped := ensureProjectScope(tc.raw, cfg)
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
		// "issuetype = Bug OR (issuetype = Story AND project = FIELDCTO)" --
		// the first branch escapes the scope entirely.
		"issuetype = Bug OR issuetype = Story AND project = " + oracleProject,
		"status = Open OR priority = High AND project = " + oracleProject,
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
	assertRejected(t, c, "status = Closed ORDER BY created DESC AND project = "+oracleProject)
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
// a lone predicate for each field, the "open" NOT IN expansion, and a
// multi-word captured value. All are scoped through ensureProjectScope
// first, exactly as the real pipeline does in translateCascade.
func TestOracle_FastPathShapes(t *testing.T) {
	c := oracleClient(t)
	cfg := &config.Config{JiraProject: oracleProject}

	cases := []struct {
		name  string
		query string
	}{
		{"project alone", "in " + oracleProject},
		{"issuetype alone", "bugs"},
		{"status done", "done"},
		{"status in progress", "in progress"},
		{"status open (NOT IN expansion)", "open"},
		{"assignee self", "my issues"},
		{"assignee assigned to me", "assigned to me"},
		{"combined", "my open bugs in " + oracleProject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jql, err := nl2jql.Parse(tc.query)
			if err != nil {
				t.Fatalf("nl2jql.Parse(%q): %v", tc.query, err)
			}
			scoped := ensureProjectScope(jql, cfg)
			assertValid(t, c, scoped)
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
		assertRejected(t, c, "project = "+oracleProject+` AND sprintName = "Q3" AND storyPoints > 3`)
	})
	t.Run("nonexistent status value", func(t *testing.T) {
		assertRejected(t, c, "project = "+oracleProject+` AND status = "Pending Zorblax"`)
	})
}

// TestOracle_SyntaxErrors covers plain grammar violations, independent of
// any field or value vocabulary -- the baseline the "validation=strict"
// mode shares with the default parse mode.
func TestOracle_SyntaxErrors(t *testing.T) {
	c := oracleClient(t)
	assertRejected(t, c, "project = "+oracleProject+" AND status = Open AND")
	assertRejected(t, c, "project = "+oracleProject+" AND ( ) AND")
}
