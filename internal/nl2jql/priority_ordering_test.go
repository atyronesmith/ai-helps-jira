package nl2jql

import (
	"testing"
	"time"
)

func TestPriorityEnumerationAndScopedOrderingFrame(t *testing.T) {
	tests := []struct{ input, want string }{
		{
			"list my open work items where the priority is either major or minor and order by priority, then updated",
			`statusCategory != Done AND assignee = currentUser() AND priority IN (Major, Minor) ORDER BY priority DESC, updated DESC`,
		},
		{
			"list my open work items where the priority is either mAjOr or MINOR and order by priority, then updated",
			`statusCategory != Done AND assignee = currentUser() AND priority IN (Major, Minor) ORDER BY priority DESC, updated DESC`,
		},
		{
			"list my open work items where the priority is either major or minor and order by updated",
			`statusCategory != Done AND assignee = currentUser() AND priority IN (Major, Minor) ORDER BY updated DESC`,
		},
		{
			"list my open work items where the priority is either major or minor and order by priority then updated",
			`statusCategory != Done AND assignee = currentUser() AND priority IN (Major, Minor) ORDER BY priority DESC, updated DESC`,
		},
		{
			"list my open work items where the priority is either major or minor and order by priority, updated",
			`statusCategory != Done AND assignee = currentUser() AND priority IN (Major, Minor) ORDER BY priority DESC, updated DESC`,
		},
		{
			"list my open work items where the priority is either major or minor and order by priority asc, then updated asc",
			`statusCategory != Done AND assignee = currentUser() AND priority IN (Major, Minor) ORDER BY priority ASC, updated ASC`,
		},
		{
			"list my work items where the priority is either Major or Minor",
			`assignee = currentUser() AND priority IN (Major, Minor)`,
		},
		{
			"list my work items where the priority is either critical or highest",
			`assignee = currentUser() AND priority IN (Critical, Highest)`,
		},
		{
			"list my Major work items or minor work items ordered by created",
			`assignee = currentUser() AND priority IN (Major, Minor) ORDER BY created ASC`,
		},
		{
			"assigned to by@example.com by updated",
			`assignee = "by@example.com" ORDER BY updated DESC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != tt.want {
				t.Errorf("Parse = %q, want %q", got, tt.want)
			}
			plan, err := ParsePlan(tt.input, "", time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("ParsePlan: %v", err)
			}
			compiled, err := plan.Compile()
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got := compiled.Render(); got != tt.want {
				t.Errorf("ParsePlan = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPriorityOrderingRejectsMalformedBooleanAndSortFrames(t *testing.T) {
	for _, input := range []string{
		"list my work items where the priority is either major or minor or order by priority",
		"list my work items where the priority is either major and minor",
		"list my work items where the priority is either major",
		"list my either work items",
		"list my work items where the priority is either major or minor and order by priority then",
		"list my work items where the priority is either major or minor and order by priority, then updated extra",
		"list my work items where the priority is either major or minor and order by priority upward",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse(input); err == nil {
				t.Error("Parse succeeded, want error")
			}
			if _, err := ParsePlan(input, "", time.Now()); err == nil {
				t.Error("ParsePlan succeeded, want error")
			}
		})
	}
}

func TestPriorityOrderingRespectsExplicitEffectiveUser(t *testing.T) {
	plan, err := ParsePlan("list my open work items where the priority is either major or minor and order by priority, then updated", `"person@example.com"`, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	compiled, err := plan.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got, want := compiled.Render(), `statusCategory != Done AND assignee = "person@example.com" AND priority IN (Major, Minor) ORDER BY priority DESC, updated DESC`; got != want {
		t.Errorf("compiled query = %q, want %q", got, want)
	}
}
