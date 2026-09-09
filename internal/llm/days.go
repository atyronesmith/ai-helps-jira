// days.go extracts an activity/comment window ("Days") from a natural-language
// digest query without an LLM call. This is the "option 2" of the Phase-2 plan
// for gap B: the deterministic nl2jql pipeline returns JQL only, so the comment
// window the digest handler uses to cut the comment stream is recovered with a
// cheap regex instead of a second model call. Callers fall back to the original
// LLM JSON path (option 1) when ExtractDays returns 0.
package llm

import (
	"regexp"
	"strconv"
	"strings"
)

// Activity-window units expressed in days. JIRA JQL has no week/month/sprint
// unit, and the digest "days" field is a comment-activity window (see
// QuerySystemPrompt rule #5), so every unit is reduced to a day count.
const (
	daysPerDay    = 1
	daysPerWeek   = 7
	daysPerMonth  = 30
	daysPerSprint = 14
)

// countWindowRe matches a *counted* activity window such as "in the last 3
// days", "past 2 weeks", "over the past month", "last few days", "since 3
// days", or "a couple of weeks". It is field-agnostic on purpose: the digest
// window describes comment activity, not a created/updated/resolved date, so no
// date-field prefix is required. A window lead-in (last/past/in the/...) is
// mandatory so a bare "3 days" elsewhere in the text is not mistaken for one.
//
// Named capture groups, in order: lead=1, count=2, unit=3.
var countWindowRe = regexp.MustCompile(
	`(?i)\b(?P<lead>in[ \t]+the[ \t]+|over[ \t]+the[ \t]+|within[ \t]+the[ \t]+|during[ \t]+the[ \t]+|for[ \t]+the[ \t]+|from[ \t]+the[ \t]+|since[ \t]+|last[ \t]+|past[ \t]+|recently[ \t]+|recent[ \t]+)` +
		`(?P<count>\d+|a[ \t]+few|a[ \t]+couple[ \t]+of|couple[ \t]+of|a[ \t]+couple|few|several|some|one|couple|a|an)` +
		`[ \t]+(?:of[ \t]+)?` +
		`(?P<unit>sprints?|days?|weeks?|months?)\b`)

// bareWindowRe matches a window with an implied count of one: "last week",
// "this week", "past day", "in the last day".
//
// Named capture groups, in order: lead=1, unit=2.
var bareWindowRe = regexp.MustCompile(
	`(?i)\b(?P<lead>in[ \t]+the[ \t]+|this[ \t]+|last[ \t]+|past[ \t]+|over[ \t]+the[ \t]+|within[ \t]+the[ \t]+|for[ \t]+the[ \t]+)` +
		`(?P<unit>sprints?|days?|weeks?|months?)\b`)

// yesterdayRe matches the "yesterday" one-day window.
var yesterdayRe = regexp.MustCompile(`(?i)\byesterday\b`)

// ExtractDays detects an activity/comment window in a natural-language digest
// query and returns the number of days it represents, or 0 when none is found.
//
// Examples: "in the last 3 days" -> 3, "past 2 weeks" -> 14, "last week" -> 7,
// "in the last day" -> 1, "yesterday" -> 1. It is deterministic and LLM-free;
// callers (GenerateJQLForDigest) fall back to the original LLM JSON path when
// this returns 0 so a window the regex cannot parse is still recovered.
func ExtractDays(query string) int {
	q := strings.Join(strings.Fields(query), " ")
	if q == "" {
		return 0
	}

	// 1) Counted window: "in the last 3 days", "past 2 weeks", "a few days".
	if m := countWindowRe.FindStringSubmatch(q); m != nil {
		count := vagueCount(m[2])
		if days := windowUnitDays(m[3]) * count; days > 0 {
			return days
		}
	}

	// 2) Bare unit window: "last week", "this week", "in the last day".
	if m := bareWindowRe.FindStringSubmatch(q); m != nil {
		if days := windowUnitDays(m[2]); days > 0 {
			return days
		}
	}

	// 3) "yesterday" -> one day.
	if yesterdayRe.MatchString(q) {
		return 1
	}

	return 0
}

// windowUnitDays reduces a plural unit token ("days", "weeks", "sprints",
// "months") to its day count. Unknown units yield 0 so the caller falls back.
func windowUnitDays(unit string) int {
	switch strings.ToLower(strings.TrimSuffix(unit, "s")) {
	case "day":
		return daysPerDay
	case "week":
		return daysPerWeek
	case "month":
		return daysPerMonth
	case "sprint":
		return daysPerSprint
	}
	return 0
}

// vagueCount maps a captured count token to a number. Digits parse directly; the
// common vague counters collapse to 1/2/3. Unknown tokens yield 0 so the caller
// falls back to the LLM path rather than guessing.
func vagueCount(token string) int {
	t := strings.ToLower(strings.Join(strings.Fields(token), " "))
	if n, err := strconv.Atoi(t); err == nil {
		return n
	}
	switch t {
	case "one", "a", "an":
		return 1
	case "couple", "couple of", "a couple", "a couple of":
		return 2
	case "few", "a few", "several", "some":
		return 3
	}
	return 0
}
