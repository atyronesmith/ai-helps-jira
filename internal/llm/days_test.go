package llm

import "testing"

// ExtractDays is the gap-B "option 2" extraction: a deterministic, LLM-free read of
// a comment-activity window off the natural-language query.
func TestExtractDays(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"show me bugs in the last 3 days", 3},
		{"past 2 weeks", 14},
		{"last week", 7},
		{"in the last day", 1},
		{"yesterday", 1},
		{"last couple of days", 2},
		{"last 5 sprints", 70},
		{"critical bugs", 0},
		{"no time window mentioned", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := ExtractDays(tc.query); got != tc.want {
			t.Errorf("ExtractDays(%q) = %d, want %d", tc.query, got, tc.want)
		}
	}
}
