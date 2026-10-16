package llm

import (
	"regexp"
	"strings"
)

var countQuestion = regexp.MustCompile(`(?i)^(?:how\s+many\b|count\b)`)

// IsCountQuery recognizes explicit count question frames without rewriting the
// input sent to the translator. Other phrasings can use the explicit count flag.
func IsCountQuery(input string) bool {
	return countQuestion.MatchString(strings.TrimSpace(input))
}
