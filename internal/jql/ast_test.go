package jql

import (
	"reflect"
	"testing"
)

func TestParseRenderParseAST(t *testing.T) {
	cases := []string{
		`status = Open OR assignee = currentUser()`,
		`priority != Lowest AND number > 2 AND number >= 3 AND number < 9 AND number <= 8`,
		`summary ~ "phrase" AND summary !~ "other phrase"`,
		`"Story Points" > .5 AND "Story Points" < -0.25`,
		`status WAS Closed BY (alice, bob) DURING ("2026-01-01", "2026-02-01")`,
		`labels IN (one, "two words") AND labels NOT IN (three)`,
		`issuetype IN standardIssueTypes() AND issuetype NOT IN subtaskIssueTypes()`,
		`assignee IN membersOf("engineering")`,
		`issue IN nestedFunction(otherFunction("value"), "second")`,
		`resolution IS EMPTY AND resolution IS NOT NULL`,
		`"Story Points" >= 5 AND cf[123] = currentUser("account id")`,
		`created >= startOfDay("+2d") AND updated < endOfWeek()`,
		`status WAS Open AND status WAS NOT Closed AND status WAS IN (Open, "In Progress") AND status WAS NOT IN (Closed, Resolved)`,
		`status WAS Open BY currentUser() DURING ("2026-01-01", "2026-01-31") BEFORE "2026-02-01" AFTER "2025-01-01" ON "2026-01-15"`,
		`assignee CHANGED FROM "old account" TO currentUser() BY currentUser()`,
		`project = TEST ORDER BY "Story Points" DESC, cf[123] ASC, updated`,
		`NOT (status = Closed OR (priority = High AND NOT assignee = currentUser()))`,
		`summary ~ "hostile \\\" quote and \\\\ slash"`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			first, err := Parse(input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", input, err)
			}
			second, err := Parse(first.Render())
			if err != nil {
				t.Fatalf("Parse(Render(Parse(%q))): %v", input, err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Errorf("AST changed after render/parse:\n first: %#v\nsecond: %#v\nrender: %s", first, second, first.Render())
			}
		})
	}
}

func TestParseRejectsMalformedSyntax(t *testing.T) {
	cases := []string{
		`status =`, `status Open`, `status = Open extra`, `status == Open`,
		`summary ~ "unterminated`, `summary ~ 'unterminated`,
		`(status = Open`, `status = Open)`,
		`status = Open AND (priority = High ORDER BY updated)`,
		`status = Open ORDER BY updated ASC ORDER BY key DESC`,
		`status = Open ORDER BY updated ASC AND priority = High`,
		`status = Open ORDER BY updated SIDEWAYS`,
		`project IN ()`, `project IN (TEST, )`, `assignee = currentUser(`,
		`issuetype IN Bug`, `issuetype NOT IN subtaskIssueTypes(`,
		`issuetype IN subtaskIssueTypes() extra`,
		`issuetype IN subtaskIssueTypes() ORDER BY updated AND status = Open`,
		`status WAS Open BY a BY b`,
		`status WAS Open DURING ("2026-01-01")`,
		`status WAS IN Open`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if q, err := Parse(input); err == nil {
				t.Errorf("Parse(%q) unexpectedly succeeded: %#v", input, q)
			}
		})
	}
}

func TestScopeASTSemantics(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{`status = Open OR project = TEST`, false},
		{`(status = Open OR priority = High) AND project = TEST`, true},
		{`project IN (TEST, OTHER) AND status = Open`, true},
		{`NOT project = TEST`, false},
		{`project = TEST AND status = Open OR priority = High`, false},
		{`"project" = TEST AND status = Open`, true},
		{`"project" = TEST OR status = Open`, false},
		{`project = TEST AND issuetype NOT IN subtaskIssueTypes()`, true},
		{`project = TEST OR issuetype NOT IN subtaskIssueTypes()`, false},
	} {
		q, err := Parse(tc.query)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.query, err)
			continue
		}
		if got := q.HasProjectScope(); got != tc.want {
			t.Errorf("%q scope=%v want %v", tc.query, got, tc.want)
		}
	}
}

func TestMembershipFunctionDoesNotGrantIdentityScopeExemption(t *testing.T) {
	for _, raw := range []string{`key IN linkedIssues("PROJ-1")`, `id IN linkedIssues("PROJ-1")`} {
		q, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if q.DirectIdentityOnly() {
			t.Fatalf("function query incorrectly treated as direct identity: %s", raw)
		}
		if err := Validate(raw, Options{DefaultProject: "PROJ"}); err == nil {
			t.Fatalf("unscoped function query accepted: %s", raw)
		}
	}
}

func FuzzParseRenderRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`status = Open`, `a = 1 OR (b IN (2, 3) AND c ~ "x")`,
		`ORDER BY updated DESC`, `status WAS Open BY currentUser()`,
		`NOT (project = TEST OR priority >= 2)`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		q, err := Parse(input)
		if err != nil {
			return
		}
		round, err := Parse(q.Render())
		if err != nil {
			t.Fatalf("rendered query did not parse: %v", err)
		}
		if !reflect.DeepEqual(q, round) {
			t.Fatalf("AST changed after round trip:\n%#v\n%#v\nrender: %s", q, round, q.Render())
		}
	})
}

func FuzzQuotedLiteralRoundTrip(f *testing.F) {
	for _, value := range []string{`simple`, `quote " slash \\`, `OR ) AND project = OTHER`} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		input := `summary = ` + Quote(value)
		q, err := Parse(input)
		if err != nil {
			t.Fatalf("quoted value failed to parse: %v", err)
		}
		round, err := Parse(q.Render())
		if err != nil {
			t.Fatalf("rendered value failed to parse: %v", err)
		}
		if !reflect.DeepEqual(q, round) {
			t.Fatalf("quoted AST changed after round trip: %#v != %#v", q, round)
		}
	})
}

func TestHasFieldInspectsPredicates(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`summary ~ "project = X"`, false}, {`NOT "project" IN (OTHER)`, true}, {`project = TEST OR status = Open`, true},
	} {
		q, err := Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if q.HasField("project") != tc.want {
			t.Fatalf("field detection: %s", tc.raw)
		}
	}
}
