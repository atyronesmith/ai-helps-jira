package nl2jql

import (
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	projects := map[string]string{
		"Support":      "SUP",
		"Engineering":  "ENG",
		"Eng Platform": "EPL",
	}

	n := NewNormalizer()
	// Pin the clock to Tuesday 2026-09-08 so "last Tuesday" deterministically
	// resolves to exactly one week back, reproducing the requested mapping
	// "created last Tuesday" -> "created >= -7d".
	n.Now = func() time.Time { return time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC) }

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "exact due this week question is narrowly canonicalized",
			input: "List jiras that are due this week?",
			want:  `List jiras that are due >= startOfWeek() AND due < startOfWeek("+1")?`,
		},
		{
			name:  "hero: fuzzy project plus counted days",
			input: "my open bugs in Spport created in the last 3 days",
			want:  "my open bugs in SUP created >= -3d AND created <= now()",
		},
		{
			name:  "requested example: last Tuesday == -7d on a Tuesday",
			input: "created last Tuesday",
			want:  `created >= startOfDay("-7") AND created < startOfDay("-6")`,
		},
		{
			name:  "weeks reduce to days",
			input: "bugs updated in the last 2 weeks",
			want:  "bugs updated >= -14d AND updated <= now()",
		},
		{
			name:  "sprints use the configured sprint length",
			input: "stories resolved over the last 2 sprints",
			want:  "stories resolved over the last 2 sprints",
		},
		{
			name:  "vague counters: a couple of weeks",
			input: "bugs created in the last couple of weeks",
			want:  "bugs created in the last couple of weeks",
		},
		{
			name:  "today and yesterday",
			input: "created today, updated yesterday",
			want:  `created >= startOfDay() AND created < startOfDay("+1"), updated >= startOfDay("-1") AND updated < startOfDay()`,
		},
		{
			name:  "this week and last week",
			input: "created this week and updated last week",
			want:  `created >= startOfWeek() AND created < startOfWeek("+1") and updated >= startOfWeek("-1") AND updated < startOfWeek()`,
		},
		{
			name:  "due date aliases collapse to canonical due field",
			input: "due date in the last 3 days",
			want:  "due >= -3d AND due <= now()",
		},
		{
			name:  "typo inside a multi-word key snaps",
			input: "bugs in Eng Plattform",
			want:  "bugs in EPL",
		},
		{
			name:  "status phrase 'in progress' never becomes a project",
			input: "bugs in progress created today",
			want:  `bugs in progress created >= startOfDay() AND created < startOfDay("+1")`,
		},
		{
			name:  "unknown project passes through untouched",
			input: "bugs in Zanzibar created today",
			want:  `bugs in Zanzibar created >= startOfDay() AND created < startOfDay("+1")`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := n.Normalize(tt.input, projects); got != tt.want {
				t.Errorf("Normalize(%q)\n got: %q\nwant: %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestNormalizeIdempotent pins a key property: running the normalizer over
// its own output must be a fixed point, so double-application is harmless.
func TestNormalizeIdempotent(t *testing.T) {
	n := NewNormalizer()
	in := "my open bugs in Spport created in the last 3 days"
	once := n.Normalize(in, map[string]string{"Support": "SUP"})
	if twice := n.Normalize(once, map[string]string{"Support": "SUP"}); once != twice {
		t.Errorf("not idempotent:\nonce: %q\ntwice: %q", once, twice)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"spport", "support", 1},
		{"eng plattform", "eng platform", 1},
		{"flaw", "lawn", 2},
	}
	for _, c := range cases {
		if got := levenshtein(strings.ToLower(c.a), strings.ToLower(c.b)); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
