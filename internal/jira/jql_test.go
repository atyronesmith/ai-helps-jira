package jira

import (
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

func TestWeeklyActivityJQL(t *testing.T) {
	t.Parallel()

	got, err := WeeklyActivityJQL("FIELDCTO", "currentUser()", "2026-08-10", "2026-08-17")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := `project = FIELDCTO AND (status CHANGED BY currentUser() AFTER "2026-08-10" BEFORE "2026-08-18"` +
		` OR (worklogAuthor = currentUser() AND worklogDate >= "2026-08-10" AND worklogDate <= "2026-08-17"))` +
		` ORDER BY updated DESC`
	gotQuery, err := jql.Parse(got)
	if err != nil {
		t.Fatalf("parse generated query: %v", err)
	}
	wantQuery, err := jql.Parse(want)
	if err != nil {
		t.Fatalf("parse expected query: %v", err)
	}
	if gotQuery.Render() != wantQuery.Render() {
		t.Errorf("WeeklyActivityJQL() has a different normalized JQL shape\n got: %s\nwant: %s", gotQuery.Render(), wantQuery.Render())
	}
}

// updatedBy does not exist as a JQL field on Jira Cloud. The search API accepts it
// with HTTP 200 and silently returns nothing, which is why weekly status reported
// "No issues found" for every window it was ever given (FIELDCTO-2816).
func TestWeeklyActivityJQLAvoidsUpdatedBy(t *testing.T) {
	t.Parallel()

	got, err := WeeklyActivityJQL("FIELDCTO", "currentUser()", "2026-08-10", "2026-08-17")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "updatedBy") {
		t.Errorf("JQL references the nonexistent field updatedBy: %s", got)
	}
}

// CHANGED ... BEFORE is exclusive, so the final day of the window would be dropped
// unless the end date is advanced. worklogDate is date-granular and must not be.
func TestWeeklyActivityJQLEndBounds(t *testing.T) {
	t.Parallel()

	got, err := WeeklyActivityJQL("ENG", "currentUser()", "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `BEFORE "2026-02-01"`) {
		t.Errorf("CHANGED clause should use an exclusive end of 2026-02-01: %s", got)
	}
	if !strings.Contains(got, `worklogDate <= "2026-01-31"`) {
		t.Errorf("worklog clause should use the inclusive end of 2026-01-31: %s", got)
	}
}

// The OR group must stay bracketed. JQL has no precedence climbing, so an
// unbracketed OR would leave the project scope applying to one branch only --
// the same defect as FIELDCTO-2807.
func TestWeeklyActivityJQLBracketsTheOrGroup(t *testing.T) {
	t.Parallel()

	got, err := WeeklyActivityJQL("FIELDCTO", "currentUser()", "2026-08-10", "2026-08-17")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	or := strings.Index(got, " OR ")
	if or < 0 {
		t.Fatalf("expected an OR group: %s", got)
	}
	// Every OR in this query must sit inside parentheses.
	depth := 0
	for i, r := range got {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		}
		if strings.HasPrefix(got[i:], " OR ") && depth == 0 {
			t.Errorf("OR at index %d is at top level and would break project scoping: %s", i, got)
		}
	}
}

func TestWeeklyActivityJQLQuotedUser(t *testing.T) {
	t.Parallel()

	got, err := WeeklyActivityJQL("FIELDCTO", `"a@b.com"`, "2026-08-10", "2026-08-17")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `status CHANGED BY "a@b.com"`) {
		t.Errorf("user identifier should pass through verbatim: %s", got)
	}
}

func TestWeeklyActivityJQLRejectsBadEndDate(t *testing.T) {
	t.Parallel()

	if _, err := WeeklyActivityJQL("FIELDCTO", "currentUser()", "2026-08-10", "not-a-date"); err == nil {
		t.Error("expected an error for an unparseable end date")
	}
}
