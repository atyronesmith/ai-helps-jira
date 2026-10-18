package nl2jql

import (
	"errors"
	"testing"
	"time"
)

func TestReviewedLastUpdatedOrderingPhrases(t *testing.T) {
	tests := []struct{ input, want string }{
		{"list my work items ordered by last updated", `assignee = currentUser() ORDER BY updated DESC`},
		{"list my work items in order of when last updated", `assignee = currentUser() ORDER BY updated DESC`},
		{"list my Critical work items ordered by last updated", `assignee = currentUser() AND priority = Critical ORDER BY updated DESC`},
		{"list my High work items ordered by last updated", `assignee = currentUser() AND priority = High ORDER BY updated DESC`},
		{"list my Critical priority work items ordered by last updated", `assignee = currentUser() AND priority = Critical ORDER BY updated DESC`},
		{"list my priority Critical work items ordered by last updated", `assignee = currentUser() AND priority = Critical ORDER BY updated DESC`},
		{"list my work items ordered by last updated asc", `assignee = currentUser() ORDER BY updated ASC`},
		{"list my work items ordered by last updated ascending", `assignee = currentUser() ORDER BY updated ASC`},
		{"list my work items ordered by last updated desc", `assignee = currentUser() ORDER BY updated DESC`},
		{"list my work items ordered by updated", `assignee = currentUser() ORDER BY updated DESC`},
		{"list my work items ordered by updated date", `assignee = currentUser() ORDER BY updated DESC`},
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

func TestChronologicalOrderingUsesDateFieldType(t *testing.T) {
	tests := []struct{ input, want string }{
		{"list my work items ordered by created from oldest to newest", `assignee = currentUser() ORDER BY created ASC`},
		{"list my work items ordered by last updated from oldest to newest", `assignee = currentUser() ORDER BY updated ASC`},
		{"list my work items ordered by due date from newest to oldest", `assignee = currentUser() ORDER BY due DESC`},
		{"list my work items ordered by resolution date oldest to newest", `assignee = currentUser() ORDER BY resolved ASC`},
		{"list my work items ordered by created from oldest to newest, last updated from newest to oldest", `assignee = currentUser() ORDER BY created ASC, updated DESC`},
		{"list my work items ordered by last updated", `assignee = currentUser() ORDER BY updated DESC`},
		{"list my work items ordered by updated date", `assignee = currentUser() ORDER BY updated DESC`},
		{"list my work items ordered by last updated asc", `assignee = currentUser() ORDER BY updated ASC`},
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

func TestChronologicalDirectionNormalizesCaseAndWhitespace(t *testing.T) {
	input := "list my work items ordered by created FROM\n  OLDEST   TO newest"
	ast, err := jqlParser.ParseString("", input)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	plan, err := ast.Plan("currentUser()", time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	compiled, err := plan.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got, want := compiled.Render(), `assignee = currentUser() ORDER BY created ASC`; got != want {
		t.Errorf("compiled query = %q, want %q", got, want)
	}
}

func TestChronologicalOrderingRejectsNonDateOrMissingFields(t *testing.T) {
	for _, input := range []string{
		"list my work items ordered by summary from oldest to newest",
		"list my work items ordered by priority from oldest to newest",
		"list my work items with story points > 3 ordered by story points from newest to oldest",
		"list my work items from oldest to newest",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := ParsePlan(input, "", time.Now())
			if err == nil {
				t.Fatal("ParsePlan succeeded, want error")
			}
			if !errors.Is(err, ErrAmbiguousIntent) {
				t.Errorf("ParsePlan error = %v, want ErrAmbiguousIntent", err)
			}
		})
	}
	for _, input := range []string{
		"list my work items ordered by from oldest to newest",
		"list my work items ordered by created oldest newest",
		"list my work items ordered by created from newest oldest",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := ParsePlan(input, "", time.Now())
			if err == nil {
				t.Fatal("ParsePlan succeeded, want error")
			}
		})
	}
}

func TestReviewedOrderingFramesRejectMalformedInput(t *testing.T) {
	for _, input := range []string{
		"list my work items in order of when",
		"list my work items ordered by last updated upwards",
		"list my work items in order of when last updated asc sorted by created",
		"list my work items ordered by last updated extra words",
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
