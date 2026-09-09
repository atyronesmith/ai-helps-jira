package nl2jql

import (
	"errors"
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
			name:  "canonical example",
			query: "my open bugs in Support",
			want:  `project = Support AND issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved") AND assignee = currentUser()`,
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
			want:  `project = Support AND issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved")`,
		},
		{
			name:  "multi-word assignee name",
			query: "bugs assigned to John Smith in Support",
			want:  `project = Support AND issuetype = Bug AND assignee = "John Smith"`,
		},
		{
			name:  "multi-word project key gets quoted",
			query: "open bugs in Eng Platform",
			want:  `project = "Eng Platform" AND issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved")`,
		},
		{
			name:  "stop words between type and status",
			query: "a bug that is open",
			want:  `issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved")`,
		},
		{
			name:  "matching is case-insensitive",
			query: "MY DONE EPICS IN eng",
			want:  `project = eng AND issuetype = Epic AND status = Done AND assignee = currentUser()`,
		},
		{
			name:  "clauses in any order render identically",
			query: "bugs in Support open my",
			want:  `project = Support AND issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved") AND assignee = currentUser()`,
		},
		{
			name:  "duplicate ownership expressions collapse",
			query: "my open bugs assigned to me",
			want:  `issuetype = Bug AND status NOT IN ("Done", "Closed", "Resolved") AND assignee = currentUser()`,
		},
		{
			name:  "punctuation is elided",
			query: "show me all open tickets, please!",
			want:  `status NOT IN ("Done", "Closed", "Resolved")`,
		},
		{
			name:  "singular issue types",
			query: "an open task",
			want:  `issuetype = Task AND status NOT IN ("Done", "Closed", "Resolved")`,
		},
		{
			name:  "hyphenated in-progress",
			query: "bugs in-progress",
			want:  `issuetype = Bug AND status = "In Progress"`,
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
