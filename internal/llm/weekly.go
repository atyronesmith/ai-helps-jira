package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// WeeklyStatusContent is the structured output from the LLM.
type WeeklyStatusContent struct {
	UserName string              `json:"user_name"`
	Projects []WeeklyProjectItem `json:"projects"`
}

// WeeklyProjectItem groups work items under a project/epic heading.
type WeeklyProjectItem struct {
	ProjectName string   `json:"project_name"`
	IssueKey    string   `json:"issue_key"`
	Bullets     []string `json:"bullets"`
}

const WeeklyStatusSystemPrompt = `You are a program manager writing a weekly status report from JIRA activity data.

You are given a list of JIRA issues that a user worked on during a specific date range, along with comments they wrote during that period and issue metadata (status, transitions, parent epics).

Produce a structured weekly status report:

1. Group items by their parent epic or project theme. Each group becomes a "project".
2. For each project group:
   - Use the parent epic's summary as the project name (or the issue summary if standalone)
   - Include the parent epic's issue key (or the issue key if standalone)
   - Write narrative bullets:
     - First bullet should explain WHY this work matters (background/motivation)
     - Subsequent bullets describe WHAT was done, using specific technical details from the comments
     - Use past tense
     - Never say "shipped" — nothing is shipped
     - Be specific: include numbers, tool names, technical details from comments
     - Each bullet should be a complete thought, 1-3 sentences

3. If an issue has no parent epic, group it on its own.

Respond ONLY with valid JSON (no markdown fences) with these keys:
  user_name (string - the user's display name or email),
  projects (array of {project_name, issue_key, bullets: [string]})`

// IssueWithComments pairs an issue with its relevant comments.
type IssueWithComments struct {
	Issue    jira.IssueDetail
	Comments []jira.Comment
	Parent   *jira.IssueDetail // parent epic/initiative, if any
}

func GenerateWeeklyStatus(cfg *config.Config, items []IssueWithComments, startDate, endDate string) (*WeeklyStatusContent, error) {
	ctx := context.Background()

	slog.Info("generating weekly status", "issues", len(items), "start", startDate, "end", endDate)

	provider, err := NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM provider: %w", err)
	}

	// Build user message with all context
	var parts []string
	parts = append(parts, fmt.Sprintf("## Weekly Status Report: %s to %s", startDate, endDate))
	parts = append(parts, fmt.Sprintf("User: %s", cfg.Assignee()))

	for _, item := range items {
		parts = append(parts, fmt.Sprintf("\n### %s — %s", item.Issue.Key, item.Issue.Summary))
		parts = append(parts, fmt.Sprintf("Type: %s | Status: %s | Priority: %s",
			item.Issue.IssueType, item.Issue.Status, item.Issue.Priority))

		if item.Parent != nil {
			parts = append(parts, fmt.Sprintf("Parent: %s — %s (%s)",
				item.Parent.Key, item.Parent.Summary, item.Parent.IssueType))
		}

		if item.Issue.Description != "" {
			desc := item.Issue.Description
			if len(desc) > 500 {
				desc = desc[:500] + "..."
			}
			parts = append(parts, fmt.Sprintf("Description: %s", desc))
		}

		if len(item.Comments) > 0 {
			parts = append(parts, fmt.Sprintf("Comments from this period (%d):", len(item.Comments)))
			for _, c := range item.Comments {
				body := c.Body
				if len(body) > 1000 {
					body = body[:1000] + "..."
				}
				parts = append(parts, fmt.Sprintf("  [%s] %s: %s",
					c.Created.Format("2006-01-02"), c.AuthorName, body))
			}
		} else {
			parts = append(parts, "No comments during this period (status/field changes only).")
		}
	}

	userMessage := strings.Join(parts, "\n")

	text, err := provider.Complete(ctx, WeeklyStatusSystemPrompt, userMessage, 8192)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}
	slog.Debug("raw LLM response", "text", text)

	var result WeeklyStatusContent
	if err := json.Unmarshal([]byte(cleanJSON(text)), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	slog.Info("weekly status generated", "projects", len(result.Projects))
	return &result, nil
}
