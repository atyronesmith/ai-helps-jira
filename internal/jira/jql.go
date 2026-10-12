package jira

import (
	"fmt"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// WeeklyActivityJQL builds the query behind the weekly status report: issues the
// user actually worked on between start and end, meaning they either moved an
// issue's status or logged work against it.
//
// It deliberately avoids `updatedBy`. That field does not exist on Jira Cloud, and
// the search API does not say so — it answers HTTP 200 with an empty result set, so
// weekly status reported "No issues found" for every window it was ever given. The
// failure was invisible because an empty answer is indistinguishable from a quiet
// week (FIELDCTO-2816).
//
// start and end are inclusive calendar dates in YYYY-MM-DD form. The two clauses
// need different end bounds: `CHANGED ... BEFORE` is exclusive, so end is advanced a
// day for it, while worklogDate comparisons are date-granular and take end directly.
//
// user must arrive already in JQL form — either currentUser() or a quoted
// identifier — which is what config.Assignee() returns.
func WeeklyActivityJQL(project, user, start, end string) (string, error) {
	endTime, err := time.Parse("2006-01-02", end)
	if err != nil {
		return "", fmt.Errorf("weekly activity jql: invalid end date %q: %w", end, err)
	}
	endExclusive := endTime.AddDate(0, 0, 1).Format("2006-01-02")

	// The OR group is parenthesised deliberately. JQL has no precedence climbing,
	// so an unbracketed OR here would let the project scope apply to only one
	// branch — the same defect as FIELDCTO-2807.
	return fmt.Sprintf(
		`project = %s AND (status CHANGED BY %s AFTER %q BEFORE %q`+
			` OR (worklogAuthor = %s AND worklogDate >= %q AND worklogDate <= %q))`+
			` ORDER BY updated DESC`,
		jql.QuoteIdent(project), user, start, endExclusive, user, start, end,
	), nil
}
