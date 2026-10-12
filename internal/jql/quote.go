package jql

import "strings"

// Quote renders value as a JQL double-quoted string literal, unconditionally.
// Every backslash and embedded double quote is escaped, so the result is safe
// to splice into a JQL template regardless of what value contains -- a quote,
// a parenthesis, the bareword OR, or anything else that would otherwise let
// value break out of its own clause and change the query's meaning.
//
// This is the function to reach for whenever value did not originate in this
// program's own configuration: an MCP tool argument, a CLI flag, anything a
// caller supplied. Quote never decides whether escaping is necessary -- it
// always escapes -- which is what makes it safe for input Quote's author
// cannot vouch for.
func Quote(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, r := range value {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// QuoteIdent renders key as a bare JQL word when it contains none of the
// characters that would require quoting (whitespace or an embedded double
// quote), and as a quoted string literal via Quote otherwise.
//
// This is the "quote only if needed" behavior historically implemented as
// quoteIfNeeded in internal/nl2jql/parser.go, moved here so there is one
// implementation in the tree. It exists to keep output such as
// `project = FIELDCTO` or `assignee = John` unquoted when the value is
// already a safe bare word, matching what callers and golden tests expect,
// while still falling back to full quoting for anything that is not.
//
// QuoteIdent is a convenience, not a safety boundary: a value with no
// whitespace or quote can still be structurally meaningful on its own (JQL
// requires whitespace around keywords like OR/AND, so a bare word without
// whitespace cannot introduce a top-level keyword, but unbalanced
// parentheses in a bare word are still possible and will surface as a
// rejected query via jql.Validate rather than a silent injection). Callers
// that cannot vouch for a value's origin -- anything arriving as an MCP tool
// argument or other external input -- should prefer Quote, which escapes
// unconditionally.
func QuoteIdent(key string) string {
	if strings.ContainsAny(key, " \t\"") {
		return Quote(key)
	}
	return key
}
