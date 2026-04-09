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

type DigestContent struct {
	OverallStatus   string           `json:"overall_status"`
	ProgressUpdates []ProgressItem   `json:"progress_updates"`
	Blockers        []BlockerItem    `json:"blockers"`
	NotStarted      []NotStartedItem `json:"not_started"`
	Summary         string           `json:"summary"`
}

type ProgressItem struct {
	EpicKey     string `json:"epic_key"`
	EpicSummary string `json:"epic_summary"`
	Status      string `json:"status"`
	Update      string `json:"update"`
}

type BlockerItem struct {
	EpicKey     string `json:"epic_key"`
	EpicSummary string `json:"epic_summary"`
	Blocker     string `json:"blocker"`
	Impact      string `json:"impact"`
}

type NotStartedItem struct {
	EpicKey     string `json:"epic_key"`
	EpicSummary string `json:"epic_summary"`
	Reason      string `json:"reason"`
}

const DigestSystemPrompt = `You are a senior program manager analyzing JIRA data for an executive digest.

You are given a parent issue (Initiative, Feature, or Epic) and its linked child
issues, which may span multiple hierarchy levels (Initiative → Feature → Epic).
Recent comments from child issues are included.

Analyze the data and produce a structured digest:

1. **Overall Status**: One of "on track", "at risk", or "blocked"
2. **Progress Updates**: For each issue with recent activity, summarize what happened.
   Use the issue key (e.g. EPIC-123) regardless of its type in the hierarchy.
3. **Blockers**: Any issues flagged as blocked, waiting on external teams, or stalled
4. **Not Started**: Issues that appear to have no progress or comments but should have
   started based on their status or context
5. **Summary**: 2-3 sentence executive summary of progress across the hierarchy

Respond ONLY with valid JSON (no markdown fences) with these keys:
  overall_status (string),
  progress_updates (array of {epic_key, epic_summary, status, update}),
  blockers (array of {epic_key, epic_summary, blocker, impact}),
  not_started (array of {epic_key, epic_summary, reason}),
  summary (string)`

func GenerateDigest(cfg *config.Config, parent *jira.IssueDetail, links []jira.IssueLink, comments []jira.Comment) (*DigestContent, error) {
	ctx := context.Background()

	slog.Info("generating digest", "parent", parent.Key, "links", len(links), "comments", len(comments))

	provider, err := NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM provider: %w", err)
	}

	// Build user message with all context
	var parts []string
	parts = append(parts, fmt.Sprintf("## Parent Issue: %s", parent.Key))
	parts = append(parts, fmt.Sprintf("Type: %s", parent.IssueType))
	parts = append(parts, fmt.Sprintf("Summary: %s", parent.Summary))
	parts = append(parts, fmt.Sprintf("Status: %s", parent.Status))
	if parent.Description != "" {
		parts = append(parts, fmt.Sprintf("Description: %s", parent.Description))
	}

	// Group comments by issue key for easier reference
	commentsByKey := make(map[string][]jira.Comment)
	for _, c := range comments {
		commentsByKey[c.IssueKey] = append(commentsByKey[c.IssueKey], c)
	}

	parts = append(parts, fmt.Sprintf("\n## Linked Issues (%d)", len(links)))
	for _, link := range links {
		parts = append(parts, fmt.Sprintf("\n### %s — %s", link.TargetKey, link.TargetSummary))
		parts = append(parts, fmt.Sprintf("Status: %s | Type: %s | Link: %s (%s)",
			link.TargetStatus, link.TargetType, link.LinkType, link.Direction))

		epicComments := commentsByKey[link.TargetKey]
		if len(epicComments) > 0 {
			parts = append(parts, fmt.Sprintf("Recent comments (%d):", len(epicComments)))
			for _, c := range epicComments {
				parts = append(parts, fmt.Sprintf("  [%s] %s: %s",
					c.Created.Format("2006-01-02"), c.AuthorName, c.Body))
			}
		} else {
			parts = append(parts, "No recent comments.")
		}
	}

	userMessage := strings.Join(parts, "\n")

	text, err := provider.Complete(ctx, DigestSystemPrompt, userMessage, 8192)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}
	slog.Debug("raw LLM response", "text", text)

	var result DigestContent
	if err := json.Unmarshal([]byte(cleanJSON(text)), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	slog.Info("digest generated", "parent", parent.Key, "status", result.OverallStatus)
	return &result, nil
}
