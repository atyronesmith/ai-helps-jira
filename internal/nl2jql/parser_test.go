package nl2jql

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   string
		want    string
		wantErr bool
	}{
		{
			name:  "due this week expands to bounded range and ascending sort",
			query: "my dueweek",
			want:  `assignee = currentUser() AND due >= startOfWeek() AND due <= endOfWeek() ORDER BY due ASC`,
		},
		{
			name:  "canonical example",
			query: "my open bugs in Support",
			want:  `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`,
		},
		{
			name:  "story point greater than comparison",
			query: "list all my work items with story points > 3?",
			want:  `assignee = currentUser() AND "Story Points" > 3`,
		},
		{
			name:  "story point comparison operators",
			query: "story points >= 3.5",
			want:  `"Story Points" >= 3.5`,
		},
		{
			name:  "story point less than",
			query: "story points < 3",
			want:  `"Story Points" < 3`,
		},
		{
			name:  "story point less than or equal",
			query: "story points <= 3",
			want:  `"Story Points" <= 3`,
		},
		{
			name:  "story point equality",
			query: "story points = 3",
			want:  `"Story Points" = 3`,
		},
		{
			name:  "natural story point ordering",
			query: "list all my open work items ordered by story points ascending",
			want:  `statusCategory != Done AND assignee = currentUser() ORDER BY "Story Points" ASC`,
		},
		{
			name:  "sorted by direction abbreviation",
			query: "my bugs sorted by priority desc",
			want:  `issuetype = Bug AND assignee = currentUser() ORDER BY priority DESC`,
		},
		{
			name:  "order by date alias",
			query: "open tasks order by creation date ascending",
			want:  `issuetype = Task AND statusCategory != Done ORDER BY created ASC`,
		},
		{
			name:  "multiword Jira field alias",
			query: "order by work item type descending my",
			want:  `assignee = currentUser() ORDER BY issuetype DESC`,
		},
		{
			name:  "in progress beats the project preposition",
			query: "assigned to John in progress tasks in ENG",
			want:  `project = ENG AND issuetype = Task AND status = "In Progress" AND assignee = John`,
		},
		{
			name:  "epics assigned to me",
			query: "epics assigned to me",
			want:  `issuetype = Epic AND assignee = currentUser()`,
		},
		{
			name:  "filler words are ignored",
			query: "show me all done tasks in ENG",
			want:  `project = ENG AND issuetype = Task AND status = Done`,
		},
		{
			name:  "status and type only",
			query: "in progress bugs",
			want:  `issuetype = Bug AND status = "In Progress"`,
		},
		{
			name:  "article between preposition and project",
			query: "open bugs in the Support",
			want:  `project = Support AND issuetype = Bug AND statusCategory != Done`,
		},
		{
			name:  "multi-word assignee name",
			query: "bugs assigned to John Smith in Support",
			want:  `project = Support AND issuetype = Bug AND assignee = "John Smith"`,
		},
		{
			name:  "multi-word project key gets quoted",
			query: "open bugs in Eng Platform",
			want:  `project = "Eng Platform" AND issuetype = Bug AND statusCategory != Done`,
		},
		{
			name:  "stop words between type and status",
			query: "a bug that is open",
			want:  `issuetype = Bug AND statusCategory != Done`,
		},
		{
			name:  "matching is case-insensitive",
			query: "MY DONE EPICS IN eng",
			want:  `project = eng AND issuetype = Epic AND status = Done AND assignee = currentUser()`,
		},
		{
			name:  "clauses in any order render identically",
			query: "bugs in Support open my",
			want:  `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`,
		},
		{
			name:  "duplicate ownership expressions collapse",
			query: "my open bugs assigned to me",
			want:  `issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`,
		},
		{
			name:  "punctuation is elided",
			query: "show me all open tickets, please!",
			want:  `statusCategory != Done`,
		},
		{
			name:  "singular issue types",
			query: "an open task",
			want:  `issuetype = Task AND statusCategory != Done`,
		},
		{
			name:  "hyphenated in-progress",
			query: "bugs in-progress",
			want:  `issuetype = Bug AND status = "In Progress"`,
		},
		// FIELDCTO-2808: several values for one field must become a membership
		// test. AND-ing them is valid JQL that can never match.
		{
			name:  "two issue types collapse into IN",
			query: "bugs and stories",
			want:  `issuetype IN (Bug, Story)`,
		},
		{
			name:  "three issue types collapse into IN",
			query: "bugs tasks epics",
			want:  `issuetype IN (Bug, Task, Epic)`,
		},
		{
			name:  "collapsed types still combine with other fields",
			query: "my bugs and stories in Support",
			want:  `project = Support AND issuetype IN (Bug, Story) AND assignee = currentUser()`,
		},
		{
			name:  "duplicate issue types collapse to a single equality",
			query: "bugs and bugs",
			want:  `issuetype = Bug`,
		},
		{
			name:  "two assignees collapse into IN",
			query: "bugs assigned to John assigned to Jane",
			want:  `issuetype = Bug AND assignee IN (John, Jane)`,
		},
		{
			name:  "priority field before value",
			query: "priority Critical",
			want:  `priority = Critical`,
		},
		{
			name:  "priority value before field",
			query: "Critical priority",
			want:  `priority = Critical`,
		},
		{
			name:  "priority ordering distinguishes Highest from High",
			query: "Highest priority and High priority",
			want:  `priority IN (Highest, High)`,
		},
		{
			name:  "all Jira priorities are recognized",
			query: "priority Critical and priority Highest and priority High and priority Medium and priority Low and priority Lowest",
			want:  `priority IN (Critical, Highest, High, Medium, Low, Lowest)`,
		},

		{
			name:    "noise-only input yields no constraints",
			query:   "please show me",
			wantErr: true,
		},
		{
			name:    "empty input is rejected",
			query:   "",
			wantErr: true,
		},
		{
			name:    "unknown words are rejected loudly",
			query:   "external bugs",
			wantErr: true,
		},
		{
			name:    "bare project name is rejected",
			query:   "Support",
			wantErr: true,
		},
		{
			name:    "unknown sort field is rejected",
			query:   "my issues ordered by sprint ascending",
			wantErr: true,
		},
		{
			name:    "unknown sort direction is rejected",
			query:   "my issues ordered by priority upwards",
			wantErr: true,
		},
		{
			name:    "multiple order clauses are rejected",
			query:   "my issues ordered by priority asc sorted by created desc",
			wantErr: true,
		},
		{
			name:    "due week sort conflict is rejected",
			query:   "my dueweek ordered by priority asc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.query)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %q, want error", tt.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tt.query, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q)\n got: %s\nwant: %s", tt.query, got, tt.want)
			}
		})
	}
}

