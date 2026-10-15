package jql

import (
	"strings"
	"testing"
)

func TestValidate_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		jql     string
		opts    Options
		wantErr bool
		errHas  string // optional substring the error message must contain
	}{
		// Rule 1: balanced quotes.
		{
			name: "balanced double quotes ok",
			jql:  `project = TEST AND summary ~ "foo bar"`,
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "unterminated double quote rejected",
			jql:     `project = TEST AND summary ~ "foo bar`,
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
			errHas:  "rule 1",
		},
		{
			name: "escaped quote inside literal ok",
			jql:  `project = TEST AND summary ~ "a \"quoted\" word"`,
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "unterminated single quote rejected",
			jql:     `project = TEST AND summary ~ 'foo`,
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},

		// Rule 1: balanced parentheses.
		{
			name: "balanced parens ok",
			jql:  `project = TEST AND (status = Open OR status = New)`,
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "unbalanced opening paren rejected",
			jql:     `project = TEST AND (status = Open`,
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "unbalanced closing paren rejected",
			jql:     `project = TEST AND status = Open)`,
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "paren-like characters inside a literal do not affect balance",
			jql:  `project = TEST AND summary ~ "(unbalanced"`,
			opts: Options{DefaultProject: "TEST"},
		},

		// Rule 2: ORDER BY at most once, top level, final clause.
		{
			name: "single trailing order by ok",
			jql:  "project = TEST AND status = Open ORDER BY priority ASC",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "duplicate top-level order by rejected",
			jql:     "project = TEST ORDER BY priority ASC ORDER BY status ASC",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "nested order by rejected",
			jql:     "project = TEST AND (status = Open ORDER BY priority ASC)",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "content glued on after order by rejected",
			jql:     "project = TEST ORDER BY priority ASC AND status = Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "order by inside a quoted literal is not mistaken for the clause",
			jql:  `project = TEST AND summary ~ "ORDER BY"`,
			opts: Options{DefaultProject: "TEST"},
		},

		// Rule 3: dangling / doubled operators.
		{
			name:    "leading AND rejected",
			jql:     "AND status = Open",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "leading OR rejected",
			jql:     "OR status = Open",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "trailing AND rejected",
			jql:     "status = Open AND",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "trailing OR rejected",
			jql:     "status = Open OR",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "doubled AND AND rejected",
			jql:     "status = Open AND AND project = TEST",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "doubled OR AND rejected",
			jql:     "status = Open OR AND project = TEST",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "well-formed operators ok",
			jql:  "project = TEST AND status = Open AND priority = High",
			opts: Options{DefaultProject: "TEST"},
		},

		// Rule 4: non-empty where-clause, unconditional.
		{
			name:    "empty jql rejected",
			jql:     "",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "whitespace-only jql rejected",
			jql:     "   ",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "where-clause empty with only order by rejected unconditionally",
			jql:     "ORDER BY priority ASC",
			opts:    Options{}, // no default project configured -- rule 4 still applies
			wantErr: true,
		},

		// Rule 5: scope invariant.
		{
			name: "project predicate present passes when configured",
			jql:  "project = TEST AND status = Open",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "missing project predicate rejected when configured",
			jql:     "status = Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "missing project predicate ok when no default configured",
			jql:  "status = Open",
			opts: Options{},
		},
		{
			name:    "project = X OR foo leaks scope and is rejected",
			jql:     "project = TEST OR status = Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "bare top-level OR without any project predicate is rejected",
			jql:     "issuetype = Bug OR issuetype = Story",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "correctly scoped and parenthesised OR equivalent passes",
			jql:  "project = TEST AND (issuetype = Bug OR issuetype = Story)",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "nested OR before project restriction is scoped",
			jql:  "(status = Open OR priority = High) AND project = TEST",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "nested OR with project restriction outside every branch is scoped",
			jql:  "project = TEST AND ((status = Open OR priority = High) AND resolution = Fixed)",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "nested OR branch without project restriction is rejected",
			jql:     "(project = TEST AND status = Open) OR priority = High",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "Unicode before top-level OR cannot hide an unscoped branch",
			jql:     `project = TEST AND summary ~ "ı" OR status = Open`,
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "quoted Unicode and OR text do not split a safe query",
			jql:  `project = TEST AND summary ~ "ı OR status = Open"`,
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "project restriction in only one nested OR branch is rejected",
			jql:     "status = Open OR (project = TEST AND priority = High)",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "project inequality does not satisfy scope",
			jql:     "project != TEST AND status = Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "project NOT IN does not satisfy scope",
			jql:     "project NOT IN (OTHER) AND status = Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name: "positive restriction on a different project remains allowed",
			jql:  "project = OTHER AND status = Open",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "positive project IN restriction remains allowed",
			jql:  "project IN (TEST, OTHER) AND status = Open",
			opts: Options{DefaultProject: "TEST"},
		},

		// Rule 5 exemption: direct-identity queries.
		{
			name: "key predicate exempts from scope invariant",
			jql:  "key = TEST-123",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "id predicate exempts from scope invariant",
			jql:  "id = 10042",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "key IN list still exempt",
			jql:  "key in (TEST-1, TEST-2)",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "key predicate joined by top-level OR is still exempt",
			jql:  "key = TEST-1 OR key = TEST-2",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "id IN list is exempt",
			jql:  "id IN (10042, 10043)",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name: "key and id identity predicates combined with AND are exempt",
			jql:  "key = TEST-1 AND id = 10042",
			opts: Options{DefaultProject: "TEST"},
		},
		{
			name:    "mixing key with a non-identity field no longer exempt",
			jql:     "key = TEST-123 OR status = Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "key with unsupported WAS predicate is not exempt",
			jql:     "key = TEST-1 OR status WAS Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "key with unsupported CHANGED predicate is not exempt",
			jql:     "key = TEST-1 OR status CHANGED",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "key with CHANGED AFTER predicate is not exempt",
			jql:     "key = TEST-1 AND status CHANGED AFTER -1d",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
		{
			name:    "malformed identity equality is not exempt",
			jql:     "key = TEST-1 WAS Open",
			opts:    Options{DefaultProject: "TEST"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.jql, tt.opts)
			if tt.wantErr && err == nil {
				t.Fatalf("Validate(%q) = nil, want error", tt.jql)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tt.jql, err)
			}
			if tt.wantErr && tt.errHas != "" && !strings.Contains(err.Error(), tt.errHas) {
				t.Fatalf("Validate(%q) error = %v, want substring %q", tt.jql, err, tt.errHas)
			}
		})
	}
}

