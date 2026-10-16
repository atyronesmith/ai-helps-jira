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

	// Keep the status history expression as one atom; the AST then keeps the
	// project restriction outside the OR across the two activity sources.
	changed, err := jql.Atom(fmt.Sprintf("status CHANGED BY %s AFTER %s BEFORE %s", user, jql.Quote(start), jql.Quote(endExclusive)))
	if err != nil {
		return "", fmt.Errorf("weekly activity jql: invalid user expression: %w", err)
	}
	worklog := jql.All(
		jql.Compare("worklogAuthor", "=", user),
		jql.Compare("worklogDate", ">=", jql.Quote(start)),
		jql.Compare("worklogDate", "<=", jql.Quote(end)),
	)
	return (&jql.Query{
		Where: jql.All(
			jql.Compare("project", "=", jql.QuoteIdent(project)),
			jql.Any(changed, worklog),
		),
		OrderBy: []jql.Sort{{Field: "updated", Direction: "DESC"}},
	}).Render(), nil
}
