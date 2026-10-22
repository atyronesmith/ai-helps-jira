package nl2jql

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestTranslateRecencyFirstOrderingIsCaseInsensitive(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("recency ordering must stay deterministic")}
	tr := newRoutingTranslator(t, provider)
	tests := []struct {
		query string
		want  string
	}{
		{"my open issues newest first", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
		{"my open issues NEWEST FIRST", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
		{"my open issues Newest first", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
		{"my open issues MOST   RECENT FIRST", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
		{"my open issues latest first", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
		{"my open issues recent first", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
		{"my open issues oldest first", `statusCategory != Done AND assignee = currentUser() ORDER BY created ASC`},
		{"my open issues LEAST RECENT FIRST", `statusCategory != Done AND assignee = currentUser() ORDER BY created ASC`},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			result, err := tr.Translate(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("Translate(%q): %v", tc.query, err)
			}
			if result.JQL != tc.want {
				t.Errorf("JQL = %q, want %q", result.JQL, tc.want)
			}
			if result.Route != RouteFastPath {
				t.Errorf("route = %q, want fast path", result.Route)
			}
		})
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("fallback provider called %d times, want 0", got)
	}
}

func TestTranslateRecencyIdiomsApplyToNamedDateFields(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("named date ordering must stay deterministic")}
	tr := newRoutingTranslator(t, provider)
	tests := []struct {
		query string
		want  string
	}{
		{"my issues ordered by created newest first", `assignee = currentUser() ORDER BY created DESC`},
		{"my issues ordered by updated newest first", `assignee = currentUser() ORDER BY updated DESC`},
		{"my issues sorted by resolved newest first", `assignee = currentUser() ORDER BY resolved DESC`},
		{"my issues order by due date newest first", `assignee = currentUser() ORDER BY due DESC`},
		{"my issues ordered by created oldest first", `assignee = currentUser() ORDER BY created ASC`},
		{"my issues sorted by updated oldest first", `assignee = currentUser() ORDER BY updated ASC`},
		{"my issues order by resolved oldest first", `assignee = currentUser() ORDER BY resolved ASC`},
		{"my issues ordered by due date oldest first", `assignee = currentUser() ORDER BY due ASC`},
		{"my issues ordered by updated MOST   RECENT FIRST", `assignee = currentUser() ORDER BY updated DESC`},
		{"my issues sorted by resolved LEAST RECENT FIRST", `assignee = currentUser() ORDER BY resolved ASC`},
		{"my issues ordered by due date from newest to oldest", `assignee = currentUser() ORDER BY due DESC`},
		{"my issues ordered by created from oldest to newest", `assignee = currentUser() ORDER BY created ASC`},
		{"my issues ordered by updated newest first, due oldest first", `assignee = currentUser() ORDER BY updated DESC, due ASC`},
		{"my open issues newest first", `statusCategory != Done AND assignee = currentUser() ORDER BY created DESC`},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			result, err := tr.Translate(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("Translate(%q): %v", tc.query, err)
			}
			if result.JQL != tc.want {
				t.Errorf("JQL = %q, want %q", result.JQL, tc.want)
			}
			if result.Route != RouteFastPath {
				t.Errorf("route = %q, want fast path", result.Route)
			}
		})
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("fallback provider called %d times, want 0", got)
	}
}

func TestTranslateNamedRecencyIdiomsRejectNonDateFields(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("ambiguous ordering must not reach fallback")}
	tr := newRoutingTranslator(t, provider)
	_, err := tr.Translate(context.Background(), "my issues ordered by priority newest first")
	if !errors.Is(err, ErrAmbiguousIntent) {
		t.Fatalf("want clarification for recency wording on a non-date field, got %v", err)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("fallback provider called %d times, want 0", got)
	}
}

func TestTranslateStatusIsNotTerminalMeansOpenCategory(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("terminal negation must stay deterministic")}
	tr := newRoutingTranslator(t, provider)
	tests := []struct {
		query string
		want  string
	}{
		{"my status is not done", `statusCategory != Done AND assignee = currentUser()`},
		{"my STATUS IS NOT CLOSED", `statusCategory != Done AND assignee = currentUser()`},
		{"my status is not resolved", `statusCategory != Done AND assignee = currentUser()`},
		{"my status is not canceled", `statusCategory != Done AND assignee = currentUser()`},
		{"my status is not CANCELLED", `statusCategory != Done AND assignee = currentUser()`},
		{"my not done", `statusCategory != Done AND assignee = currentUser()`},
		{"my status is Open", `status = Open AND assignee = currentUser()`},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			result, err := tr.Translate(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("Translate(%q): %v", tc.query, err)
			}
			if result.JQL != tc.want {
				t.Errorf("JQL = %q, want %q", result.JQL, tc.want)
			}
			if result.Route != RouteFastPath {
				t.Errorf("route = %q, want fast path", result.Route)
			}
		})
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("fallback provider called %d times, want 0", got)
	}
}

func TestRecencyAndStatusNegationNormalizationProtectTextLiteral(t *testing.T) {
	query := "my issues that contain the words newest, first, status, is, not, done"
	normalized := NewNormalizer().Normalize(query, nil)
	if normalized != query {
		t.Fatalf("Normalize(%q) = %q; text literal must remain unchanged", query, normalized)
	}
	provider := &recordingProvider{fail: errors.New("literal text query must stay deterministic")}
	tr := newRoutingTranslator(t, provider)
	result, err := tr.Translate(context.Background(), query)
	if err != nil {
		t.Fatalf("Translate(%q): %v", query, err)
	}
	if result.Route != RouteFastPath {
		t.Errorf("route = %q, want fast path", result.Route)
	}
	if result.JQL != `assignee = currentUser() AND text ~ "newest OR first OR status OR is OR \"not\" OR done"` {
		t.Errorf("JQL = %q, literal words were changed or ordering was added", result.JQL)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Errorf("fallback provider called %d times, want 0", got)
	}
}
