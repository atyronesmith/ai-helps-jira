package jql

import (
	"testing"
	"time"
)

// fixedNow mirrors the incident date (2026-10-09) referenced in
// FIELDCTO-2815, so the -365d case resolves to exactly the example in the
// ticket.
var fixedNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestResolveRelativeDatesForDisplay(t *testing.T) {
	tests := []struct {
		name string
		jql  string
		want string
	}{
		{
			name: "no relative date is a no-op",
			jql:  `project = FIELDCTO AND status = Closed`,
			want: `project = FIELDCTO AND status = Closed`,
		},
		{
			name: "no-op preserves an unrelated quoted literal",
			jql:  `summary ~ "release-2.0" AND status = Closed`,
			want: `summary ~ "release-2.0" AND status = Closed`,
		},
		{
			name: "the original incident query",
			jql:  `project = FIELDCTO AND status = Closed AND created >= -365d`,
			want: `project = FIELDCTO AND status = Closed AND created >= 2025-10-09`,
		},
		{
			name: "several relative dates in one query",
			jql:  `project = FIELDCTO AND created >= -7d AND updated >= -14d ORDER BY created DESC`,
			want: `project = FIELDCTO AND created >= 2026-10-02 AND updated >= 2026-09-25 ORDER BY created DESC`,
		},
		{
			name: "week and year units",
			jql:  `created >= -2w AND due >= -1y`,
			want: `created >= 2026-09-25 AND due >= 2025-10-09`,
		},
		{
			// Jira reads BOTH m and M as minutes, case-insensitively, and has
			// no month unit. Verified live: "-100000m" and "-100000M" each
			// return what "-70d" does, 100000 minutes being about 69 days.
			//
			// QuerySystemPrompt lists "-1m" in a way that reads as "one month"
			// (FIELDCTO-2817), so the model may emit it meaning a month.
			// Rendering the minute-wide window it actually produces is the
			// point: a timestamp moments old, next to a request that said
			// "last month", is wrong at a glance — where "2026-09-09" would
			// have looked entirely reasonable.
			name: "lowercase m is minutes, not months, and shows a clock time",
			jql:  `created >= -1m`,
			want: `created >= 2026-10-09 11:59`,
		},
		{
			name: "uppercase M is also minutes, not months",
			jql:  `created >= -1M`,
			want: `created >= 2026-10-09 11:59`,
		},
		{
			name: "hours also render with a clock time",
			jql:  `created >= -3h`,
			want: `created >= 2026-10-09 09:00`,
		},
		{
			name: "zero-argument date functions resolve",
			jql:  `created >= startOfDay() AND updated >= startOfWeek()`,
			want: `created >= 2026-10-09 AND updated >= 2026-10-05`,
		},
		{
			name: "a relative-looking token inside a quoted literal is left alone",
			jql:  `summary ~ "ship by -7d sprint" AND created >= -7d`,
			want: `summary ~ "ship by -7d sprint" AND created >= 2026-10-02`,
		},
		{
			name: "empty input is a no-op",
			jql:  ``,
			want: ``,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRelativeDatesForDisplay(tc.jql, fixedNow)
			if got != tc.want {
				t.Errorf("ResolveRelativeDatesForDisplay(%q)\n got:  %q\n want: %q", tc.jql, got, tc.want)
			}
		})
	}
}

// TestResolveRelativeDatesForDisplay_DoesNotMutateInput guards the "display
// only" contract: the function must never be mistaken for something safe to
// call in place of the original JQL sent to Jira. This is a documentation
// test, not a runtime check -- Go strings are immutable, so there is no
// mutation to catch, but the test records the intent and would catch a
// future refactor that started modifying a byte slice in place.
func TestResolveRelativeDatesForDisplay_DoesNotMutateInput(t *testing.T) {
	original := `project = FIELDCTO AND created >= -365d`
	cp := original
	_ = ResolveRelativeDatesForDisplay(original, fixedNow)
	if original != cp {
		t.Fatalf("input string changed: %q -> %q", cp, original)
	}
}