// TestValidate_KeyRefetchExemption is the exact shape used by the
// post-mutation refetch in internal/mcpserver/crud_tools.go:
// SearchJQL(fmt.Sprintf("key = %s", key), 1). Without the rule 5 exemption
// for direct-identity queries, this would be rejected whenever a default
// project is configured, and the caller treats a search error as "assume
// stale" and silently falls back -- so the refetch optimisation would stop
// working with no error surfaced anywhere.
func TestValidate_KeyRefetchExemption(t *testing.T) {
	jqlStr := "key = TEST-456"
	if err := Validate(jqlStr, Options{DefaultProject: "TEST"}); err != nil {
		t.Fatalf("Validate(%q) with default project configured = %v, want nil", jqlStr, err)
	}
}

// TestValidate_ErrorNamesOffendingJQLAndRule checks that a rejection names
// both the offending JQL and the rule that failed, per the ticket's "fail
// closed: return an error naming the offending JQL and the rule violated."
func TestValidate_ErrorNamesOffendingJQLAndRule(t *testing.T) {
	bad := "status = Open"
	err := Validate(bad, Options{DefaultProject: "TEST"})
	if err == nil {
		t.Fatalf("Validate(%q) = nil, want error", bad)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Fatalf("error %v does not name the offending JQL %q", err, bad)
	}
	if !strings.Contains(err.Error(), "rule") {
		t.Fatalf("error %v does not name a rule", err)
	}
}

func TestBlankQuotedLiterals(t *testing.T) {
	in := `summary ~ "ORDER BY" AND labels = 'a or b'`
	got := BlankQuotedLiterals(in)
	if len(got) != len(in) {
		t.Fatalf("BlankQuotedLiterals changed length: got %d, want %d", len(got), len(in))
	}
	if strings.Contains(strings.ToUpper(got), "ORDER BY") {
		t.Fatalf("literal contents leaked through blanking: %q", got)
	}
	if strings.Contains(got, "\"") || strings.Contains(got, "'") {
		t.Fatalf("quote characters were not blanked: %q", got)
	}
}

func TestTopLevelKeywordIndex(t *testing.T) {
	blanked := BlankQuotedLiterals("status = Open OR (priority = High AND resolution = Fixed)")
	idx := TopLevelKeywordIndex(blanked, "OR")
	if idx < 0 {
		t.Fatalf("expected to find top-level OR")
	}
	blanked2 := BlankQuotedLiterals("status = Open AND (priority = High OR resolution = Fixed)")
	if TopLevelKeywordIndex(blanked2, "OR") >= 0 {
		t.Fatalf("OR nested inside parens should not be reported as top level")
	}
}

func TestOrderByIndex(t *testing.T) {
	blanked := BlankQuotedLiterals("status = Open ORDER BY   priority ASC")
	idx := OrderByIndex(blanked)
	if idx < 0 {
		t.Fatalf("expected to find ORDER BY despite irregular whitespace")
	}
	if blanked[idx:idx+5] != "ORDER" && blanked[idx:idx+5] != "order" {
		t.Fatalf("index %d does not point at ORDER", idx)
	}
}
