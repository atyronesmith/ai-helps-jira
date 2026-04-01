package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

type EnrichmentContent struct {
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Labels             []string `json:"labels"`
	Priority           string   `json:"priority"`
}

const EnrichSystemPrompt = `You are a senior product manager helping enrich sparse JIRA tickets.
Given an existing ticket's fields, generate improved content:

1. **Description**: A fuller description (2-3 paragraphs) that expands on the summary.
   Keep any existing description content and build on it. If the description is empty,
   write one from scratch based on the summary.
2. **Acceptance Criteria**: 3-6 testable criteria for this ticket.
3. **Labels**: 2-4 relevant labels (lowercase-hyphenated). Include any existing labels
   that are still relevant.
4. **Priority**: One of "Highest", "High", "Medium", "Low", "Lowest".
   Keep the current priority unless it seems clearly wrong.

Respond ONLY with valid JSON (no markdown fences) with these keys:
  description, acceptance_criteria (array of strings),
  labels (array of strings), priority`

// LoadEnrichPrompt looks for an ENHANCE.* file in the current directory.
// If found, returns its contents and the filename. Otherwise returns the
// built-in default prompt.
func LoadEnrichPrompt() (prompt, source string) {
	matches, _ := filepath.Glob("ENHANCE.*")
	if len(matches) > 0 {
		data, err := os.ReadFile(matches[0])
		if err != nil {
			slog.Warn("failed to read prompt file, using default", "file", matches[0], "error", err)
			return EnrichSystemPrompt, "(built-in)"
		}
		slog.Info("loaded custom enrich prompt", "file", matches[0])
		return string(data), matches[0]
	}
	return EnrichSystemPrompt, "(built-in)"
}

func GenerateEnrichment(cfg *config.Config, issue *jira.IssueDetail, systemPrompt string) (*EnrichmentContent, error) {
	ctx := context.Background()

	slog.Info("generating enrichment", "key", issue.Key)

	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, cfg.VertexRegion, cfg.VertexProjectID),
	)

	var parts []string
	parts = append(parts, fmt.Sprintf("Issue: %s", issue.Key))
	parts = append(parts, fmt.Sprintf("Type: %s", issue.IssueType))
	parts = append(parts, fmt.Sprintf("Summary: %s", issue.Summary))
	if issue.Description != "" {
		parts = append(parts, fmt.Sprintf("Current Description: %s", issue.Description))
	} else {
		parts = append(parts, "Current Description: (empty)")
	}
	if len(issue.Labels) > 0 {
		parts = append(parts, fmt.Sprintf("Current Labels: %s", strings.Join(issue.Labels, ", ")))
	} else {
		parts = append(parts, "Current Labels: (none)")
	}
	parts = append(parts, fmt.Sprintf("Current Priority: %s", issue.Priority))
	parts = append(parts, fmt.Sprintf("Status: %s", issue.Status))

	userMessage := strings.Join(parts, "\n")

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewTextBlock(userMessage),
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude api: %w", err)
	}

	slog.Info("enrichment LLM response",
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens)

	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}
	if text == "" {
		return nil, fmt.Errorf("no text in claude response")
	}
	slog.Debug("raw LLM response", "text", text)

	var result EnrichmentContent
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	slog.Info("enrichment generated", "key", issue.Key)
	return &result, nil
}

// BuildEnrichedDescription combines description and acceptance criteria.
func BuildEnrichedDescription(e *EnrichmentContent) string {
	var b strings.Builder
	b.WriteString(e.Description)
	if len(e.AcceptanceCriteria) > 0 {
		b.WriteString("\n\nh3. Acceptance Criteria\n")
		for _, c := range e.AcceptanceCriteria {
			fmt.Fprintf(&b, "* %s\n", c)
		}
	}
	return b.String()
}
