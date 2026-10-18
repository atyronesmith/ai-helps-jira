// engine_test.go pins the layered routing contract of the translation pipeline:
//
//	Normalizer (dates, typos, project names)
//	    -> Parse     (fast: the "my open bugs in Support" subset)
//	    -> Fallback  (LLM: priority, dates-as-JQL, complex filters)
//
// The fast path is taken only when the strict grammar accepts the whole query;
// anything the grammar rejects -- even a fragment it half-recognises -- routes
// to the LLM fallback, which still receives the normalised (cleaner) text.
package nl2jql

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// recordingProvider counts fallback invocations and captures the exact user
// prompt it receives, so a test can assert both which route a query takes and
// that the LLM is fed the normalised -- not the raw -- query.
type recordingProvider struct {
	calls     int32
	lastInput string
	jql       string
	fail      error
}

func (p *recordingProvider) Generate(_ context.Context, _, user string) (string, error) {
	atomic.AddInt32(&p.calls, 1)
	p.lastInput = user
	return p.jql, p.fail
}

func newRoutingTranslator(t *testing.T, p Provider) *Translator {
	t.Helper()
	fb, err := NewFallbackEngine(p)
	if err != nil {
		t.Fatalf("NewFallbackEngine: %v", err)
	}
	tr, err := NewTranslator(NewNormalizer(), fb, Options{})
	if err != nil {
		t.Fatalf("NewTranslator: %v", err)
	}
	return tr
}

// TestRoutingExpectations pins the three documented routing expectations of the
// layered pipeline: simple agile phrasing stays on the fast path, while
// priority / dates-as-JQL / complex filters and raw JQL fragments fall through
// to the LLM.
func TestRoutingExpectations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		query     string
		wantRoute string
		wantCalls int32
		wantJQL   string
	}{
		{
			name:      "simple agile phrasing takes the fast path",
			query:     "my open bugs in Support",
			wantRoute: "fast_path",
			wantCalls: 0,
			wantJQL:   `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`,
		},
		{
			name:      "count question for my critical open issues stays deterministic",
			query:     "How many Jira issues do I have open that are priority Critical?",
			wantRoute: RouteFastPath,
			wantCalls: 0,
			wantJQL:   `statusCategory != Done AND assignee = currentUser() AND priority = Critical`,
		},
		{
			name:      "priority + dates-as-JQL + complex filters fall through to the LLM",
			query:     "critical bugs from last week",
			wantRoute: "fallback",
			wantCalls: 1,
			wantJQL:   "status = Open",
		},
		{
			name:      "typed date comparisons stay deterministic",
			query:     "my open bugs in SUP created >= -3d",
			wantRoute: RouteFastPath,
			wantCalls: 0,
			wantJQL:   "project = SUP AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser() AND created >= -3d",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The fast path must never touch the LLM; the fallback must reach
			// it exactly once. A non-nil fail guards the fast-path case.
			fail := errors.New("fast path must not reach the LLM")
			if tc.wantCalls > 0 {
				fail = nil
			}
			p := &recordingProvider{jql: supportedStatusPlan, fail: fail}
			tr := newRoutingTranslator(t, p)

			res, err := tr.Translate(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("Translate(%q): %v", tc.query, err)
			}
			if res.Route != tc.wantRoute {
				t.Errorf("route = %q, want %q", res.Route, tc.wantRoute)
			}
			if got := atomic.LoadInt32(&p.calls); got != tc.wantCalls {
				t.Errorf("provider reached %d times, want %d", got, tc.wantCalls)
			}
			if res.JQL != tc.wantJQL {
				t.Errorf("JQL = %q, want %q", res.JQL, tc.wantJQL)
			}
			if res.Input != tc.query {
				t.Errorf("input = %q, want exact input %q", res.Input, tc.query)
			}
			if res.NormalizedInput == "" {
				t.Error("normalized input is empty")
			}
			if tc.wantRoute == RouteFallback && res.ParseError == "" {
				t.Error("fallback result has no deterministic parse diagnostic")
			}
		})
	}
}

