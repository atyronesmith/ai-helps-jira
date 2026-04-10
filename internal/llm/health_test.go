package llm

import (
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestCheckBacklogHealth_Stale(t *testing.T) {
	now := time.Now()
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "stale task", Status: "In Progress", IssueType: "Task", Updated: now.AddDate(0, 0, -20), ParentKey: "PROJ-100"},
		{Key: "PROJ-2", Summary: "fresh task", Status: "In Progress", IssueType: "Task", Updated: now.AddDate(0, 0, -3), ParentKey: "PROJ-100"},
		{Key: "PROJ-3", Summary: "stale but not active", Status: "To Do", IssueType: "Task", Updated: now.AddDate(0, 0, -30), ParentKey: "PROJ-100"},
	}

	findings := CheckBacklogHealth(issues, 14)

	stale := filterCategory(findings, "stale")
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale finding, got %d", len(stale))
	}
	if stale[0].Key != "PROJ-1" {
		t.Errorf("expected PROJ-1 stale, got %s", stale[0].Key)
	}
}

func TestCheckBacklogHealth_StaleDaysCustom(t *testing.T) {
	now := time.Now()
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "8 days old", Status: "In Progress", IssueType: "Task", Updated: now.AddDate(0, 0, -8), ParentKey: "PROJ-100"},
	}

	// Default 14 days — not stale
	findings14 := CheckBacklogHealth(issues, 14)
	if len(filterCategory(findings14, "stale")) != 0 {
		t.Error("should not be stale at 14-day threshold")
	}

	// Custom 7 days — stale
	findings7 := CheckBacklogHealth(issues, 7)
	if len(filterCategory(findings7, "stale")) != 1 {
		t.Error("should be stale at 7-day threshold")
	}
}

func TestCheckBacklogHealth_MissingDescription(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "no desc", Description: "", IssueType: "Task", Status: "To Do", Updated: time.Now(), ParentKey: "PROJ-100"},
		{Key: "PROJ-2", Summary: "has desc", Description: "some text", IssueType: "Task", Status: "To Do", Updated: time.Now(), ParentKey: "PROJ-100"},
		{Key: "PROJ-3", Summary: "whitespace only", Description: "   ", IssueType: "Task", Status: "To Do", Updated: time.Now(), ParentKey: "PROJ-100"},
	}

	findings := CheckBacklogHealth(issues, 14)
	missing := filterCategory(findings, "missing_description")
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing_description findings, got %d", len(missing))
	}
}

func TestCheckBacklogHealth_Orphaned(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "orphan task", IssueType: "Task", Status: "To Do", ParentKey: "", Updated: time.Now()},
		{Key: "PROJ-2", Summary: "has parent", IssueType: "Task", Status: "To Do", ParentKey: "PROJ-100", Updated: time.Now()},
		{Key: "PROJ-3", Summary: "epic itself", IssueType: "Epic", Status: "To Do", ParentKey: "", Updated: time.Now()},
		{Key: "PROJ-4", Summary: "initiative", IssueType: "Initiative", Status: "To Do", ParentKey: "", Updated: time.Now()},
		{Key: "PROJ-5", Summary: "feature", IssueType: "Feature", Status: "To Do", ParentKey: "", Updated: time.Now()},
	}

	findings := CheckBacklogHealth(issues, 14)
	orphaned := filterCategory(findings, "orphaned")
	if len(orphaned) != 1 {
		t.Fatalf("expected 1 orphaned finding (PROJ-1), got %d", len(orphaned))
	}
	if orphaned[0].Key != "PROJ-1" {
		t.Errorf("expected PROJ-1, got %s", orphaned[0].Key)
	}
}

func TestCheckBacklogHealth_UnassignedActive(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "active no assignee", Status: "In Progress", Assignee: "", IssueType: "Task", Updated: time.Now(), ParentKey: "PROJ-100"},
		{Key: "PROJ-2", Summary: "active with assignee", Status: "In Progress", Assignee: "alice", IssueType: "Task", Updated: time.Now(), ParentKey: "PROJ-100"},
		{Key: "PROJ-3", Summary: "todo no assignee", Status: "To Do", Assignee: "", IssueType: "Task", Updated: time.Now(), ParentKey: "PROJ-100"},
	}

	findings := CheckBacklogHealth(issues, 14)
	unassigned := filterCategory(findings, "unassigned_active")
	if len(unassigned) != 1 {
		t.Fatalf("expected 1 unassigned_active finding, got %d", len(unassigned))
	}
	if unassigned[0].Key != "PROJ-1" {
		t.Errorf("expected PROJ-1, got %s", unassigned[0].Key)
	}
}

