package nl2jql

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var closeMonthReferenceTime = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func TestTranslateClosedMonthExactIntent(t *testing.T) {
	provider := &recordingProvider{
		jql:  `status = Open`,
		fail: errors.New("closed-month intent must not call the LLM"),
	}
	tr := newRoutingTranslator(t, provider)

	const input = "  How many jiras did I close in June 2026?  "
	result, err := tr.Translate(context.Background(), input)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := `status WAS Closed BY currentUser() DURING ("2026/06/01", "2026/07/01")`
	if result.JQL != want {
		t.Errorf("JQL = %q, want %q", result.JQL, want)
	}
	if result.Route != RouteFastPath {
		t.Errorf("route = %q, want %q", result.Route, RouteFastPath)
	}
	if result.Input != input {
		t.Errorf("input = %q, want exact original input %q", result.Input, input)
	}
	if result.NormalizedInput != "How many jiras did I close in June 2026?" {
		t.Errorf("normalized input = %q, want trimmed input", result.NormalizedInput)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Errorf("fallback provider called %d times, want 0", got)
	}
}

func TestTranslateClosedMonthAllEnglishMonths(t *testing.T) {
	months := []struct {
		name  string
		start string
		end   string
	}{
		{"JANUARY", "01", "02"}, {"February", "02", "03"}, {"March", "03", "04"},
		{"April", "04", "05"}, {"May", "05", "06"}, {"June", "06", "07"},
		{"July", "07", "08"}, {"August", "08", "09"}, {"September", "09", "10"},
		{"October", "10", "11"}, {"November", "11", "12"}, {"December", "12", "2027/01"},
	}
	for _, tc := range months {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := translateClosedMonthQuery("How many jiras did I close in "+tc.name+" 2026?", closeMonthReferenceTime)
			if !ok {
				t.Fatal("query was not recognized")
			}
			start := "2026/" + tc.start + "/01"
			end := "2026/" + tc.end + "/01"
			if tc.name == "December" {
				end = tc.end + "/01"
			}
			want := `status WAS Closed BY currentUser() DURING ("` + start + `", "` + end + `")`
			if got != want {
				t.Errorf("JQL = %q, want %q", got, want)
			}
		})
	}
}

func TestTranslateClosedMonthFebruaryAndDecemberRollover(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{
			query: "How many issues did I close in February 2024?",
			want:  `status WAS Closed BY currentUser() DURING ("2024/02/01", "2024/03/01")`,
		},
		{
			query: "How many jira issues did I close in December 2026?",
			want:  `status WAS Closed BY currentUser() DURING ("2026/12/01", "2027/01/01")`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := translateClosedMonthQuery(tc.query, closeMonthReferenceTime)
			if !ok {
				t.Fatal("query was not recognized")
			}
			if got != tc.want {
				t.Errorf("JQL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClosedMonthIntentRejectsMalformedAndAmbiguousQueries(t *testing.T) {
	cases := []string{
		"How many jiras did I close in Jun 2026?",
		"How many jiras did I close in June 26?",
		"How many jiras did I close in June 0000?",
		"How many jiras did I close in June 2026 or July 2026?",
		"How many jiras were closed in June 2026?",
		"List jiras I closed in either July, April of this year?",
		"List jiras I closed in July or April last year?",
		"How many jiras did I close in June 2026? in the current project",
	}
	for _, query := range cases {
		t.Run(query, func(t *testing.T) {
			provider := &recordingProvider{jql: `status = Closed`}
			tr := newRoutingTranslator(t, provider)
			result, err := tr.Translate(context.Background(), query)
			if err != nil {
				t.Fatalf("Translate(%q): %v", query, err)
			}
			if result.Route != RouteFallback {
				t.Errorf("route = %q, want %q", result.Route, RouteFallback)
			}
			if got := atomic.LoadInt32(&provider.calls); got != 1 {
				t.Errorf("fallback provider called %d times, want 1", got)
			}
		})
	}
}

func TestTranslateClosedMonthsMultipleAndCurrentYear(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{
			query: "List jiras I closed in either July or april of this year?",
			want:  `(status WAS Closed BY currentUser() DURING ("2026/07/01", "2026/08/01") OR status WAS Closed BY currentUser() DURING ("2026/04/01", "2026/05/01"))`,
		},
		{
			query: "How many issues did I close in December or FEBRUARY 2024?",
			want:  `(status WAS Closed BY currentUser() DURING ("2024/12/01", "2025/01/01") OR status WAS Closed BY currentUser() DURING ("2024/02/01", "2024/03/01"))`,
		},
		{
			query: "Count jiras I closed in July or april or JULY 2026?",
			want:  `(status WAS Closed BY currentUser() DURING ("2026/07/01", "2026/08/01") OR status WAS Closed BY currentUser() DURING ("2026/04/01", "2026/05/01"))`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := translateClosedMonthQuery(tc.query, closeMonthReferenceTime)
			if !ok {
				t.Fatal("query was not recognized")
			}
			if got != tc.want {
				t.Errorf("JQL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranslateClosedMonthQueryUsesInjectedClock(t *testing.T) {
	got, ok := translateClosedMonthQuery("List jiras I closed in December or January of this year?", time.Date(2026, time.December, 31, 23, 0, 0, 0, time.FixedZone("test", -5*60*60)))
	if !ok {
		t.Fatal("query was not recognized")
	}
	want := `(status WAS Closed BY currentUser() DURING ("2026/12/01", "2027/01/01") OR status WAS Closed BY currentUser() DURING ("2026/01/01", "2026/02/01"))`
	if got != want {
		t.Errorf("JQL = %q, want %q", got, want)
	}
}

func TestTranslateClosedMonthsFastPathSkipsProvider(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("multi-month intent must not call the LLM")}
	tr := newRoutingTranslator(t, provider)
	tr.normalizer.Now = func() time.Time { return closeMonthReferenceTime }

	const input = "List jiras I closed in either July or april of this year?"
	result, err := tr.Translate(context.Background(), input)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := `(status WAS Closed BY currentUser() DURING ("2026/07/01", "2026/08/01") OR status WAS Closed BY currentUser() DURING ("2026/04/01", "2026/05/01"))`
	if result.JQL != want {
		t.Errorf("JQL = %q, want %q", result.JQL, want)
	}
	if result.Route != RouteFastPath {
		t.Errorf("route = %q, want %q", result.Route, RouteFastPath)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Errorf("fallback provider called %d times, want 0", got)
	}
}
