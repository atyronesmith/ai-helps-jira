package nl2jql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// closedMonthQuery recognizes a deliberately small set of list/count requests
// whose wording makes the actor and transition clear. Keep it anchored:
// broader activity requests belong to the strict parser or LLM fallback.
var closedMonthQuery = regexp.MustCompile(`(?i)^(?:(?:list|show|get|find|count) (?:all )?(?:jiras|jira issues|issues) (?:that )?i closed|how many (?:jiras|jira issues|issues) did i close) in ((?:either )?(?:january|february|march|april|may|june|july|august|september|october|november|december)(?: or (?:january|february|march|april|may|june|july|august|september|october|november|december))*) (?:of )?(this year|\d{4})\?$`)

var englishMonth = map[string]time.Month{
	"january": time.January, "february": time.February, "march": time.March,
	"april": time.April, "may": time.May, "june": time.June,
	"july": time.July, "august": time.August, "september": time.September,
	"october": time.October, "november": time.November, "december": time.December,
}

// translateClosedMonthQuery compiles the recognized request to a Jira history
// predicate. DURING uses a half-open calendar interval, and BY binds the
// transition actor to the current Jira user.
func translateClosedMonthQuery(query string, now time.Time) (string, bool) {
	query = strings.Join(strings.Fields(query), " ")
	m := closedMonthQuery.FindStringSubmatch(query)
	if m == nil {
		return "", false
	}
	monthNames := strings.Fields(strings.TrimPrefix(strings.ToLower(m[1]), "either "))
	if len(monthNames)%2 == 0 || (strings.HasPrefix(strings.ToLower(m[1]), "either ") && len(monthNames) < 3) {
		return "", false
	}
	months := make([]time.Month, 0, (len(monthNames)+1)/2)
	for i, name := range monthNames {
		if i%2 == 1 {
			if name != "or" {
				return "", false
			}
			continue
		}
		month, ok := englishMonth[name]
		if !ok {
			return "", false
		}
		// Keep the user's order while avoiding duplicate OR branches.
		duplicate := false
		for _, seen := range months {
			if seen == month {
				duplicate = true
				break
			}
		}
		if !duplicate {
			months = append(months, month)
		}
	}
	yearText := m[2]
	if strings.EqualFold(yearText, "this year") {
		yearText = strconv.Itoa(now.Year())
	}
	year, err := strconv.Atoi(yearText)
	if err != nil || year < 1 || year > 9999 {
		return "", false
	}
	clauses := make([]string, 0, len(months))
	for _, month := range months {
		start := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0)
		clauses = append(clauses, fmt.Sprintf(`status WAS Closed BY currentUser() DURING ("%s", "%s")`,
			start.Format("2006/01/02"), end.Format("2006/01/02")))
	}
	if len(clauses) == 1 {
		return clauses[0], true
	}
	return "(" + strings.Join(clauses, " OR ") + ")", true
}
