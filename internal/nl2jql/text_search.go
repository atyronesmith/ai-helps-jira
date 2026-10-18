package nl2jql

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

var textWordsFrame = regexp.MustCompile(`(?i)\b(?:that\s+)?contain\s+the\s+words\b`)
var textOrderingFrame = regexp.MustCompile(`(?i)\b(?:and\s+)?(?:ordered|sorted|order)\s+by\b|\bin\s+order\s+of\s+when\b|\bby(?:\s+|$)`)
var plainTextWord = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Search literals bypass grammar spelling/date/project normalization. The
// closed word-list grammar separately validates separators and terms.
func textLiteralSpan(input string) (start, end int, ok bool) {
	frame := textWordsFrame.FindStringIndex(input)
	if frame == nil {
		return 0, 0, false
	}
	start, end = frame[1], len(input)
	if order := textOrderingFrame.FindStringIndex(input[start:]); order != nil {
		end = start + order[0]
	}
	return start, end, true
}

func validateTextWordQueries(query *Query) error {
	seenText := false
	var inspect func(*Clause) (bool, error)
	inspect = func(clause *Clause) (bool, error) {
		if clause.Group != nil {
			return false, validateTextWordQueries(clause.Group)
		}
		if clause.Negation != nil {
			return inspect(clause.Negation)
		}
		return clause.TextSearch != nil, nil
	}
	for _, clause := range query.Clauses {
		if clause == nil {
			continue
		}
		if seenText && clause.OrderBy == nil {
			return fmt.Errorf("%w: separate literal words with commas or OR, then specify ordering", ErrAmbiguousIntent)
		}
		text, err := inspect(clause)
		if err != nil {
			return err
		}
		seenText = seenText || text
	}
	return nil
}

func textOrPredicate(search *TextSearch) (*Intent, error) {
	if search == nil || len(search.Terms) == 0 || len(search.Terms) > 32 {
		return nil, fmt.Errorf("%w: use a bounded nonempty list of search words", ErrAmbiguousIntent)
	}
	words := make([]string, 0, len(search.Terms))
	for _, term := range search.Terms {
		if term == nil || term.Value == "" || len(term.Value) > 128 {
			return nil, fmt.Errorf("%w: invalid search word", ErrAmbiguousIntent)
		}
		word := term.Value
		switch strings.ToUpper(word) {
		case "AND", "OR", "NOT", "TO":
			word = luceneLiteral(word)
		default:
			if !plainTextWord.MatchString(word) {
				word = luceneLiteral(word)
			}
		}
		words = append(words, word)
	}
	return comparison("text", "~", jql.Quote(strings.Join(words, " OR "))), nil
}

func luceneLiteral(word string) string {
	word = strings.ReplaceAll(word, `\`, `\\`)
	word = strings.ReplaceAll(word, `"`, `\"`)
	return `"` + word + `"`
}
