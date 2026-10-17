package nl2jql

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

func TestIntentBooleanBehavior(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"my open or done bugs", `issuetype = Bug AND (statusCategory != Done OR status = Done) AND assignee = currentUser()`},
		{"my (open bugs or done tasks) in TEST", `project = TEST AND assignee = currentUser() AND (issuetype = Bug AND statusCategory != Done OR issuetype = Task AND status = Done)`},
		{"(open bugs) or (done tasks)", `issuetype = Bug AND statusCategory != Done OR issuetype = Task AND status = Done`},
		{"my bugs and stories", `issuetype IN (Bug, Story) AND assignee = currentUser()`},
		{"not (done or in progress) bugs", `issuetype = Bug AND NOT status IN (Done, "In Progress")`},
		{`my (created today or updated yesterday)`, `assignee = currentUser() AND (created >= startOfDay() AND created < startOfDay("+1") OR updated >= startOfDay("-1") AND updated < startOfDay())`},
		{"currently closed bugs", `issuetype = Bug AND status = Closed`},
		{"my open bugs ordered by priority desc, updated asc", `issuetype = Bug AND statusCategory != Done AND assignee = currentUser() ORDER BY priority DESC, updated ASC`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			provider := &recordingProvider{fail: errors.New("must be deterministic")}
			tr := newRoutingTranslator(t, provider)
			res, err := tr.Translate(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := jql.Parse(res.JQL)
			if err != nil {
				t.Fatal(err)
			}
			want, err := jql.Parse(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if actual.Render() != want.Render() {
				t.Fatalf("got %s\nwant %s", actual.Render(), want.Render())
			}
			if atomic.LoadInt32(&provider.calls) != 0 {
				t.Fatal("called provider")
			}
		})
	}
}

func TestStoryPointsParsePlanRetainsTenantFieldIntent(t *testing.T) {
	plan, err := ParsePlan(`my (story points > 3 or story points < 1)`, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Filter == nil || len(plan.Filter.Children) < 2 {
		t.Fatalf("plan = %+v", plan)
	}
	for _, child := range plan.Filter.Children {
		if child.Kind == IntentAny {
			for _, predicate := range child.Children {
				if predicate.Field != `"Story Points"` {
					t.Fatalf("field = %q", predicate.Field)
				}
			}
		}
	}
}
func TestIntentAmbiguityDoesNotReachProvider(t *testing.T) {
	for _, input := range []string{
		"open and done bugs", "done and in progress bugs", "open done bugs",
		"my (open and done bugs or done tasks)", "(open) and (done)",
		"open bugs or done tasks", "my or open bugs", "my bugs assigned to Bob", "my and assigned to Bob", "bugs in TEST and tasks in OTHER", "created today or updated yesterday", "(open bugs) or (done tasks) in TEST",
		"not closed or resolved bugs", "bugs created in the last few days", "bugs resolved in the last 2 sprints",
		"bugs created in the last 2 months", "bugs created in the last 0 days", "bugs across all projects", "bugs were open", "my open and",
	} {
		t.Run(input, func(t *testing.T) {
			p := &recordingProvider{jql: supportedStatusPlan}
			tr := newRoutingTranslator(t, p)
			_, err := tr.Translate(context.Background(), input)
			if !errors.Is(err, ErrAmbiguousIntent) {
				t.Fatalf("want clarification, got %v", err)
			}
			if atomic.LoadInt32(&p.calls) != 0 {
				t.Fatal("provider guessed ambiguous query")
			}
		})
	}
}
func TestIntentDateWindows(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"my bugs created in the last 3 days", `issuetype = Bug AND assignee = currentUser() AND created >= -3d AND created <= now()`},
		{"bugs updated yesterday", `issuetype = Bug AND updated >= startOfDay("-1") AND updated < startOfDay()`},
		{"bugs created today", `issuetype = Bug AND created >= startOfDay() AND created < startOfDay("+1")`},
		{"bugs resolved last week", `issuetype = Bug AND resolved >= startOfWeek("-1") AND resolved < startOfWeek()`},
		{"bugs resolved last month", `issuetype = Bug AND resolved >= startOfMonth("-1") AND resolved < startOfMonth()`},
		{"my issues due this week ordered by priority desc", `assignee = currentUser() AND due >= startOfWeek() AND due < startOfWeek("+1") ORDER BY priority DESC`},
		{"bugs created last Tuesday", `issuetype = Bug AND created >= startOfDay("-7") AND created < startOfDay("-6")`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			p := &recordingProvider{fail: errors.New("date must be deterministic")}
			tr := newRoutingTranslator(t, p)
			tr.normalizer.Now = func() time.Time { return closeMonthReferenceTime }
			res, err := tr.Translate(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if res.JQL != tc.want {
				t.Fatalf("got %s\nwant %s", res.JQL, tc.want)
			}
			if atomic.LoadInt32(&p.calls) != 0 {
				t.Fatal("called provider")
			}
		})
	}
}
func TestTypedDatesOwnershipAndMode(t *testing.T) {
	tr := newRoutingTranslator(t, &recordingProvider{fail: errors.New("unexpected provider")})
	tr.self = `"other@example.com"`
	res, err := tr.Translate(context.Background(), "Which of my bugs created today should I work on next?")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeRecommendation || !strings.Contains(res.JQL, `assignee = "other@example.com"`) {
		t.Fatalf("wrong ownership/mode: %+v", res)
	}
	plan, err := ParsePlan(tr.normalizer.Normalize("bugs updated yesterday", nil), tr.self, closeMonthReferenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dates) != 2 || plan.Dates[0].Lower == nil || !plan.Dates[0].LowerInclusive || plan.Dates[1].Upper == nil || plan.Dates[1].UpperInclusive {
		t.Fatalf("wrong typed bounds: %+v", plan.Dates)
	}
	if plan.Dates[0].Lower.Kind != BoundFunction || plan.Dates[0].Lower.Function != "startOfDay" || plan.Dates[0].Lower.Offset != -1 {
		t.Fatalf("wrong bound: %+v", plan.Dates[0].Lower)
	}
}
