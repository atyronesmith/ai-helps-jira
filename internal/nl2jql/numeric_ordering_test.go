package nl2jql

import (
	"testing"
	"time"
)

func TestNumericAndDurationOrderingAliases(t *testing.T) {
	tests := []struct{ input, want string }{
		{"list my work items by original estimate", `assignee = currentUser() ORDER BY originalEstimate DESC`},
		{"list my work items by original work estimate", `assignee = currentUser() ORDER BY originalEstimate DESC`},
		{"list my work items by originalEstimate", `assignee = currentUser() ORDER BY originalEstimate DESC`},
		{"list my work items by logged work time", `assignee = currentUser() ORDER BY timespent DESC`},
		{"list my work items by logged time", `assignee = currentUser() ORDER BY timespent DESC`},
		{"list my work items by time spent", `assignee = currentUser() ORDER BY timespent DESC`},
		{"list my work items by timespent", `assignee = currentUser() ORDER BY timespent DESC`},
		{"list my work items by amount of work remaining", `assignee = currentUser() ORDER BY remainingEstimate DESC`},
		{"list my work items by work remaining", `assignee = currentUser() ORDER BY remainingEstimate DESC`},
		{"list my work items by remaining estimate", `assignee = currentUser() ORDER BY remainingEstimate DESC`},
		{"list my work items by remainingEstimate", `assignee = currentUser() ORDER BY remainingEstimate DESC`},
		{"list my work items by story points", `assignee = currentUser() ORDER BY "Story Points" DESC`},
		{"list my work items ordered by priority", `assignee = currentUser() ORDER BY priority DESC`},
		{"list my work items ordered by created", `assignee = currentUser() ORDER BY created ASC`},
		{"list my work items ordered by last updated", `assignee = currentUser() ORDER BY updated DESC`},
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

func TestNumericAndDurationOrderingExplicitDirections(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"list my work items by original estimate asc", `assignee = currentUser() ORDER BY originalEstimate ASC`},
		{"list my work items by logged work time ascending", `assignee = currentUser() ORDER BY timespent ASC`},
		{"list my work items by remaining estimate desc", `assignee = currentUser() ORDER BY remainingEstimate DESC`},
		{"list my work items by story points ascending", `assignee = currentUser() ORDER BY "Story Points" ASC`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != tt.want {
				t.Errorf("Parse = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNumericOrderingRejectsUnknownFieldsAndExtraWords(t *testing.T) {
	for _, input := range []string{
		"list my work items by arbitrary custom field",
		"list my work items by logged time with extra words",
		"list my work items by original estimate upwards",
		"list my work items ordered by remaining estimate extra",
		"list my work items by",
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
