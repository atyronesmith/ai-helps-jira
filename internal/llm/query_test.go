package llm

import (
	"strings"
	"testing"
)

// TestQuerySystemPrompt_DoesNotImplyMonthUnit pins the wording fix for
// FIELDCTO-2817: the prompt's relative-dates example list used to read
// "-1w" (one week ago), "-1d" (one day ago), "-2w", "-1m", etc. -- two
// glossed examples followed by an unglossed "-1m" that a model naturally
// reads as "one month" by false progression. Jira's JQL grammar has no
// month unit: lowercase and uppercase "m"/"M" both mean minutes, so a model
// that takes the bait emits a query whose "last month" window is actually
// about a minute wide, with no error at any layer.
//
// This test only pins the prompt's TEXT. It cannot verify that a model
// reading the prompt will in fact avoid emitting "-1m" for "last month" --
// a prompt is an instruction, not code, and no unit test can make a model
// comply. What it protects against is a silent textual regression: if
// someone reintroduces a bare, unglossed "-1m" into the relative-dates
// examples (e.g. "cleaning up" the wording back to the old list), this test
// fails. The behavioral half of this fix -- confirming m/M really do mean
// minutes -- is pinned separately in internal/jql/display_test.go against
// Jira's actual (verified) semantics, and the oracle suite exercises it
// against the live tenant.
func TestQuerySystemPrompt_DoesNotImplyMonthUnit(t *testing.T) {
	prompt := QuerySystemPrompt

	// A bare, unglossed "-1m" is exactly the shape that misled a reader into
	// thinking "m" means "month" by analogy with the glossed "-1w"/"-1d"
	// examples next to it. Every sentence that mentions "-1m" or "-1M" must
	// itself say "minute" somewhere, so it cannot be read in isolation as
	// implying a month.
	for _, sentence := range strings.Split(prompt, ".") {
		lower := strings.ToLower(sentence)
		if (strings.Contains(sentence, `-1m`) || strings.Contains(sentence, `-1M`)) && !strings.Contains(lower, "minute") {
			t.Errorf("QuerySystemPrompt mentions -1m/-1M without the word \"minute\" in the same sentence: %q", strings.TrimSpace(sentence))
		}
	}

	// The prompt must say in some form that there is no month unit, so a
	// model cannot come away believing "m" or "M" means month.
	if !strings.Contains(prompt, "no month unit") {
		t.Error(`QuerySystemPrompt must explicitly state JQL has "no month unit"`)
	}

	// The prompt must point the model at a correct alternative for a
	// calendar month (startOfMonth/endOfMonth) and/or a rolling window
	// (days or weeks), not leave "last month" unanswered.
	if !strings.Contains(prompt, "startOfMonth()") || !strings.Contains(prompt, "endOfMonth()") {
		t.Error("QuerySystemPrompt must mention startOfMonth()/endOfMonth() as the calendar-month alternative")
	}
}

func TestQuerySystemPromptQuotesExplicitEmails(t *testing.T) {
	for _, required := range []string{"quote the assignee value", "reserved JQL punctuation", `assignee = "jsmith@example.com"`} {
		if !strings.Contains(QuerySystemPrompt, required) {
			t.Errorf("QuerySystemPrompt is missing %q", required)
		}
	}
}
