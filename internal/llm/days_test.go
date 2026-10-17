package llm

import (
	"errors"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

func TestExtractDaysAcceptsOnlyExactRollingCommentWindows(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{{"show comments in the last 3 days", 3}, {"comment activity over the past 2 weeks", 14}, {"comments last week", 7}, {"comments in the last day", 1}, {"critical bugs", 0}, {"no time window mentioned", 0}, {"", 0}}
	for _, tc := range cases {
		if got := ExtractDays(tc.query); got != tc.want {
			t.Errorf("ExtractDays(%q)=%d want %d", tc.query, got, tc.want)
		}
	}
}

func TestExtractDaysRejectsVagueAndUnrepresentableCommentWindows(t *testing.T) {
	for _, query := range []string{"comments last couple of days", "comments last few days", "comments last 5 sprints", "comments last 2 months", "comments yesterday", "comments this week", "comments since Monday"} {
		t.Run(query, func(t *testing.T) {
			if got := ExtractDays(query); got != 0 {
				t.Errorf("ExtractDays=%d, want 0", got)
			}
			_, _, err := splitDigestQuery(query)
			if !errors.Is(err, nl2jql.ErrAmbiguousIntent) {
				t.Fatalf("split error=%v, want clarification", err)
			}
		})
	}
}

func TestIssueDateWindowsNeverBecomeCommentDays(t *testing.T) {
	for _, query := range []string{"issues created in the last 3 days", "issues updated over the past 2 weeks", "issues resolved last week", "issues due in the last 5 days", "issues created after last week", "issues updated before last week", "issues with creation date last week"} {
		t.Run(query, func(t *testing.T) {
			clean, days, err := splitDigestQuery(query)
			if err != nil {
				t.Fatal(err)
			}
			if days != 0 {
				t.Fatalf("issue date window became comment days: %d", days)
			}
			if clean != query {
				t.Fatalf("issue-date query changed: %q", clean)
			}
		})
	}
}
