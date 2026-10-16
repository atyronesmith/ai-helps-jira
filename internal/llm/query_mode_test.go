package llm

import "testing"

func TestIsCountQuery(t *testing.T) {
	for _, input := range []string{"how many open issues do I have?", " HOW MANY bugs?", "count my open tasks", "Count\tmy tickets"} {
		if !IsCountQuery(input) {
			t.Errorf("count request %q was not recognized", input)
		}
	}
	for _, input := range []string{"show issues about a count", "countdown bugs", "my tickets", "", "how manyish issues"} {
		if IsCountQuery(input) {
			t.Errorf("list request %q was classified as count", input)
		}
	}
}