func TestRecommendationQueryKeepsFilterOnFastPath(t *testing.T) {
	const query = "Which of my open jiras should I work on next?"
	provider := &recordingProvider{fail: errors.New("recommendation filter must not call the LLM")}
	tr := newRoutingTranslator(t, provider)

	result, err := tr.Translate(context.Background(), query)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if result.Route != RouteFastPath {
		t.Errorf("route = %q, want %q", result.Route, RouteFastPath)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Errorf("provider called %d times, want 0", got)
	}
	want := `statusCategory != Done AND assignee = currentUser()`
	if result.JQL != want {
		t.Errorf("JQL = %q, want %q", result.JQL, want)
	}
	if result.NormalizedInput != "my open jiras" {
		t.Errorf("normalized input = %q, want filter intent only", result.NormalizedInput)
	}
}

func TestDueThisWeekQuestionUsesFastPath(t *testing.T) {
	const query = "List jiras that are due this week?"
	provider := &recordingProvider{fail: errors.New("due-this-week query must not call the LLM")}
	tr := newRoutingTranslator(t, provider)

	result, err := tr.Translate(context.Background(), query)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := `due >= startOfWeek() AND due < startOfWeek("+1")`
	if result.Route != RouteFastPath {
		t.Errorf("route = %q, want %q", result.Route, RouteFastPath)
	}
	if result.JQL != want {
		t.Errorf("JQL = %q, want %q", result.JQL, want)
	}
	if result.Input != query {
		t.Errorf("input = %q, want exact input %q", result.Input, query)
	}
	if result.NormalizedInput != `List jiras that are due >= startOfWeek() AND due < startOfWeek("+1")?` {
		t.Errorf("normalized input = %q, want %q", result.NormalizedInput, `List jiras that are due >= startOfWeek() AND due < startOfWeek("+1")?`)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Errorf("provider called %d times, want 0", got)
	}
}

func TestStoryPointsQueryUsesFastPath(t *testing.T) {
	result, err := ParsePlan("list all my work items with story points > 3?", "", time.Now())
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if result.Filter == nil || len(result.Filter.Children) == 0 {
		t.Fatalf("Story Points plan = %+v", result)
	}
	last := result.Filter.Children[len(result.Filter.Children)-1]
	if last.Field != `"Story Points"` || last.Operator != ">" || last.Value != "3" {
		t.Fatalf("Story Points predicate = %+v", last)
	}
}

func TestStoryPointsTranslationRequiresMetadata(t *testing.T) {
	provider := &recordingProvider{jql: supportedStatusPlan}
	tr := newRoutingTranslator(t, provider)
	_, err := tr.Translate(context.Background(), "my work items with story points > 3")
	if !errors.Is(err, fieldmeta.ErrMissingMetadata) {
		t.Fatalf("Translate error=%v, want missing field metadata", err)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("provider calls=%d want 0", got)
	}
}