func TestCheckBacklogHealth_MissingLabels(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "no labels", Labels: nil, IssueType: "Task", Status: "To Do", Updated: time.Now(), ParentKey: "PROJ-100"},
		{Key: "PROJ-2", Summary: "empty labels", Labels: []string{}, IssueType: "Task", Status: "To Do", Updated: time.Now(), ParentKey: "PROJ-100"},
		{Key: "PROJ-3", Summary: "has labels", Labels: []string{"backend"}, IssueType: "Task", Status: "To Do", Updated: time.Now(), ParentKey: "PROJ-100"},
	}

	findings := CheckBacklogHealth(issues, 14)
	missing := filterCategory(findings, "missing_labels")
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing_labels findings, got %d", len(missing))
	}
}

func TestCheckBacklogHealth_MultipleProblems(t *testing.T) {
	now := time.Now()
	// One issue with multiple problems
	issues := []*jira.IssueDetail{
		{
			Key:         "PROJ-1",
			Summary:     "problematic ticket",
			Description: "",
			Status:      "In Progress",
			IssueType:   "Task",
			Assignee:    "",
			Labels:      nil,
			ParentKey:   "",
			Updated:     now.AddDate(0, 0, -30),
		},
	}

	findings := CheckBacklogHealth(issues, 14)

	// Should have findings in all 5 categories
	categories := make(map[string]bool)
	for _, f := range findings {
		categories[f.Category] = true
	}

	expected := []string{"stale", "missing_description", "orphaned", "unassigned_active", "missing_labels"}
	for _, cat := range expected {
		if !categories[cat] {
			t.Errorf("expected finding in category %q", cat)
		}
	}
}

func TestCheckBacklogHealth_HealthyBacklog(t *testing.T) {
	issues := []*jira.IssueDetail{
		{
			Key:         "PROJ-1",
			Summary:     "good ticket",
			Description: "Well described task",
			Status:      "In Progress",
			IssueType:   "Task",
			Assignee:    "alice",
			Labels:      []string{"backend"},
			ParentKey:   "PROJ-100",
			Updated:     time.Now(),
		},
	}

	findings := CheckBacklogHealth(issues, 14)
	if len(findings) != 0 {
		t.Errorf("expected no findings for healthy issue, got %d", len(findings))
	}
}

func TestCheckBacklogHealth_ActiveStatuses(t *testing.T) {
	now := time.Now()
	staleDate := now.AddDate(0, 0, -20)

	statuses := []struct {
		status   string
		isActive bool
	}{
		{"In Progress", true},
		{"In Review", true},
		{"In Dev", true},
		{"In QA", true},
		{"In Test", true},
		{"Reviewing", true},
		{"To Do", false},
		{"New", false},
		{"Done", false},
		{"Backlog", false},
	}

	for _, tc := range statuses {
		issues := []*jira.IssueDetail{
			{Key: "PROJ-1", Summary: "test", Status: tc.status, IssueType: "Task",
				Updated: staleDate, ParentKey: "PROJ-100", Assignee: "alice",
				Labels: []string{"x"}, Description: "desc"},
		}
		findings := CheckBacklogHealth(issues, 14)
		stale := filterCategory(findings, "stale")
		if tc.isActive && len(stale) != 1 {
			t.Errorf("status %q should be considered active (stale), got %d findings", tc.status, len(stale))
		}
		if !tc.isActive && len(stale) != 0 {
			t.Errorf("status %q should not be considered active, got %d stale findings", tc.status, len(stale))
		}
	}
}

func filterCategory(findings []HealthFinding, category string) []HealthFinding {
	var result []HealthFinding
	for _, f := range findings {
		if f.Category == category {
			result = append(result, f)
		}
	}
	return result
}
