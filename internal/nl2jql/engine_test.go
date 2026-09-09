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
	"sync/atomic"
	"testing"
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
			wantJQL:   `project = Support AND issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved") AND assignee = currentUser()`,
		},
		{
			name:      "priority + dates-as-JQL + complex filters fall through to the LLM",
			query:     "critical bugs from last week",
			wantRoute: "fallback",
			wantCalls: 1,
			wantJQL:   "status = Open",
		},
		{
			name:      "a normalised raw-JQL fragment is too strict for the parser",
			query:     "my open bugs in SUP created >= -3d",
			wantRoute: "fallback",
			wantCalls: 1,
			wantJQL:   "status = Open",
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
			p := &recordingProvider{jql: "status = Open", fail: fail}
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
		})
	}
}

// TestFallbackReceivesNormalizedInput proves the "cleaner input than before"
// promise of the Phase-4 example: the normalizer rewrites the natural-language
// date "created in the last 3 days" into the strict JQL fragment "created >=
// -3d", which is what trips the parser and routes the query to the LLM. The
// fallback therefore receives the normalised text, not the raw query.
func TestFallbackReceivesNormalizedInput(t *testing.T) {
	t.Parallel()
	p := &recordingProvider{jql: "status = Open"}
	tr := newRoutingTranslator(t, p)

	_, err := tr.Translate(context.Background(), "my open bugs in SUP created in the last 3 days")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := "my open bugs in SUP created >= -3d"
	if p.lastInput != want {
		t.Errorf("fallback received %q, want normalised %q", p.lastInput, want)
	}
}