func TestOrderByQueriesUseFastPath(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{
			query: "list all my open work items ordered by story points ascending.",
			want:  `statusCategory != Done AND assignee = currentUser() ORDER BY cf[10001] ASC`,
		},
		{
			query: "my bugs sorted by priority desc",
			want:  `issuetype = Bug AND assignee = currentUser() ORDER BY priority DESC`,
		},
		{
			query: "open tasks order by creation date ascending",
			want:  `issuetype = Task AND statusCategory != Done ORDER BY created ASC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			provider := &recordingProvider{fail: errors.New("ORDER BY query must not call the LLM")}
			fb, err := NewFallbackEngine(provider)
			if err != nil {
				t.Fatal(err)
			}
			opts := Options{}
			if strings.Contains(tt.query, "story points") {
				opts.Fields = []fieldmeta.Field{{ID: "customfield_10001", Name: "Story Points", Type: "number"}}
			}
			tr, err := NewTranslator(NewNormalizer(), fb, opts)
			if err != nil {
				t.Fatal(err)
			}
			result, err := tr.Translate(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if result.Route != RouteFastPath {
				t.Errorf("route = %q, want %q (parse error %q)", result.Route, RouteFastPath, result.ParseError)
			}
			if result.JQL != tt.want {
				t.Errorf("JQL = %q, want %q", result.JQL, tt.want)
			}
			if got := atomic.LoadInt32(&provider.calls); got != 0 {
				t.Errorf("provider called %d times, want 0", got)
			}
		})
	}
}

func TestInvalidOrderByQueriesFallBack(t *testing.T) {
	for _, query := range []string{
		"my issues ordered by sprint ascending",
		"my issues ordered by priority upwards",
	} {
		t.Run(query, func(t *testing.T) {
			provider := &recordingProvider{jql: `{"version":1,"outcome":"unsupported"}`}
			tr := newRoutingTranslator(t, provider)
			result, err := tr.Translate(context.Background(), query)
			if translationerror.KindOf(err) != translationerror.Unsupported || result.JQL != "" {
				t.Fatalf("Translate result=%+v err=%v; want unsupported with empty JQL", result, err)
			}
			if result.Route != RouteFallback {
				t.Errorf("route = %q, want %q", result.Route, RouteFallback)
			}
			if result.ParseError == "" {
				t.Error("ParseError is empty, want diagnostic for fallback")
			}
			if result.Input != query || result.NormalizedInput != query {
				t.Errorf("input/normalized = %q/%q, want original query %q", result.Input, result.NormalizedInput, query)
			}
			if got := atomic.LoadInt32(&provider.calls); got != 1 {
				t.Errorf("provider called %d times, want 1", got)
			}
		})
	}
}

func TestMalformedStoryPointsComparisonFallsBack(t *testing.T) {
	for _, query := range []string{"my work items with story points >", "my work items with story points > -3", "my work items with story points > many"} {
		t.Run(query, func(t *testing.T) {
			provider := &recordingProvider{jql: `{"version":1,"outcome":"unsupported"}`}
			tr := newRoutingTranslator(t, provider)
			result, err := tr.Translate(context.Background(), query)
			if translationerror.KindOf(err) != translationerror.Unsupported || result.JQL != "" {
				t.Fatalf("Translate result=%+v err=%v; want unsupported with empty JQL", result, err)
			}
			if result.Route != RouteFallback {
				t.Errorf("route = %q, want %q", result.Route, RouteFallback)
			}
			if result.ParseError == "" || result.Input != query || result.NormalizedInput != query {
				t.Errorf("diagnostics=%+v; want parse error and original normalized input", result)
			}
			if got := atomic.LoadInt32(&provider.calls); got != 1 {
				t.Errorf("provider called %d times, want 1", got)
			}
		})
	}
}

func TestRecommendationNormalizationIsNarrow(t *testing.T) {
	const query = "Which of my open jiras should I work on next sprint?"
	provider := &recordingProvider{jql: `{"version":1,"outcome":"unsupported"}`}
	tr := newRoutingTranslator(t, provider)

	result, err := tr.Translate(context.Background(), query)
	if translationerror.KindOf(err) != translationerror.Unsupported || result.JQL != "" {
		t.Fatalf("Translate result=%+v err=%v; want unsupported with empty JQL", result, err)
	}
	if result.Route != RouteFallback {
		t.Errorf("route = %q, want %q", result.Route, RouteFallback)
	}
	if result.ParseError == "" || result.Input != query || result.NormalizedInput != query {
		t.Errorf("diagnostics=%+v; want parse error and original normalized input", result)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 1 {
		t.Errorf("provider called %d times, want 1", got)
	}
	if provider.lastInput != query {
		t.Errorf("fallback input = %q, want the full query %q", provider.lastInput, query)
	}
}

func TestTranslateOpenWorkPhrases(t *testing.T) {
	t.Parallel()

	queries := []struct {
		query   string
		wantJQL string
	}{
		{"show me all open issues assigned to me that are not done", `statusCategory != Done AND assignee = currentUser()`},
		{"list my open jiras", `statusCategory != Done AND assignee = currentUser()`},
		{"issues assigned to me that are not Closed and not Canceled", `statusCategory != Done AND assignee = currentUser()`},
		{"not done", `statusCategory != Done`},
		{"not closed", `statusCategory != Done`},
		{"not canceled", `statusCategory != Done`},
		{"not cancelled", `statusCategory != Done`},
		{"unfinished work", `statusCategory != Done`},
		{"active issues", `statusCategory != Done`},
		{"anything still open", `statusCategory != Done`},
	}

	for _, tc := range queries {
		tc := tc
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			provider := &recordingProvider{
				jql:  supportedStatusPlan,
				fail: errors.New("open-work phrasing must be handled deterministically"),
			}
			tr := newRoutingTranslator(t, provider)

			result, err := tr.Translate(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("Translate(%q): %v", tc.query, err)
			}
			if result.Route != RouteFastPath {
				t.Errorf("route = %q, want %q", result.Route, RouteFastPath)
			}
			if got := atomic.LoadInt32(&provider.calls); got != 0 {
				t.Errorf("fallback provider called %d times, want 0", got)
			}
			if result.JQL != tc.wantJQL {
				t.Errorf("JQL = %q, want %q", result.JQL, tc.wantJQL)
			}
			if strings.Contains(result.JQL, `issuetype !=`) {
				t.Errorf("JQL = %q, must not replace requested status filter with issue type exclusion", result.JQL)
			}
		})
	}
}

func TestTranslateExplicitEmailAndCompoundOpenPhrase(t *testing.T) {
	t.Parallel()
	provider := &recordingProvider{
		jql:  supportedStatusPlan,
		fail: errors.New("explicit email and open status must be handled deterministically"),
	}
	tr := newRoutingTranslator(t, provider)
	result, err := tr.Translate(context.Background(), "all issues assigned to jsmith@example.com that are not closed and not resolved, in PROJ")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := `project = PROJ AND statusCategory != Done AND assignee = "jsmith@example.com"`
	if result.JQL != want {
		t.Errorf("JQL = %q, want %q", result.JQL, want)
	}
	if result.Route != RouteFastPath {
		t.Errorf("route = %q, want %q", result.Route, RouteFastPath)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Errorf("fallback provider called %d times, want 0", got)
	}
	if strings.Contains(result.JQL, " OR ") || strings.Contains(result.JQL, "resolution") {
		t.Errorf("JQL contains conflicting open-status predicate: %q", result.JQL)
	}
}

// An unsupported custom constraint still reaches fallback with normalization
// diagnostics intact; supported date bounds alone no longer require a provider.
func TestFallbackReceivesNormalizedInput(t *testing.T) {
	t.Parallel()
	p := &recordingProvider{jql: `{"version":1,"outcome":"unsupported"}`}
	tr := newRoutingTranslator(t, p)

	query := "my open bugs in SUP created in the last 3 days with custom constraint"
	result, err := tr.Translate(context.Background(), query)
	if translationerror.KindOf(err) != translationerror.Unsupported || result.JQL != "" {
		t.Fatalf("Translate result=%+v err=%v; want unsupported with empty JQL", result, err)
	}
	if result.Route != RouteFallback || p.calls != 1 || result.Input != query || result.NormalizedInput != "my open bugs in SUP created >= -3d AND created <= now() with custom constraint" || result.ParseError == "" {
		t.Errorf("result=%+v provider calls=%d; want fallback diagnostics and one call", result, p.calls)
	}
	want := "my open bugs in SUP created >= -3d AND created <= now() with custom constraint"
	if p.lastInput != want {
		t.Errorf("fallback received %q, want normalised %q", p.lastInput, want)
	}
}
