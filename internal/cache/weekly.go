package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

type weeklyIssueFingerprint struct {
	Key            string `json:"key"`
	Updated        string `json:"updated"`
	StatusCategory string `json:"status_category"`
	Project        string `json:"project"`
	Assignee       string `json:"assignee"`
	AssigneeID     string `json:"assignee_id"`
	Summary        string `json:"summary"`
}

type weeklyFingerprint struct {
	Scope   string                   `json:"scope"`
	Owner   string                   `json:"owner"`
	Project string                   `json:"project"`
	Start   string                   `json:"start"`
	End     string                   `json:"end"`
	Issues  []weeklyIssueFingerprint `json:"issues"`
}

// WeeklyCacheKey fingerprints the complete current issue set and the identity
// and date range used to produce a weekly report. The returned value contains
// only a digest, never configuration or issue text.
func WeeklyCacheKey(cfg *config.Config, start, end string, issues []jira.Issue) string {
	scope, _ := scopeName(cfg)
	var owner, project string
	if cfg != nil {
		owner = cfg.Assignee()
		project = cfg.JiraProject
	}

	rows := make([]weeklyIssueFingerprint, 0, len(issues))
	for _, issue := range issues {
		updated := issue.Updated.UTC().Format(time.RFC3339Nano)
		rows = append(rows, weeklyIssueFingerprint{
			Key: issue.Key, Updated: updated, StatusCategory: issue.StatusCategory,
			Project: issue.Project, Assignee: issue.Assignee, AssigneeID: issue.AssigneeID,
			Summary: issue.Summary,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Key != rows[j].Key {
			return rows[i].Key < rows[j].Key
		}
		left, _ := json.Marshal(rows[i])
		right, _ := json.Marshal(rows[j])
		return string(left) < string(right)
	})
	payload, _ := json.Marshal(weeklyFingerprint{
		Scope: scope, Owner: owner, Project: project, Start: start, End: end, Issues: rows,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
