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
	Key         string
	Summary     string
	Description string // plain text extracted from ADF
	Status      string
	Priority    string
	Labels      []string
	IssueType   string
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

type BoardInfo struct {
	Name       string
	BoardType  string // "scrum" or "kanban"
	SprintName string // empty if kanban
	Issues     []Issue
}
