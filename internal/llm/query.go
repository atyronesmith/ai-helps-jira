package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

type QueryResult struct {
	JQL  string `json:"jql"`
	Days int    `json:"days,omitempty"` // optional time window extracted from natural language
}

const QuerySystemPrompt = `You are a JIRA JQL expert. Translate the user's natural-language query into valid JQL.

Context:
- The JIRA project key is %s
- The current user is referred to as %s in JQL
- Today's date is %s

Common JQL fields: project, assignee, reporter, status, priority, issuetype, labels, summary, description, created, updated, resolved, sprint, fixVersion, component.

Common statuses: "To Do", "In Progress", "In Review", "Done", "Closed", "Resolved".
Common priorities: Highest, High, Medium, Low, Lowest.
Common issue types: Bug, Story, Task, Epic, Sub-task.

Date functions: now(), startOfDay(), startOfWeek(), startOfMonth(), endOfDay(), endOfWeek(), endOfMonth().
Relative dates: "-1w" (one week ago), "-1d" (one day ago), "-2w", "-1m", etc.

Rules:
1. Always scope to project = %s unless the user explicitly asks about a different project or all projects.
2. If the user mentions "my" or "me", use assignee = %s.
3. If the user mentions a specific person by name or username, use assignee = "name" or assignee = "email".
4. Use ORDER BY when it improves readability (e.g. priority ASC, created DESC).
5. If the user mentions a time window for viewing changes or activity (e.g. "last day", "past 2 weeks", "since Monday"), include a "days" field with the number of days. Do NOT put time constraints in the JQL for this — the days field controls a separate comment filter. Only use date constraints in JQL when filtering by issue creation/update dates. Omit the "days" field if no time window is mentioned.
6. Respond ONLY with valid JSON (no markdown fences): {"jql": "your JQL here"} or {"jql": "your JQL here", "days": 1}`

func GenerateJQL(cfg *config.Config, naturalQuery string) (*QueryResult, error) {
	ctx := context.Background()

	slog.Info("generating JQL", "query", naturalQuery)

	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, cfg.VertexRegion, cfg.VertexProjectID),
	)

	today := time.Now().Format("2006-01-02")
	systemPrompt := fmt.Sprintf(QuerySystemPrompt,
		cfg.JiraProject, cfg.Assignee(), today,
		cfg.JiraProject, cfg.Assignee(),
	)

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewTextBlock(naturalQuery),
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude api: %w", err)
	}

	slog.Info("query LLM response",
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

	var result QueryResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}
	if result.JQL == "" {
		return nil, fmt.Errorf("LLM returned empty JQL")
	}

	slog.Info("generated JQL", "jql", result.JQL)
	return &result, nil
}
