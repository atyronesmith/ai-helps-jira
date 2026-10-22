package nl2jql

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ActivityRequest describes a personal past-work lookup. It is deliberately
// separate from QueryPlan: Jira activity can refer to comments, worklogs, or
// history, which cannot be represented faithfully as one issue-search JQL.
type ActivityRequest struct {
	Input        string
	StartDate    string
	EndDate      string
	Project      string
	YearInferred bool
}

var (
	activityMention        = regexp.MustCompile(`(?i)\b(?:i\s+work(?:ed)?\s+on|did\s+[a-z][a-z0-9_.@+-]*\s+work\s+on|[a-z][a-z0-9_.@+-]*\s+worked\s+on|my\s+work\s+activity)\b`)
	activityRecommendation = regexp.MustCompile(`(?i)\bshould\s+i\s+work\s+on\b`)
	activityRequest        = regexp.MustCompile(`(?i)^\s*(?:(?:what|which|list|show)(?:\s+me)?(?:\s+(?:jira\s+)?(?:items?|issues?|tickets?))?\s+)?(?:did\s+i\s+work\s+on|i\s+work(?:ed)?\s+on|my\s+work\s+activity)(?:\s+(?:during|between|from)\s+(?P<start>.+?)\s+(?P<join>and|to|-)\s+(?P<end>.+?))?(?:\s+(?:in|for)\s+(?:project\s+)?(?P<project>[A-Za-z][A-Za-z0-9_]*))?\s*[?!.]*\s*$`)
	activityDate           = regexp.MustCompile(`(?i)^(?:(?P<iso>\d{4}-\d{2}-\d{2})|(?P<month>jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\s+(?P<day>\d{1,2})(?:,?\s+(?P<year>\d{4}))?)$`)
	monthNumber            = map[string]time.Month{
		"jan": time.January, "january": time.January,
		"feb": time.February, "february": time.February,
		"mar": time.March, "march": time.March,
		"apr": time.April, "april": time.April,
		"may": time.May,
		"jun": time.June, "june": time.June,
		"jul": time.July, "july": time.July,
		"aug": time.August, "august": time.August,
		"sep": time.September, "september": time.September,
		"oct": time.October, "october": time.October,
		"nov": time.November, "november": time.November,
		"dec": time.December, "december": time.December,
	}
)

// ParseActivityRequest recognizes personal past-work wording before ordinary
// issue-search translation. recognized is true for unsupported variations too,
// so callers can ask for clarification without invoking an LLM rewrite.
func ParseActivityRequest(input string, now time.Time) (request ActivityRequest, recognized bool, err error) {
	request.Input = input
	masked := maskFallbackLiterals(input)
	if activityRecommendation.MatchString(masked) || !activityMention.MatchString(masked) {
		return request, false, nil
	}
	recognized = true
	match := activityRequest.FindStringSubmatch(masked)
	if match == nil {
		return request, true, fmt.Errorf("%w: specify one personal activity request with a supported date range and optional project", ErrAmbiguousIntent)
	}
	groups := make(map[string]string)
	for i, name := range activityRequest.SubexpNames() {
		if i > 0 && name != "" {
			groups[name] = strings.TrimSpace(match[i])
		}
	}
	if groups["start"] == "" || groups["end"] == "" {
		return request, true, fmt.Errorf("%w: specify the date range for the activity lookup", ErrAmbiguousIntent)
	}
	if join := strings.ToLower(groups["join"]); join != "and" && join != "to" && join != "-" {
		return request, true, fmt.Errorf("%w: use 'and', 'to', or a hyphen between the activity dates", ErrAmbiguousIntent)
	}
	startParts, err := parseActivityDate(groups["start"])
	if err != nil {
		return request, true, fmt.Errorf("%w: invalid activity start date: %v", ErrAmbiguousIntent, err)
	}
	endParts, err := parseActivityDate(groups["end"])
	if err != nil {
		return request, true, fmt.Errorf("%w: invalid activity end date: %v", ErrAmbiguousIntent, err)
	}
	if (startParts.year == 0) != (endParts.year == 0) {
		return request, true, fmt.Errorf("%w: include a year on both dates or omit both", ErrAmbiguousIntent)
	}
	inferred := startParts.year == 0
	if inferred {
		startParts.year, endParts.year = now.Year(), now.Year()
	}
	start := time.Date(startParts.year, startParts.month, startParts.day, 0, 0, 0, 0, time.UTC)
	end := time.Date(endParts.year, endParts.month, endParts.day, 0, 0, 0, 0, time.UTC)
	if start.Year() != startParts.year || start.Month() != startParts.month || start.Day() != startParts.day || end.Year() != endParts.year || end.Month() != endParts.month || end.Day() != endParts.day {
		return request, true, fmt.Errorf("%w: activity range contains an invalid calendar date", ErrAmbiguousIntent)
	}
	if start.After(end) {
		return request, true, fmt.Errorf("%w: activity date range is reversed; include years for a cross-year range", ErrAmbiguousIntent)
	}
	if end.Year() > 9999 || end.AddDate(0, 0, 1).Year() > 9999 {
		return request, true, fmt.Errorf("%w: activity end date has no representable following day", ErrAmbiguousIntent)
	}
	project := groups["project"]
	if project != "" && !safeActivityProject.MatchString(project) {
		return request, true, fmt.Errorf("%w: use a project key containing letters, digits, or underscores and beginning with a letter", ErrAmbiguousIntent)
	}
	request.StartDate = start.Format("2006-01-02")
	request.EndDate = end.Format("2006-01-02")
	request.Project = project
	request.YearInferred = inferred
	return request, true, nil
}

var safeActivityProject = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

type activityDateParts struct {
	year  int
	month time.Month
	day   int
}

func parseActivityDate(raw string) (activityDateParts, error) {
	match := activityDate.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return activityDateParts{}, fmt.Errorf("use YYYY-MM-DD or a month name and day, optionally with a year")
	}
	groups := make(map[string]string)
	for i, name := range activityDate.SubexpNames() {
		if i > 0 && name != "" {
			groups[name] = match[i]
		}
	}
	if groups["iso"] != "" {
		parsed, err := time.Parse("2006-01-02", groups["iso"])
		if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
			return activityDateParts{}, fmt.Errorf("%q is not a valid calendar date", raw)
		}
		return activityDateParts{year: parsed.Year(), month: parsed.Month(), day: parsed.Day()}, nil
	}
	month, ok := monthNumber[strings.ToLower(groups["month"])]
	if !ok {
		return activityDateParts{}, fmt.Errorf("unknown month %q", groups["month"])
	}
	var day, year int
	if _, err := fmt.Sscanf(groups["day"], "%d", &day); err != nil {
		return activityDateParts{}, fmt.Errorf("invalid day %q", groups["day"])
	}
	if groups["year"] != "" {
		if _, err := fmt.Sscanf(groups["year"], "%d", &year); err != nil || year < 1 || year > 9999 {
			return activityDateParts{}, fmt.Errorf("invalid year %q", groups["year"])
		}
	}
	return activityDateParts{year: year, month: month, day: day}, nil
}
