package jira

import "time"

type Issue struct {
	Key      string
	Status   string
	Priority string
	Summary  string
	Updated  time.Time
	Assignee string // email address of assignee
	Board    string // board name this issue was found on
	Sprint   string // sprint name, empty if kanban
}

// IssueDetail holds full issue fields for enrichment/editing.
type IssueDetail struct {
	Key           string
	Summary       string
	Description   string // plain text extracted from ADF
	Status        string
	Priority      string
	Labels        []string
	IssueType     string
	Assignee      string    // display name
	AssigneeID    string    // account ID for API operations
	ParentKey     string    // parent issue key (if any)
	ParentSummary string    // parent issue summary
	Updated       time.Time // last update time from JIRA
}

// Comment represents a single JIRA issue comment.
type Comment struct {
	ID          string
	IssueKey    string
	AuthorName  string
	AuthorEmail string
	Body        string // plain text extracted from ADF
	Created     time.Time
}

// IssueLink represents a directional link between two issues.
type IssueLink struct {
	SourceKey     string
	TargetKey     string
	LinkType      string // e.g. "is parent of", "Epic Link"
	Direction     string // "inward" or "outward"
	TargetSummary string
	TargetStatus  string
	TargetType    string // issue type of target
}

// Transition represents an available workflow transition for an issue.
type Transition struct {
	ID   string
	Name string
	To   string // target status name
}

// User represents a JIRA user.
type User struct {
	AccountID    string
	DisplayName  string
	EmailAddress string
}

type BoardInfo struct {
	Name       string
	BoardType  string // "scrum" or "kanban"
	SprintName string // empty if kanban
	Issues     []Issue
}

// Worklog represents a single JIRA worklog entry.
type Worklog struct {
	ID               string
	IssueKey         string
	AuthorName       string
	TimeSpent        string
	TimeSpentSeconds int
	Started          time.Time
	Comment          string
}
