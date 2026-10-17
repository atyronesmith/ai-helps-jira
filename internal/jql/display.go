package jql

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// display.go turns relative-date expressions in a JQL string into absolute
// calendar dates, for presentation only.
//
// This is an affordance, not a validator: it does not check, guarantee, or
// repair anything about the JQL. A relative bound such as "-365d" is the
// correct thing to send to Jira, and ResolveRelativeDatesForDisplay never
// changes what is sent -- callers compute a separate string for display and
// keep executing the original. The point is narrower: "created >= -365d"
// gives a human nothing to compare against a request like "since July 1
// 2026", while "created >= 2025-10-09" is wrong (or right) at a glance.
//
// relativeDurationRe recognizes relative durations in existing JQL, including
// historical model output. Both m and M mean minutes in Jira; months require
// calendar functions. Current translation uses typed days and calendar bounds.
var relativeDurationRe = regexp.MustCompile(`-(\d+)([dwhmMy])\b`)

// relativeFuncRe matches the zero-argument JQL date functions the normalizer
// emits unparameterized: startOfDay() and startOfWeek(). Other JQL date
// functions (startOfMonth, endOfDay, endOfWeek, endOfMonth, now(), ...) remain
// unresolved here rather than being approximated.
var relativeFuncRe = regexp.MustCompile(`\b(startOfDay|startOfWeek)\(\)`)

// ResolveRelativeDatesForDisplay returns jqlStr with every recognised
// relative-date expression rewritten to an absolute "YYYY-MM-DD" date
// computed relative to now. It is a display transform only:
//
//   - It never alters anything inside a quoted string literal (matches are
//     located against a BlankQuotedLiterals copy), so a label or summary
//     that happens to contain text shaped like "-7d" is left untouched.
//   - It is not a parser: unrecognised shapes (quoted relative dates,
//     non-zero-argument date functions, units other than d/w/h/m/M/y) pass
//     through unchanged rather than being guessed at.
//   - The returned string is for a human to read. Callers must keep using
//     the original jqlStr for anything sent to Jira.
func ResolveRelativeDatesForDisplay(jqlStr string, now time.Time) string {
	if jqlStr == "" {
		return jqlStr
	}
	blanked := BlankQuotedLiterals(jqlStr)

	type span struct {
		start, end int
		repl       string
	}
	var spans []span

	for _, m := range relativeDurationRe.FindAllStringSubmatchIndex(blanked, -1) {
		n, err := strconv.Atoi(blanked[m[2]:m[3]])
		if err != nil {
			continue
		}
		unit := blanked[m[4]:m[5]]
		abs, ok := absoluteDate(now, n, unit)
		if !ok {
			continue
		}
		spans = append(spans, span{m[0], m[1], abs})
	}

	for _, m := range relativeFuncRe.FindAllStringSubmatchIndex(blanked, -1) {
		var abs string
		switch blanked[m[2]:m[3]] {
		case "startOfDay":
			abs = now.Format("2006-01-02")
		case "startOfWeek":
			abs = startOfWeek(now).Format("2006-01-02")
		default:
			continue
		}
		spans = append(spans, span{m[0], m[1], abs})
	}

	if len(spans) == 0 {
		return jqlStr
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var b strings.Builder
	last := 0
	for _, sp := range spans {
		if sp.start < last {
			continue // defensive: overlapping matches should not occur
		}
		b.WriteString(jqlStr[last:sp.start])
		b.WriteString(sp.repl)
		last = sp.end
	}
	b.WriteString(jqlStr[last:])
	return b.String()
}

// absoluteDate computes now shifted back by n units. ok is false for a unit
// this function does not recognise.
//
// Sub-day units render with a clock time. A one-minute window formatted as a
// bare date looks like a perfectly ordinary "today", which would defeat the
// purpose -- the reader needs to see 14:30 to notice the window is a minute
// wide and not a month.
func absoluteDate(now time.Time, n int, unit string) (abs string, ok bool) {
	var t time.Time
	withClock := false
	switch unit {
	case "d":
		t = now.AddDate(0, 0, -n)
	case "w":
		t = now.AddDate(0, 0, -n*7)
	case "y":
		t = now.AddDate(-n, 0, 0)
	case "h":
		t, withClock = now.Add(-time.Duration(n)*time.Hour), true
	case "m", "M":
		// Both are minutes. Jira matches the unit case-insensitively and has
		// no month unit, so "-1M" is a sixty-second window, not thirty days.
		t, withClock = now.Add(-time.Duration(n)*time.Minute), true
	default:
		return "", false
	}
	if withClock {
		return t.Format("2006-01-02 15:04"), true
	}
	return t.Format("2006-01-02"), true
}

// startOfWeek returns the most recent Monday on or before now, matching the
// Monday-start convention daysSinceWeekday uses elsewhere in this project
// (internal/nl2jql/normalizer.go).
func startOfWeek(now time.Time) time.Time {
	offset := (int(now.Weekday()) + 6) % 7 // Monday -> 0, Sunday -> 6
	return now.AddDate(0, 0, -offset)
}
