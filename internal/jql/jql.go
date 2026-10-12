// Package jql provides local, network-free structural analysis and
// validation of JQL strings.
//
// This package never repairs JQL. Its helpers only observe structure
// (quote/paren balance, keyword position, clause boundaries) and Validate
// only reports violations. Silently rewriting a caller's query is how the
// defects tracked under FIELDCTO-2807/2808 stayed hidden; this package is
// deliberately read-only so that mistake cannot recur here.
package jql

import (
	"fmt"
	"regexp"
	"strings"
)

// Options carries the tenant context Validate needs. An Options struct is
// used instead of adding a parameter to Client.SearchJQL.
type Options struct {
	// DefaultProject is the tenant's configured default project key, if any.
	// When set, Validate enforces the scope invariant (rule 5).
	DefaultProject string
}

// isJQLWordByte reports whether c can appear inside a JQL bareword, so
// keyword scans can enforce word boundaries and never match the "or" inside
// "reporter".
func isJQLWordByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

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

// TopLevelKeywordIndex returns the index of the first occurrence of the
// uppercase keyword kw that sits at parenthesis depth zero in the
// already-blanked JQL, with word boundaries on both sides. It returns -1
// when there is none.
func TopLevelKeywordIndex(blanked, kw string) int {
	up := strings.ToUpper(blanked)
	depth := 0
	for i := 0; i < len(up); i++ {
		switch up[i] {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 || !strings.HasPrefix(up[i:], kw) {
			continue
		}
		if i > 0 && isJQLWordByte(up[i-1]) {
			continue
		}
		if end := i + len(kw); end < len(up) && isJQLWordByte(up[end]) {
			continue
		}
		return i
	}
	return -1
}

// OrderByIndex returns the index at which the trailing ORDER BY clause
// begins, or -1 when the query has none. ORDER and BY may be separated by
// any run of whitespace. The scan is depth- and quote-aware for the same
// reasons as TopLevelKeywordIndex.
func OrderByIndex(blanked string) int {
	up := strings.ToUpper(blanked)
	depth := 0
	for i := 0; i < len(up); i++ {
		switch up[i] {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 || !strings.HasPrefix(up[i:], "ORDER") {
			continue
		}
		if i > 0 && isJQLWordByte(up[i-1]) {
			continue
		}
		j := i + len("ORDER")
		k := j
		for k < len(up) && (up[k] == ' ' || up[k] == '\t' || up[k] == '\n' || up[k] == '\r') {
			k++
		}
		if k == j || !strings.HasPrefix(up[k:], "BY") {
			continue // "ORDER" not followed by whitespace then "BY"
		}
		if end := k + len("BY"); end < len(up) && isJQLWordByte(up[end]) {
			continue
		}
		return i
	}
	return -1
}

// allOrderByIndexes returns every occurrence of the ORDER BY keyword pair in
// blanked, optionally restricted to parenthesis depth zero. Unlike
// OrderByIndex (which exists to find the trailing clause and stops at the
// first top-level match), this is used by Validate to detect a second
// occurrence -- top-level or nested -- so rule 2 can be enforced precisely.
func allOrderByIndexes(blanked string, topLevelOnly bool) []int {
	up := strings.ToUpper(blanked)
	depth := 0
	var found []int
	for i := 0; i < len(up); i++ {
		switch up[i] {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if topLevelOnly && depth != 0 {
			continue
		}
		if !strings.HasPrefix(up[i:], "ORDER") {
			continue
		}
		if i > 0 && isJQLWordByte(up[i-1]) {
			continue
		}
		j := i + len("ORDER")
		k := j
		for k < len(up) && (up[k] == ' ' || up[k] == '\t' || up[k] == '\n' || up[k] == '\r') {
			k++
		}
		if k == j || !strings.HasPrefix(up[k:], "BY") {
			continue
		}
		if end := k + len("BY"); end < len(up) && isJQLWordByte(up[end]) {
			continue
		}
		found = append(found, i)
	}
	return found
}

// danglingLeadingRe and danglingTrailingRe detect a leading or trailing
// AND/OR on an (already blanked) where-clause, e.g. "AND status = Open" or
// "status = Open AND".
var (
	danglingLeadingRe  = regexp.MustCompile(`(?i)^\s*(AND|OR)\b`)
	danglingTrailingRe = regexp.MustCompile(`(?i)\b(AND|OR)\s*$`)
	doubledOperatorRe  = regexp.MustCompile(`(?i)\b(AND|OR)\s+(AND|OR)\b`)
)

// projectPredicateRe matches any JQL predicate on the project field. It
// covers the equality and membership operators; the leading word boundary
// keeps look-alikes such as "subproject" or "projectname" from matching.
var projectPredicateRe = regexp.MustCompile(`(?i)\bproject\s*(?:!=|=|\bin\b|not\s+in\b)`)

// predicateFieldRe matches the field name immediately preceding a comparison
// or membership operator, so Validate can collect every field a where-clause
// makes a predicate on (used for the key/id direct-identity exemption).
var predicateFieldRe = regexp.MustCompile(`(?i)\b([A-Za-z_][A-Za-z0-9_]*)\s*(?:!=|=|~|!~|>=|<=|>|<|\bnot\s+in\b|\bin\b|\bis\s+not\b|\bis\b)`)

// Validate runs a local, network-free structural check over jqlStr and
// returns an error naming the offending JQL and the rule violated, or nil
// when jqlStr passes every rule. Validate never modifies or repairs jqlStr.
func Validate(jqlStr string, opts Options) error {
	fail := func(rule, detail string) error {
		return fmt.Errorf("invalid JQL (rule %s): %s: %q", rule, detail, jqlStr)
	}

	// Rule 1: balanced quotes.
	if !quotesBalanced(jqlStr) {
		return fail("1-balanced-quotes", "unterminated or unbalanced quote")
	}

	blanked := BlankQuotedLiterals(jqlStr)

	// Rule 1: balanced parentheses, checked on the blanked copy so a paren
	// character inside a string literal is never mistaken for syntax.
	if !parensBalanced(blanked) {
		return fail("1-balanced-parens", "unbalanced parentheses")
	}

	// Rule 2: ORDER BY appears at most once, at top level, and is the final
	// clause.
	topOrderBys := allOrderByIndexes(blanked, true)
	allOrderBys := allOrderByIndexes(blanked, false)
	if len(allOrderBys) > len(topOrderBys) {
		return fail("2-order-by", "ORDER BY must appear at top level, not nested in parentheses")
	}
	if len(topOrderBys) > 1 {
		return fail("2-order-by", "ORDER BY may appear at most once")
	}

	var where, orderBy string
	if len(topOrderBys) == 1 {
		idx := topOrderBys[0]
		where, orderBy = jqlStr[:idx], strings.TrimSpace(jqlStr[idx:])
		// The ORDER BY clause itself must not contain a top-level AND/OR --
		// that indicates trailing content glued on after what should be the
		// final clause, e.g. "... ORDER BY priority AND foo = bar".
		blankedOrderBy := BlankQuotedLiterals(orderBy)
		if TopLevelKeywordIndex(blankedOrderBy, "AND") >= 0 || TopLevelKeywordIndex(blankedOrderBy, "OR") >= 0 {
			return fail("2-order-by", "ORDER BY must be the final clause")
		}
	} else {
		where = jqlStr
	}
	where = strings.TrimSpace(where)

	// Rule 4: the where-clause must be non-empty. Unconditional.
	if where == "" {
		return fail("4-nonempty-where", "where-clause is empty")
	}

	blankedWhere := BlankQuotedLiterals(where)

	// Rule 3: no dangling or doubled operators.
	if danglingLeadingRe.MatchString(blankedWhere) {
		return fail("3-dangling-operator", "where-clause has a leading AND/OR")
	}
	if danglingTrailingRe.MatchString(blankedWhere) {
		return fail("3-dangling-operator", "where-clause has a trailing AND/OR")
	}
	if doubledOperatorRe.MatchString(blankedWhere) {
		return fail("3-dangling-operator", "where-clause has a doubled operator (e.g. \"AND AND\")")
	}

	// Rule 5: scope invariant.
	if opts.DefaultProject != "" && !isDirectIdentityOnly(blankedWhere) {
		if TopLevelKeywordIndex(blankedWhere, "OR") >= 0 {
			return fail("5-scope", "top-level OR can escape project scope even when a project predicate is present")
		}
		if !projectPredicateRe.MatchString(blankedWhere) {
			return fail("5-scope", "no project predicate and a default project is configured")
		}
	}

	return nil
}

// quotesBalanced reports whether jql's quoting is well-formed: every quote
// opened is closed, honouring backslash escapes, with no unterminated quote
// left open at end of string.
func quotesBalanced(jql string) bool {
	b := []byte(jql)
	var quote byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(b) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
		}
	}
	return quote == 0
}

// parensBalanced reports whether blanked (quote-literal contents already
// blanked) has balanced parentheses: no closing paren without a matching
// open, and no open paren left unclosed.
func parensBalanced(blanked string) bool {
	depth := 0
	for i := 0; i < len(blanked); i++ {
		switch blanked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// isDirectIdentityOnly reports whether blankedWhere's where-clause consists
// solely of predicates on the `key` or `id` fields. Issue keys and IDs are
// globally unique, so project scoping is meaningless for such a query --
// e.g. the post-mutation refetch in internal/mcpserver/crud_tools.go,
// SearchJQL(fmt.Sprintf("key = %s", key), 1). A where-clause with no
// recognizable predicate at all is not treated as identity-only.
func isDirectIdentityOnly(blankedWhere string) bool {
	matches := predicateFieldRe.FindAllStringSubmatch(blankedWhere, -1)
	if len(matches) == 0 {
		return false
	}
	for _, m := range matches {
		field := strings.ToLower(m[1])
		if field != "key" && field != "id" {
			return false
		}
	}
	return true
}
