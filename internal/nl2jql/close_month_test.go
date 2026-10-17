package nl2jql

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var closeMonthReferenceTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func TestClosedMonthCalendarAndActor(t *testing.T) {
	cases := []struct{ query, start, end string }{
		{"How many jiras did I close in June 2026?", "2026-06-01 00:00", "2026-07-01 00:00"},
		{"issues closed by me in February 2024", "2024-02-01 00:00", "2024-03-01 00:00"},
		{"List bugs I closed in December 2026", "2026-12-01 00:00", "2027-01-01 00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			plan, err := ParsePlan(tc.query, `"other@example.com"`, closeMonthReferenceTime)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := plan.Compile()
			if err != nil {
				t.Fatal(err)
			}
			want := `status CHANGED TO Closed BY "other@example.com" DURING ("` + tc.start + `", "` + tc.end + `") BEFORE "` + tc.end + `"`
			if !strings.Contains(tree.Render(), want) {
				t.Fatalf("got %s, want history %s", tree.Render(), want)
			}
			if strings.Contains(tc.query, "bugs") && !strings.Contains(tree.Render(), "issuetype = Bug") {
				t.Fatal("lost issue type")
			}
		})
	}
}
func TestClosedMonthAllEnglishMonths(t *testing.T) {
	for _, tc := range []struct{ name, start, end string }{
		{"January", "2026-01-01", "2026-01-31"}, {"February", "2026-02-01", "2026-02-28"},
		{"March", "2026-03-01", "2026-03-31"}, {"April", "2026-04-01", "2026-04-30"},
		{"May", "2026-05-01", "2026-05-31"}, {"June", "2026-06-01", "2026-06-30"},
		{"July", "2026-07-01", "2026-07-31"}, {"August", "2026-08-01", "2026-08-31"},
		{"September", "2026-09-01", "2026-09-30"}, {"October", "2026-10-01", "2026-10-31"},
		{"November", "2026-11-01", "2026-11-30"}, {"December", "2026-12-01", "2026-12-31"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParsePlan("issues I closed in "+tc.name+" 2026", "", closeMonthReferenceTime)
			if err != nil {
				t.Fatal(err)
			}
			h := plan.Filter.History
			if h == nil || h.Start != tc.start+" 00:00" || h.End != endExclusive(t, tc.end) {
				t.Fatalf("wrong window: %+v", h)
			}
		})
	}
}
func TestClosedMonthAlternativesComposeWithFilters(t *testing.T) {
	p := &recordingProvider{fail: errors.New("history should not call provider")}
	tr := newRoutingTranslator(t, p)
	tr.normalizer.Now = func() time.Time { return closeMonthReferenceTime }
	input := "Count bugs I closed in either July or April or JULY of this year in TEST"
	res, err := tr.Translate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if res.Input != input || res.Mode != ModeCount || res.Route != RouteFastPath {
		t.Fatalf("wrong diagnostics: %+v", res)
	}
	for _, want := range []string{"project = TEST", "issuetype = Bug", "2026-08-01 00:00", "2026-05-01 00:00", " AND (status CHANGED"} {
		if !strings.Contains(res.JQL, want) {
			t.Errorf("lost %q: %s", want, res.JQL)
		}
	}
	if strings.Count(res.JQL, "CHANGED TO") != 2 {
		t.Fatal("duplicate history branch")
	}
	if atomic.LoadInt32(&p.calls) != 0 {
		t.Fatal("called provider")
	}
}
func TestClosedMonthAmbiguityDoesNotReachProvider(t *testing.T) {
	for _, input := range []string{
		"How many jiras did I close in Jun 2026?", "issues I closed in June 26",
		"issues I closed in June 0000", "issues I closed in June 2026 or July 2026",
		"issues were closed in June 2026", "issues I closed in either July, April of this year",
		"issues I closed in July or April last year", "issues I closed in June 2026 in the current project",
	} {
		t.Run(input, func(t *testing.T) {
			p := &recordingProvider{jql: supportedStatusPlan}
			tr := newRoutingTranslator(t, p)
			_, err := tr.Translate(context.Background(), input)
			if !errors.Is(err, ErrAmbiguousIntent) {
				t.Fatalf("want clarification, got %v", err)
			}
			if atomic.LoadInt32(&p.calls) != 0 {
				t.Fatal("provider guessed ambiguous intent")
			}
		})
	}
}

func endExclusive(t *testing.T, end string) string {
	t.Helper()
	d, err := time.Parse("2006-01-02", end)
	if err != nil {
		t.Fatal(err)
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02 15:04")
}
