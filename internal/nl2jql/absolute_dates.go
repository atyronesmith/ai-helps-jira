package nl2jql

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

const absoluteDateEndpoint = `(?:"[^"]*"[^\s,?()]*|[^\s,?()]+)(?:\s+[0-9][^\s,?()]*)?`

// absoluteDateRangeRE recognizes explicit inclusive calendar-date ranges on
// Jira's core timestamp fields. Endpoint tokens are deliberately captured
// broadly so validation can reject timestamps, malformed dates, and unbalanced
// quotes instead of accidentally normalizing only a date prefix.
var absoluteDateRangeRE = regexp.MustCompile(
	`(?i)\b(?:(?:was|were)\s+)?(?P<field>due\s+date|created|updated|resolved|due)\b\s+` +
		`(?:(?:was|is)\s+)?` +
		`(?:between\s+(?P<between_start>` + absoluteDateEndpoint + `)\s+and\s+(?P<between_end>` + absoluteDateEndpoint + `)` +
		`|from\s+(?P<from_start>` + absoluteDateEndpoint + `)\s+to\s+(?P<from_end>` + absoluteDateEndpoint + `))`)

// absoluteDateRangeCandidateRE also captures mismatched range connectors so
// validateAbsoluteDateRanges can refuse malformed explicit frames rather than
// allowing a fallback to guess whether "from X and Y" means an inclusive
// range. The narrower absoluteDateRangeRE remains the set of normalizable,
// fully supported forms.
var absoluteDateRangeCandidateRE = regexp.MustCompile(
	`(?i)\b(?:(?:was|were)\s+)?(?P<field>due\s+date|created|updated|resolved|due)\b\s+` +
		`(?:(?:was|is)\s+)?(?P<kind>between|from)\s+` +
		`(?P<start>` + absoluteDateEndpoint + `)\s+(?P<join>and|to)\s+(?P<end>` + absoluteDateEndpoint + `)`)

var absoluteDateRangeIntroRE = regexp.MustCompile(
	`(?i)\b(?:(?:was|were)\s+)?(?:due\s+date|created|updated|resolved|due)\b\s+(?:(?:was|is)\s+)?(?:between|from)\b`)

// normalizeAbsoluteDateRanges replaces valid inclusive ranges with a lower
// bound and an exclusive bound on the day after the requested end date.
// Invalid input is left unchanged; validateAbsoluteDateRanges reports it at the
// translation boundary so normalization itself keeps its non-error contract.
func normalizeAbsoluteDateRanges(s string) string {
	if start, end, ok := textLiteralSpan(s); ok {
		return normalizeAbsoluteDateRangeSyntax(s[:start]) + s[start:end] + normalizeAbsoluteDateRangeSyntax(s[end:])
	}
	return normalizeAbsoluteDateRangeSyntax(s)
}

func normalizeAbsoluteDateRangeSyntax(s string) string {
	return rewriteMatches(absoluteDateRangeRE, s, func(groups map[string]string) (string, bool) {
		field := jqlField(groups["field"])
		startRaw, endRaw := absoluteDateEndpoints(groups)
		start, _, next, err := parseAbsoluteDateRange(startRaw, endRaw)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf("%s >= %s AND %s < %s", field, jql.Quote(start), field, jql.Quote(next)), true
	})
}

// validateAbsoluteDateRanges rejects malformed, impossible, reversed, or
// overflowing explicit ranges before either parser/fallback can reinterpret
// them. Literal text after "contain the words" remains opaque syntax.
func validateAbsoluteDateRanges(s string) error {
	if start, end, ok := textLiteralSpan(s); ok {
		s = s[:start] + strings.Repeat(" ", end-start) + s[end:]
	}
	for _, match := range absoluteDateRangeCandidateRE.FindAllStringSubmatch(s, -1) {
		groups := make(map[string]string)
		for i, name := range absoluteDateRangeCandidateRE.SubexpNames() {
			if i > 0 && name != "" && match[i] != "" {
				groups[name] = match[i]
			}
		}
		if isChronologicalOrderPhrase(groups) {
			continue
		}
		wantJoin := "and"
		if strings.EqualFold(groups["kind"], "from") {
			wantJoin = "to"
		}
		if !strings.EqualFold(groups["join"], wantJoin) {
			return fmt.Errorf("%w: use %s dates with %q", ErrAmbiguousIntent, groups["kind"], wantJoin)
		}
		start, end := groups["start"], groups["end"]
		if _, _, _, err := parseAbsoluteDateRange(start, end); err != nil {
			return fmt.Errorf("%w: %v", ErrAmbiguousIntent, err)
		}
	}
	for _, intro := range absoluteDateRangeIntroRE.FindAllStringIndex(s, -1) {
		candidate := absoluteDateRangeCandidateRE.FindStringIndex(s[intro[0]:])
		if candidate == nil || candidate[0] != 0 {
			return fmt.Errorf("%w: complete the explicit date range", ErrAmbiguousIntent)
		}
	}
	return nil
}

func isChronologicalOrderPhrase(groups map[string]string) bool {
	if !strings.EqualFold(groups["kind"], "from") || !strings.EqualFold(groups["join"], "to") {
		return false
	}
	start := strings.Trim(groups["start"], `"`)
	end := strings.Trim(groups["end"], `"`)
	return strings.EqualFold(start, "oldest") && strings.EqualFold(end, "newest") ||
		strings.EqualFold(start, "newest") && strings.EqualFold(end, "oldest")
}

func absoluteDateEndpoints(groups map[string]string) (start, end string) {
	if groups["between_start"] != "" || groups["between_end"] != "" {
		return groups["between_start"], groups["between_end"]
	}
	return groups["from_start"], groups["from_end"]
}

func parseAbsoluteDateRange(startRaw, endRaw string) (start, end, exclusiveEnd string, err error) {
	start, err = parseAbsoluteDate(startRaw)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid start date %q", startRaw)
	}
	end, err = parseAbsoluteDate(endRaw)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid end date %q", endRaw)
	}
	startTime, _ := time.Parse("2006-01-02", start)
	endTime, _ := time.Parse("2006-01-02", end)
	if startTime.After(endTime) {
		return "", "", "", fmt.Errorf("start date %q is after end date %q", start, end)
	}
	next := endTime.AddDate(0, 0, 1)
	if next.Year() > 9999 {
		return "", "", "", fmt.Errorf("end date %q has no representable exclusive next day", end)
	}
	return start, end, next.Format("2006-01-02"), nil
}

func parseAbsoluteDate(raw string) (string, error) {
	// Sentence punctuation may follow an unquoted date or a closing quote, but
	// punctuation inside quotes remains part of the date and is rejected.
	raw = strings.TrimSuffix(raw, ".")
	if strings.HasPrefix(raw, `"`) || strings.HasSuffix(raw, `"`) {
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			return "", fmt.Errorf("unbalanced date quotes")
		}
		raw = raw[1 : len(raw)-1]
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
		return "", fmt.Errorf("not a valid YYYY-MM-DD calendar date")
	}
	return parsed.Format("2006-01-02"), nil
}
