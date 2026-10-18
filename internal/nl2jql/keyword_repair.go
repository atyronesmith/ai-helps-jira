package nl2jql

import (
	"fmt"
	"regexp"
	"strings"
)

// Repairs are limited to grammar vocabulary in search positions. Identifiers
// and quoted values are opaque; this is not a general spelling corrector.
var keywordTokens = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'[^']*'|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|[\p{L}][\p{L}\p{N}_-]*|[(),]`)

var searchCommands = strings.Fields("show find list get give fetch bring see count")
var predicateKeywords = strings.Fields("open closed done created updated resolved priority assigned issue issues ticket tickets item items jira jiras bug bugs epic epics task tasks story stories")
var orderFrames = strings.Fields("ordered sorted order")

func orderingKeywords() []string {
	words := strings.Fields("from oldest newest ascending descending asc desc")
	for alias := range orderFieldJQL {
		words = append(words, strings.Fields(alias)...)
	}
	return words
}

// oneKeywordEdit includes adjacent transpositions, a common typing error.
func oneKeywordEdit(a, b string) bool {
	if a == b || len(a)-len(b) > 1 || len(b)-len(a) > 1 {
		return false
	}
	if len(a) == len(b) {
		first := -1
		for i := range a {
			if a[i] == b[i] {
				continue
			}
			if first < 0 {
				first = i
				continue
			}
			return i == first+1 && a[first] == b[i] && a[i] == b[first] && a[i+1:] == b[i+1:]
		}
		return first >= 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	return a[i:] == b[i+1:]
}

func keywordCandidate(word string, vocabulary []string) (string, error) {
	if len(word) < 3 || len(word) > 64 {
		return "", nil
	}
	for _, char := range word {
		if char < 'a' || char > 'z' {
			return "", nil
		}
	}
	for _, known := range vocabulary {
		if word == known {
			return "", nil
		}
	}
	candidate := ""
	for _, known := range vocabulary {
		if !oneKeywordEdit(word, known) {
			continue
		}
		if candidate != "" && candidate != known {
			return "", fmt.Errorf("%w: ambiguous keyword spelling; please rephrase", ErrAmbiguousIntent)
		}
		candidate = known
	}
	return candidate, nil
}

func repairSearchKeywords(input string) (string, error) {
	literalStart, literalEnd, hasLiteral := textLiteralSpan(input)
	spans := keywordTokens.FindAllStringIndex(input, -1)
	words := make([]string, len(spans))
	for i, span := range spans {
		words[i] = strings.ToLower(input[span[0]:span[1]])
	}
	replacements := make(map[int]string)
	if len(words) > 0 && !knownSearchWord(words[0]) {
		command, err := keywordCandidate(words[0], searchCommands)
		if err != nil {
			return input, err
		}
		if command != "" {
			replacements[0] = command
			words[0] = command
		}
	}
	ordering := false
	protected := ""
	orderWords := orderingKeywords()
	for i, word := range words {
		if hasLiteral && spans[i][0] >= literalStart && spans[i][0] < literalEnd {
			continue
		}
		if strings.ContainsAny(word, "\"'@") || word == "(" || word == ")" || word == "," {
			continue
		}
		if i+1 < len(words) && words[i+1] == "by" {
			frame := ""
			for _, known := range orderFrames {
				// These alternatives have identical ordering semantics.
				if oneKeywordEdit(word, known) {
					frame = known
					break
				}
			}
			if word == "ordered" || word == "sorted" || word == "order" || frame != "" {
				if frame != "" {
					replacements[i] = frame
				}
				ordering, protected = true, ""
				continue
			}
		}
		if word == "in" && i+3 < len(words) && strings.Join(words[i:i+4], " ") == "in order of when" {
			ordering, protected = true, ""
		}
		if word == "by" && i > 0 && (protected == "project" || isIssueNoun(words[i-1])) {
			ordering, protected = true, ""
		}
		if word == "and" || word == "or" {
			protected = ""
			continue
		}
		if protected != "" {
			continue
		}
		// This one common misspelling is only recoverable in a strong
		// relative-priority frame. Keep it out of the general keyword
		// vocabulary so it cannot rewrite project names or text values.
		if word == "thare" && i > 0 && isIssueNoun(words[i-1]) &&
			i+2 < len(words) && (words[i+1] == "are" || words[i+1] == "is") && words[i+2] == "priority" {
			replacements[i] = "that"
			words[i] = "that"
			continue
		}
		if !ordering {
			if word == "in" || word == "project" {
				protected = "project"
				continue
			}
			if word == "assigned" || word == "priority" || word == "by" {
				protected = "value"
				continue
			}
		}
		vocabulary := predicateKeywords
		if ordering {
			vocabulary = orderWords
		}
		// Exact words in other grammar roles must not be "repaired" into a
		// nearby keyword, for example the verb "list" into the noun "last".
		if knownSearchWord(word) {
			continue
		}
		candidate, err := keywordCandidate(word, vocabulary)
		if err != nil {
			return input, err
		}
		if candidate != "" {
			replacements[i] = candidate
			if !ordering && (candidate == "assigned" || candidate == "priority") {
				protected = "value"
			}
		}
	}
	var output strings.Builder
	last := 0
	for i, span := range spans {
		if replacement := replacements[i]; replacement != "" {
			output.WriteString(input[last:span[0]])
			output.WriteString(replacement)
			last = span[1]
		}
	}
	output.WriteString(input[last:])
	return output.String(), nil
}

func isIssueNoun(word string) bool {
	switch word {
	case "issue", "issues", "ticket", "tickets", "item", "items", "jira", "jiras":
		return true
	}
	return false
}

func knownSearchWord(word string) bool {
	if _, ok := fillerSet[word]; ok {
		return true
	}
	if _, ok := priorityJQL[word]; ok {
		return true
	}
	for _, vocabulary := range [][]string{searchCommands, predicateKeywords, orderFrames, orderingKeywords(), strings.Fields("my mine myself me assigned to in by and or not when now last")} {
		for _, known := range vocabulary {
			if word == known {
				return true
			}
		}
	}
	return false
}
