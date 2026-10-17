package nl2jql

import (
	"testing"
	"time"
)

func TestImplicitOrderByDirection(t *testing.T) {
	tests := []struct {
		name, input, self, want string
		sortCount               int
	}{
		{"reported wording", "my open issues ordered by date created", "", `statusCategory != Done AND assignee = currentUser() ORDER BY created ASC`, 1},
		{"list wording", "list my open issues ordered by date created", "", `statusCategory != Done AND assignee = currentUser() ORDER BY created ASC`, 1},
		{"created alias", "my issues ordered by created", "", `assignee = currentUser() ORDER BY created ASC`, 1},
		{"creation date alias", "my issues ordered by creation date", "", `assignee = currentUser() ORDER BY created ASC`, 1},
		{"assigned to me with open", "assigned to me open issues ordered by date created", "", `statusCategory != Done AND assignee = currentUser() ORDER BY created ASC`, 1},
		{"explicit ascending", "my issues ordered by created asc", "", `assignee = currentUser() ORDER BY created ASC`, 1},
		{"explicit descending", "my issues ordered by created desc", "", `assignee = currentUser() ORDER BY created DESC`, 1},
		{"ascending word", "my issues ordered by created ascending", "", `assignee = currentUser() ORDER BY created ASC`, 1},
		{"descending word", "my issues ordered by created descending", "", `assignee = currentUser() ORDER BY created DESC`, 1},
		{"mixed multikey", "my issues ordered by priority, created desc, updated", "", `assignee = currentUser() ORDER BY priority ASC, created DESC, updated ASC`, 3},
		{"no requested ordering", "my open issues", "", `statusCategory != Done AND assignee = currentUser()`, 0},
		{"date filter retained", `my open issues created >= startOfDay() AND created < startOfDay("+1") ordered by date created`, "", `statusCategory != Done AND assignee = currentUser() AND created >= startOfDay() AND created < startOfDay("+1") ORDER BY created ASC`, 1},
		{"self override", "my open issues ordered by date created", `"person@example.com"`, `statusCategory != Done AND assignee = "person@example.com" ORDER BY created ASC`, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
			wantParse := tt.want
			if tt.self != "" {
				wantParse = `statusCategory != Done AND assignee = currentUser() ORDER BY created ASC`
			}
			if got != wantParse {
				t.Errorf("Parse(%q) = %q, want %q", tt.input, got, wantParse)
			}

			plan, err := ParsePlan(tt.input, tt.self, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("ParsePlan(%q): %v", tt.input, err)
			}
			compiled, err := plan.Compile()
			if err != nil {
				t.Fatalf("Compile(): %v", err)
			}
			if got := compiled.Render(); got != tt.want {
				t.Errorf("ParsePlan(%q) compiled to %q, want %q", tt.input, got, tt.want)
			}
			if len(plan.Sort) != tt.sortCount {
				t.Errorf("ParsePlan(%q) sort count = %d, want %d", tt.input, len(plan.Sort), tt.sortCount)
			}
		})
	}
}

func TestImplicitOrderByStillRejectsMalformedOrdering(t *testing.T) {
	for _, input := range []string{
		"my issues ordered by",
		"my issues ordered by created upwards",
		"my issues ordered by sprint",
		"my issues ordered by created extra words",
		"my issues ordered by priority asc sorted by created desc",
		"my issues ordered by priority, created upwards",
	} {
		t.Run(input, func(t *testing.T) {
			if got, err := Parse(input); err == nil {
				t.Errorf("Parse(%q) = %q, want error", input, got)
			}
			if _, err := ParsePlan(input, "", time.Now()); err == nil {
				t.Errorf("ParsePlan(%q) succeeded, want error", input)
			}
		})
	}
}
