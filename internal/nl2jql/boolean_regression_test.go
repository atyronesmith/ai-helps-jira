package nl2jql

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestOpenStatusBooleanRegressions(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"my open or status is Open", `(statusCategory != Done OR status = Open) AND assignee = currentUser()`},
		{"my status is Open or open", `(status = Open OR statusCategory != Done) AND assignee = currentUser()`},
		{"my open and status is Open", `status = Open AND assignee = currentUser()`},
		{"my status is Open and open", `status = Open AND assignee = currentUser()`},
		{"my open status is Open", `status = Open AND assignee = currentUser()`},
		{"my status is open and open", `status = open AND assignee = currentUser()`},
		{"my open or done or status is Open", `(statusCategory != Done OR status = Done OR status = Open) AND assignee = currentUser()`},
		{"my (open or status is Open)", `assignee = currentUser() AND (statusCategory != Done OR status = Open)`},
		{"my ((open and status is Open) or (done))", `assignee = currentUser() AND (status = Open OR status = Done)`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			assertDeterministicBooleanTranslation(t, tc.input, tc.want)
		})
	}
}

func TestLabelBooleanRegressions(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"my labeled alpha and labeled beta", `assignee = currentUser() AND labels = alpha AND labels = beta`},
		{"my labeled alpha labeled beta", `assignee = currentUser() AND labels = alpha AND labels = beta`},
		{"my labeled alpha or labeled beta", `assignee = currentUser() AND (labels = alpha OR labels = beta)`},
		{"my labeled alpha and labeled beta or labeled gamma", `assignee = currentUser() AND (labels = alpha AND labels = beta OR labels = gamma)`},
		{"my labeled alpha or labeled beta and labeled gamma", `assignee = currentUser() AND (labels = alpha OR labels = beta AND labels = gamma)`},
		{"my labeled alpha and (labeled beta or labeled gamma)", `assignee = currentUser() AND labels = alpha AND (labels = beta OR labels = gamma)`},
		{"my not (labeled alpha or labeled beta)", `assignee = currentUser() AND NOT (labels = alpha OR labels = beta)`},
		{"my (labeled alpha or labeled beta and not labeled gamma)", `assignee = currentUser() AND (labels = alpha OR labels = beta AND NOT labels = gamma)`},
		{"my (labeled alpha or (labeled beta and not labeled gamma))", `assignee = currentUser() AND (labels = alpha OR labels = beta AND NOT labels = gamma)`},
		{"my (labeled alpha or labeled beta and (open))", `assignee = currentUser() AND (labels = alpha OR labels = beta AND statusCategory != Done)`},
		{"my (labeled alpha or labeled beta and updated today)", `assignee = currentUser() AND (labels = alpha OR labels = beta AND updated >= startOfDay() AND updated < startOfDay("+1"))`},
		{"my ((labeled alpha or labeled beta) and not labeled gamma)", `assignee = currentUser() AND (labels = alpha OR labels = beta) AND NOT labels = gamma`},
		{"my open labeled alpha and labeled beta newest first", `statusCategory != Done AND assignee = currentUser() AND labels = alpha AND labels = beta ORDER BY created DESC`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			assertDeterministicBooleanTranslation(t, tc.input, tc.want)
		})
	}
}

func TestUngroupedMixedLabelAlternativesRequireClarification(t *testing.T) {
	for _, input := range []string{
		"my labeled alpha or labeled beta and not labeled gamma",
		"my labeled alpha or labeled beta and (open)",
		"my labeled alpha or labeled beta and updated today",
	} {
		t.Run(input, func(t *testing.T) {
			provider := &recordingProvider{fail: errors.New("ambiguous query must not reach fallback")}
			tr := newRoutingTranslator(t, provider)
			_, err := tr.Translate(context.Background(), input)
			if !errors.Is(err, ErrAmbiguousIntent) {
				t.Fatalf("want clarification, got %v", err)
			}
			if atomic.LoadInt32(&provider.calls) != 0 {
				t.Fatal("ambiguous query reached provider")
			}
		})
	}
}

func TestStatusConjunctionStillRequiresClarification(t *testing.T) {
	for _, input := range []string{
		"my open and done",
		"my open or done and status is Open",
		"my status is Closed and open",
		"my labeled alpha and open and labeled beta",
	} {
		t.Run(input, func(t *testing.T) {
			provider := &recordingProvider{fail: errors.New("must not guess ambiguous meaning")}
			tr := newRoutingTranslator(t, provider)
			_, err := tr.Translate(context.Background(), input)
			if !errors.Is(err, ErrAmbiguousIntent) {
				t.Fatalf("want clarification, got %v", err)
			}
			if atomic.LoadInt32(&provider.calls) != 0 {
				t.Fatal("ambiguous query reached provider")
			}
		})
	}
}

func assertDeterministicBooleanTranslation(t *testing.T, input, want string) {
	t.Helper()
	provider := &recordingProvider{fail: errors.New("must be deterministic")}
	tr := newRoutingTranslator(t, provider)
	result, err := tr.Translate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.JQL != want {
		t.Fatalf("got %s\nwant %s", result.JQL, want)
	}
	if result.Route != RouteFastPath || atomic.LoadInt32(&provider.calls) != 0 {
		t.Fatalf("expected deterministic translation, got %+v", result)
	}
}
