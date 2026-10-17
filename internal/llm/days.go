package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

var digestWindow = regexp.MustCompile(`(?i)\b(?:(?:in|over|for|within|during|from)\s+(?:the\s+)?)?(?:last|past)\s+(?:(\d+|one|a|an|few|several|couple(?:\s+of)?)\s+)?(days?|weeks?|months?|sprints?)\b`)
var issueDatePrefix = regexp.MustCompile(`(?i)\b(created|updated|resolved|due|creation(?:\s+date)?|resolution(?:\s+date)?)(?:\s+(?:in|over|during|within|the|on|after|before|since|at|date|were|was|are|is))*\s*$`)
var digestCalendar = regexp.MustCompile(`(?i)\b(?:yesterday|today|this\s+(?:week|month)|since\s+\w+)\b`)
var digestFraming = regexp.MustCompile(`(?i)^(?:show\s+(?:me\s+)?)?(?:comments|comment\s+activity|activity)(?:\s+(?:on|for|of))?\s+`)

// splitDigestQuery separates rolling comment windows from issue date filters.
// Days cannot encode a calendar period; those requests require clarification.
func splitDigestQuery(query string) (string, int, error) {
	q := strings.Join(strings.Fields(query), " ")
	matches := digestWindow.FindAllStringSubmatchIndex(q, -1)
	days := 0
	for n := len(matches) - 1; n >= 0; n-- {
		m := matches[n]
		if issueDatePrefix.MatchString(q[:m[0]]) {
			continue
		}
		unit := strings.ToLower(q[m[4]:m[5]])
		if strings.HasPrefix(unit, "month") || strings.HasPrefix(unit, "sprint") {
			return "", 0, fmt.Errorf("%w: comment windows require an exact rolling day/week duration", nl2jql.ErrAmbiguousIntent)
		}
		count := 1
		if m[2] >= 0 {
			token := strings.ToLower(q[m[2]:m[3]])
			if token != "one" && token != "a" && token != "an" {
				var err error
				count, err = strconv.Atoi(token)
				if err != nil {
					return "", 0, fmt.Errorf("%w: specify an exact comment-window duration", nl2jql.ErrAmbiguousIntent)
				}
			}
		}
		if count <= 0 || count > 100000 {
			return "", 0, fmt.Errorf("%w: invalid comment-window duration", nl2jql.ErrAmbiguousIntent)
		}
		if strings.HasPrefix(unit, "week") {
			count *= 7
		}
		if days != 0 {
			return "", 0, fmt.Errorf("%w: multiple comment windows", nl2jql.ErrAmbiguousIntent)
		}
		days = count
		q = strings.TrimSpace(q[:m[0]] + " " + q[m[1]:])
	}
	for _, m := range digestCalendar.FindAllStringIndex(q, -1) {
		if !issueDatePrefix.MatchString(q[:m[0]]) {
			return "", 0, fmt.Errorf("%w: calendar comment windows cannot be represented as rolling days", nl2jql.ErrAmbiguousIntent)
		}
	}
	q = digestFraming.ReplaceAllString(q, "")
	return strings.Join(strings.Fields(q), " "), days, nil
}

// ExtractDays reports only unambiguous rolling comment windows. Translation
// uses splitDigestQuery directly so ambiguity is returned rather than ignored.
func ExtractDays(query string) int {
	_, days, err := splitDigestQuery(query)
	if err != nil {
		return 0
	}
	return days
}