func TestOrderByAllowlistAliasesAndDirections(t *testing.T) {
	aliases := make([]string, 0, len(orderFieldJQL))
	for alias := range orderFieldJQL {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	for _, alias := range aliases {
		for _, direction := range []struct {
			input string
			want  string
		}{
			{input: "asc", want: "ASC"},
			{input: "desc", want: "DESC"},
		} {
			t.Run(alias+"/"+direction.input, func(t *testing.T) {
				query := "order by " + alias + " " + direction.input
				got, err := Parse(query)
				if err != nil {
					t.Fatalf("Parse(%q): %v", query, err)
				}
				want := "ORDER BY " + orderFieldJQL[strings.ToLower(alias)] + " " + direction.want
				if got != want {
					t.Errorf("Parse(%q) = %q, want %q", query, got, want)
				}
			})
		}
	}
}

func TestToJQL(t *testing.T) {
	t.Parallel()

	t.Run("empty AST yields ErrNoConstraints", func(t *testing.T) {
		if _, err := (&Query{}).ToJQL(); !errors.Is(err, ErrNoConstraints) {
			t.Fatalf("ToJQL() error = %v, want ErrNoConstraints", err)
		}
	})

	t.Run("clause order never leaks into the output", func(t *testing.T) {
		a, err := Parse("my open bugs in Support")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		b, err := Parse("in Support bugs open assigned to me")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if a != b {
			t.Errorf("clause order leaked into JQL:\nfirst: %s\nsecond: %s", a, b)
		}
	})
}

// TestParseDeterministic pins the core contract of the engine: repeated
// parses of the same query always produce byte-identical JQL.
func TestParseDeterministic(t *testing.T) {
	t.Parallel()

	queries := []string{
		"my open bugs in Support",
		"assigned to John in progress tasks in ENG",
		"epics assigned to me",
		"show me all done tasks in ENG",
	}

	first := make(map[string]string, len(queries))
	for i := 0; i < 10; i++ {
		for _, q := range queries {
			got, err := Parse(q)
			if err != nil {
				t.Fatalf("Parse(%q): %v", q, err)
			}
			if want, ok := first[q]; !ok {
				first[q] = got
			} else if got != want {
				t.Fatalf("non-deterministic output for %q:\nfirst: %s\nlater: %s", q, want, got)
			}
		}
	}
}

// These express the intended user-visible behavior, rather than accepting
// contradictory filters as a successful translation. Phase 3 owns the parser
// changes; keep this gap explicit until ambiguity is handled conservatively.
func TestParseAmbiguousStatusIntent_KnownGap(t *testing.T) {
	for _, input := range []string{"open and done bugs", "done and in progress bugs"} {
		t.Run(input, func(t *testing.T) {
			t.Skip("known semantic gap: Phase 3 must reject ambiguous status conjunctions")
			if got, err := Parse(input); err == nil {
				t.Fatalf("ambiguous request %q must be rejected, got %q", input, got)
			}
		})
	}
}
