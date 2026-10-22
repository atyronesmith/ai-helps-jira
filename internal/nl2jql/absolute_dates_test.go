package nl2jql

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeAbsoluteDateRanges(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "between form with quoted endpoints and prefix tense",
			input: `issues that were UPDATED between "2024-02-28" and "2024-02-29" in PROJ`,
			want:  `issues that updated >= "2024-02-28" AND updated < "2024-03-01" in PROJ`,
		},
		{
			name:  "from form with field tense and due date alias",
			input: `my due date was from 2024-12-31 to 2025-01-01`,
			want:  `my due >= "2024-12-31" AND due < "2025-01-02"`,
		},
		{
			name:  "resolved field and mixed quote styles",
			input: `resolved between 2026-01-01 and "2026-01-31"`,
			want:  `resolved >= "2026-01-01" AND resolved < "2026-02-01"`,
		},
		{
			name:  "whitespace and case variations",
			input: "issues were   created   from 2024-02-28   to 2024-02-29",
			want:  `issues created >= "2024-02-28" AND created < "2024-03-01"`,
		},
		{
			name:  "grouped range with sentence punctuation",
			input: `(updated between "2024-02-28" and 2024-02-29.)`,
			want:  `(updated >= "2024-02-28" AND updated < "2024-03-01")`,
		},
		{
			name:  "invalid leap day stays untouched",
			input: `created between 2023-02-29 and 2023-03-01`,
			want:  `created between 2023-02-29 and 2023-03-01`,
		},
		{
			name:  "timestamp is not truncated to a date",
			input: `updated between 2026-01-01T10:30 and 2026-01-02`,
			want:  `updated between 2026-01-01T10:30 and 2026-01-02`,
		},
		{
			name:  "unquoted start date-time is not truncated",
			input: `updated between 2026-01-01 10:30 and 2026-01-02`,
			want:  `updated between 2026-01-01 10:30 and 2026-01-02`,
		},
		{
			name:  "unquoted end date-time is not partially normalized",
			input: `updated between 2026-01-01 and 2026-01-02 10:30`,
			want:  `updated between 2026-01-01 and 2026-01-02 10:30`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAbsoluteDateRanges(tc.input); got != tc.want {
				t.Errorf("normalizeAbsoluteDateRanges(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestValidateAbsoluteDateRanges(t *testing.T) {
	valid := []string{
		`created between 2024-02-28 and 2024-02-29`,
		`updated from "2024-12-31" to "2025-01-01"`,
		`resolved between 0001-01-01 and 9999-12-30`,
		`created from oldest to newest`,
		`created from newest to oldest`,
	}
	for _, input := range valid {
		t.Run("valid "+input, func(t *testing.T) {
			if err := validateAbsoluteDateRanges(input); err != nil {
				t.Fatalf("validateAbsoluteDateRanges(%q): %v", input, err)
			}
		})
	}
	invalid := []string{
		`created between 2023-02-29 and 2023-03-01`,
		`created between 2024-02-30 and 2024-03-01`,
		`updated between 2026-02-02 and 2026-02-01`,
		`due from 9999-12-30 to 9999-12-31`,
		`updated between 2026-01-01T12:00 and 2026-01-02`,
		`updated between 2026-01-01 12:00 and 2026-01-02`,
		`updated between 2026-01-01 and 2026-01-02 12:00`,
		`updated between "2026-01-01 12:00" and 2026-01-02`,
		`updated between "2026-01-01" 12:00 and 2026-01-02`,
		`updated between 2026-01-01suffix and 2026-01-02`,
		`updated between "2026-01-01." and 2026-01-02`,
		`created between "2026-01-01 and 2026-01-02`,
		`created between 2026-1-01 and 2026-01-02`,
		`updated from 2026-01-01 and 2026-01-02`,
		`updated between 2026-01-01 to 2026-01-02`,
	}
	for _, input := range invalid {
		t.Run("invalid "+input, func(t *testing.T) {
			if err := validateAbsoluteDateRanges(input); !errors.Is(err, ErrAmbiguousIntent) {
				t.Fatalf("validateAbsoluteDateRanges(%q) = %v, want ErrAmbiguousIntent", input, err)
			}
		})
	}
}

func TestAbsoluteDateRangeTranslatorRegressions(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("explicit date ranges must stay deterministic")}
	tr := newRoutingTranslator(t, provider)
	tests := []struct {
		query string
		want  string
	}{
		{
			query: `issues that were updated between 2026-02-27 and 2026-03-01 in PROJ`,
			want:  `project = PROJ AND updated >= "2026-02-27" AND updated < "2026-03-02"`,
		},
		{
			query: `my open issues created from "2024-02-28" to "2024-02-29"`,
			want:  `statusCategory != Done AND assignee = currentUser() AND created >= "2024-02-28" AND created < "2024-03-01"`,
		},
		{
			query: `issues due date was between 2024-12-31 and 2025-01-01`,
			want:  `due >= "2024-12-31" AND due < "2025-01-02"`,
		},
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
			if strings.Contains(result.JQL, "status =") {
				t.Errorf("JQL contains an injected status predicate: %q", result.JQL)
			}
		})
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("fallback provider called %d times, want 0", got)
	}
}

func TestInvalidAbsoluteDateRangeRefusesWithoutProvider(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("invalid range must not reach provider")}
	tr := newRoutingTranslator(t, provider)
	for _, query := range []string{
		`my issues updated between 2023-02-29 and 2023-03-01`,
		`issues were updated from 2026-02-02 to 2026-02-01`,
		`issues updated between 2026-01-01T12:00 and 2026-01-02`,
		`issues updated between 2026-01-01 12:00 and 2026-01-02`,
		`issues updated between 2026-01-01 and 2026-01-02 12:00`,
		`issues updated between "2026-01-01 12:00" and 2026-01-02`,
		`issues updated between 2026-10-12 and`,
		`issues updated from 2026-10-12 and 2026-10-14`,
	} {
		if _, err := tr.Translate(context.Background(), query); !errors.Is(err, ErrAmbiguousIntent) {
			t.Errorf("Translate(%q) error = %v, want ErrAmbiguousIntent", query, err)
		}
	}
	if got := atomic.LoadInt32(&provider.calls); got != 0 {
		t.Fatalf("fallback provider called %d times, want 0", got)
	}
}

func TestAbsoluteDateRangeHelpersProtectTextLiteral(t *testing.T) {
	query := `issues that contain the words updated, between, 2026-01-01, and, 2026-02-01`
	if got := normalizeAbsoluteDateRanges(query); got != query {
		t.Fatalf("normalizeAbsoluteDateRanges changed literal text: %q", got)
	}
	if err := validateAbsoluteDateRanges(query); err != nil {
		t.Fatalf("validateAbsoluteDateRanges interpreted literal text as syntax: %v", err)
	}
}

func TestChronologicalSortIsNotTreatedAsAbsoluteDateRange(t *testing.T) {
	query := "list my work items ordered by created FROM\n  OLDEST   TO newest"
	normalized := NewNormalizer().Normalize(query, nil)
	if want := "list my work items ordered by created FROM OLDEST TO newest"; normalized != want {
		t.Fatalf("Normalize = %q, want %q", normalized, want)
	}
	plan, err := ParsePlan(normalized, "", time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ParsePlan(%q): %v", normalized, err)
	}
	compiled, err := plan.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got, want := compiled.Render(), `assignee = currentUser() ORDER BY created ASC`; got != want {
		t.Errorf("compiled query = %q, want %q", got, want)
	}
}
