package nl2jql

import (
	"testing"
	"time"
)

func TestBatchSearchTextAndSummaryAliases(t *testing.T) {
	tests := []struct{ input, want string }{
		{"list my work items that contain the words Atlas, Beacon or Compass", `assignee = currentUser() AND text ~ "Atlas OR Beacon OR Compass"`},
		{"list my work items contain the words Open, Critical, bugs", `assignee = currentUser() AND text ~ "Open OR Critical OR bugs"`},
		{"list my work items that contain the words Bug OR Major", `assignee = currentUser() AND text ~ "Bug OR Major"`},
		{"list my work items ordered by summary text", `assignee = currentUser() ORDER BY summary ASC`},
		{"list my work items ordered by title", `assignee = currentUser() ORDER BY summary ASC`},
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

func TestBatchSearchChildOfEpic(t *testing.T) {
	for _, input := range []string{
		"list my work items that are child tasks of an epic",
		"list my work items child tasks of an epic",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := Parse(input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			want := `assignee = currentUser() AND parent IS NOT EMPTY AND issuetype NOT IN subtaskIssueTypes()`
			if got != want {
				t.Errorf("Parse = %q, want %q", got, want)
			}
			plan, err := ParsePlan(input, "", time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("ParsePlan: %v", err)
			}
			compiled, err := plan.Compile()
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got := compiled.Render(); got != want {
				t.Errorf("ParsePlan = %q, want %q", got, want)
			}
		})
	}
}

func TestBatchSearchRecognizedEmptyOrMalformedListsFail(t *testing.T) {
	for _, input := range []string{
		"list my work items that contain the words",
		"list my work items that contain the words and open",
		"list my work items that contain the words alpha or",
		"list my work items that contain the words alpha, or",
		"list my work items contain the words alpha beta extra",
		"list my work items contain the words alpha and open",
		"list my work items contain the words alpha priority Major",
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
