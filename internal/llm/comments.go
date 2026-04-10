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

// CommentSummary holds the structured summary of a comment thread.
type CommentSummary struct {
	Summary       string   `json:"summary"`
	KeyDecisions  []string `json:"key_decisions"`
	ActionItems   []string `json:"action_items"`
	OpenQuestions []string `json:"open_questions"`
}

const commentSummaryPrompt = `You are an expert at summarizing JIRA comment threads.
Given an issue's details and its comment thread, produce a structured summary:

1. **Summary**: A concise 2-3 sentence summary of the discussion.
2. **Key Decisions**: Decisions that were made in the thread (if any).
3. **Action Items**: Tasks or follow-ups mentioned (if any). Include who owns them if mentioned.
4. **Open Questions**: Unresolved questions or topics needing follow-up (if any).

If a section has no items, return an empty array for it.

Respond ONLY with valid JSON (no markdown fences) with these keys:
  summary, key_decisions (array of strings),
  action_items (array of strings), open_questions (array of strings)`

// GenerateCommentSummary uses an LLM to summarize a comment thread.
func GenerateCommentSummary(cfg *config.Config, issue *jira.IssueDetail, comments []jira.Comment) (*CommentSummary, error) {
	ctx := context.Background()

	slog.Info("generating comment summary", "key", issue.Key, "comments", len(comments))

	provider, err := NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM provider: %w", err)
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("Issue: %s", issue.Key))
	parts = append(parts, fmt.Sprintf("Summary: %s", issue.Summary))
	parts = append(parts, fmt.Sprintf("Type: %s", issue.IssueType))
	parts = append(parts, fmt.Sprintf("Status: %s", issue.Status))
	if issue.Description != "" {
		parts = append(parts, fmt.Sprintf("Description: %s", issue.Description))
	}
	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("Comment Thread (%d comments):", len(comments)))
	for _, c := range comments {
		parts = append(parts, fmt.Sprintf("\n--- %s (%s) ---\n%s",
			c.AuthorName, c.Created.Format("2006-01-02 15:04"), c.Body))
	}

	userMessage := strings.Join(parts, "\n")

	text, err := provider.Complete(ctx, commentSummaryPrompt, userMessage, 4096)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}
	slog.Debug("raw LLM response", "text", text)

	var result CommentSummary
	if err := json.Unmarshal([]byte(cleanJSON(text)), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	slog.Info("comment summary generated", "key", issue.Key)
	return &result, nil
}
