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

type BoardInfo struct {
	Name       string
	BoardType  string // "scrum" or "kanban"
	SprintName string // empty if kanban
	Issues     []Issue
}
