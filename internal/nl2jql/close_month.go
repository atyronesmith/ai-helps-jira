package nl2jql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const monthWords = `january|february|march|april|may|june|july|august|september|october|november|december`

var historyFrame = regexp.MustCompile(`(?i)\b(?:i closed|did i close|closed by me) in ((?:either )?(?:` + monthWords + `)(?: or (?:` + monthWords + `))*) (?:of )?(this year|[0-9]{4})\b`)
var closeWords = regexp.MustCompile(`(?i)\b(?:i closed|did i close|closed by|closed in|were closed|was closed)\b`)
var englishMonth = map[string]time.Month{
	"january": time.January, "february": time.February, "march": time.March,
	"april": time.April, "may": time.May, "june": time.June, "july": time.July,
	"august": time.August, "september": time.September, "october": time.October,
	"november": time.November, "december": time.December,
}

// parseHistoryPlan combines actor, transition, calendar periods, and ordinary
// filters. It does not confuse a current assignee with a transition actor, or
// current Closed status with having closed an issue during a period.
func parseHistoryPlan(input, self string, now time.Time) (*QueryPlan, bool, error) {
	m := historyFrame.FindStringSubmatchIndex(input)
	if m == nil {
		if closeWords.MatchString(input) {
			return nil, true, fmt.Errorf("%w: specify the closing actor and calendar period", ErrAmbiguousIntent)
		}
		return nil, false, nil
	}
	yearText := input[m[4]:m[5]]
	year := now.Year()
	if !strings.EqualFold(yearText, "this year") {
		year, _ = strconv.Atoi(yearText)
	}
	if year < 1 || year > 9998 {
		return nil, true, fmt.Errorf("%w: invalid history year", ErrAmbiguousIntent)
	}
	names := strings.Fields(strings.TrimPrefix(strings.ToLower(input[m[2]:m[3]]), "either "))
	if len(names) == 1 && strings.HasPrefix(strings.ToLower(input[m[2]:m[3]]), "either ") {
		return nil, true, ErrAmbiguousIntent
	}
	rest := strings.TrimSpace(input[:m[0]] + " " + input[m[1]:])
	ast, err := jqlParser.ParseString("", rest)
	if err != nil {
		return nil, true, fmt.Errorf("%w: unsupported constraint beside closing history: %v", ErrAmbiguousIntent, err)
	}
	plan, err := ast.Plan(self, now)
	if err != nil && err != ErrNoConstraints {
		return nil, true, err
	}
	if plan == nil {
		plan = &QueryPlan{Self: self, Mode: ModeList}
	}
	seen := map[time.Month]bool{}
	var alternatives []*Intent
	for _, name := range names {
		if name == "or" {
			continue
		}
		month := englishMonth[name]
		if seen[month] {
			continue
		}
		seen[month] = true
		start := time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
		end := start.AddDate(0, 1, 0)
		alternatives = append(alternatives, &Intent{Kind: IntentHistory, History: &HistoryIntent{Status: "Closed", Actor: OwnerIntent{Self: true}, Start: start.Format("2006-01-02 15:04"), End: end.Format("2006-01-02 15:04")}})
	}
	plan.Filter = joinIntent(IntentAll, plan.Filter, joinIntent(IntentAny, alternatives...))
	return plan, true, nil
}
