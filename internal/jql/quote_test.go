package jql

import (
	"fmt"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"plain", "FIELDCTO", `"FIELDCTO"`},
		{"embedded quote", `foo"bar`, `"foo\"bar"`},
		{"embedded backslash", `foo\bar`, `"foo\\bar"`},
		{"quote and backslash", `foo\"bar`, `"foo\\\"bar"`},
		{"injection attempt", `" OR project = OTHER`, `"\" OR project = OTHER"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Quote(tc.value); got != tc.want {
				t.Errorf("Quote(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestQuote_AgreesWithGoPercentQ exists because the plan explicitly calls out
// that Go's %q is Go quoting, not JQL quoting, and the two should be shown to
// agree (or not) rather than assumed. For plain ASCII values containing only
// the characters that matter here -- a quote, a backslash -- they do agree,
// which is what lets cmd/weekly_status.go's historical use of %q coexist with
// jql.Quote without a behavioral change. They do NOT agree in general: %q
// also escapes control characters (tab, newline) and would emit a literal
// `\t`/`\n` two-byte sequence that JQL does not interpret as an escape, where
// Quote leaves such bytes untouched. That divergence doesn't arise for the
// values actually quoted in this codebase (emails, release names, project
// keys), so it is recorded here rather than treated as a bug to fix.
func TestQuote_AgreesWithGoPercentQ(t *testing.T) {
	agree := []string{
		`foo"bar`,
		`foo\bar`,
		`foo\"bar`,
		`a"OR"project=OTHER`,
		"plain-value@example.com",
	}
	for _, v := range agree {
		goQ := fmt.Sprintf("%q", v)
		jqlQ := Quote(v)
		if goQ != jqlQ {
			t.Errorf("Quote and %%q disagree for %q: Quote=%q %%q=%q", v, jqlQ, goQ)
		}
	}

	diverge := []string{
		"foo\tbar",
		"foo\nbar",
	}
	for _, v := range diverge {
		goQ := fmt.Sprintf("%q", v)
		jqlQ := Quote(v)
		if goQ == jqlQ {
			t.Errorf("expected Quote and %%q to diverge for %q, both gave %q", v, jqlQ)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"bare project key", "FIELDCTO", "FIELDCTO"},
		{"bare name", "John", "John"},
		{"space requires quoting", "John Smith", `"John Smith"`},
		{"embedded quote requires quoting", `John "JJ" Smith`, `"John \"JJ\" Smith"`},
		{"email punctuation requires quoting", "aasmith@redhat.com", `"aasmith@redhat.com"`},
		{"period requires quoting", "release.1", `"release.1"`},
		{"reserved operator requires quoting", "OR", `"OR"`},
		{"injection attempt", `" OR project = OTHER`, `"\" OR project = OTHER"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuoteIdent(tc.key); got != tc.want {
				t.Errorf("QuoteIdent(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// TestQuoteIdent_InjectionCannotEscapeScope is the negative test the plan
// calls for: feed a value that would, if interpolated raw, read as
// `" OR project = OTHER` and confirm the quoted result still validates as
// scoped to the intended project rather than escaping it.
func TestQuoteIdent_InjectionCannotEscapeScope(t *testing.T) {
	hostile := `" OR project = OTHER`
	rendered := "key = " + QuoteIdent(hostile)
	// The hostile OR must now be inside the quoted literal, not a top-level
	// keyword, so a scan for a top-level OR must find nothing.
	tree, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if _, escaped := tree.Where.(Or); escaped {
		t.Errorf("hostile OR escaped quoting in %q", rendered)
	}
}

func TestQuote_InjectionCannotEscapeScope(t *testing.T) {
	hostile := `" OR project = OTHER`
	rendered := "fixVersion = " + Quote(hostile) + " AND project = FIELDCTO"
	if err := Validate(rendered, Options{DefaultProject: "FIELDCTO"}); err != nil {
		t.Fatalf("expected quoted JQL to validate, got: %v", err)
	}
	tree, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if _, escaped := tree.Where.(Or); escaped {
		t.Errorf("hostile OR escaped quoting in %q", rendered)
	}
}
