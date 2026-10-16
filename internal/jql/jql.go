// Package jql provides shared structural JQL construction, parsing, rendering,
// and network-free validation. Tenant vocabulary is checked separately by Jira.
package jql

import "fmt"

type Options struct{ DefaultProject string }

// BlankQuotedLiterals returns jql with the contents of every quoted string
// literal (and the quotes themselves) replaced by spaces, preserving the
// exact byte length so indexes into the result are valid indexes into the
// original.
//
// Structural scans run against the blanked copy so that a keyword appearing
// inside a literal -- summary ~ "ORDER BY" or labels = "a or b" -- is never
// mistaken for JQL syntax. Backslash escapes consume the following byte, so
// an escaped quote does not terminate the literal early.
func BlankQuotedLiterals(jql string) string {
	b := []byte(jql)
	var quote byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			b[i] = ' '
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			b[i] = ' '
		}
	}
	return string(b)
}

// Validate runs a local, network-free structural check over jqlStr and
// returns an error naming the offending JQL and the rule violated, or nil
// when jqlStr passes every rule. Validate never modifies or repairs jqlStr.
func Validate(jqlStr string, opts Options) error {
	fail := func(rule, detail string) error {
		return fmt.Errorf("invalid JQL (rule %s): %s: %q", rule, detail, jqlStr)
	}
	q, err := Parse(jqlStr)
	if err != nil {
		return fail("1-structure", err.Error())
	}
	if q.Where == nil {
		return fail("4-nonempty-where", "where-clause is empty")
	}
	if opts.DefaultProject != "" && !q.DirectIdentityOnly() {
		if !q.HasProjectScope() {
			return fail("5-scope", "a positive project predicate does not restrict every query branch")
		}
	}

	return nil
}
