package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

func GenerateEpicContent(cfg *config.Config, userDescription string) (*EpicContent, error) {
	ctx := context.Background()

	slog.Info("initializing Vertex client",
		"project", cfg.VertexProjectID, "region", cfg.VertexRegion)

	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, cfg.VertexRegion, cfg.VertexProjectID),
	)

	slog.Info("sending request to Claude")
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: EpicSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewTextBlock(fmt.Sprintf("Create an EPIC for: %s", userDescription)),
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude api: %w", err)
	}

	slog.Info("response received",
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens)

	// Extract text content
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

	var epic EpicContent
	if err := json.Unmarshal([]byte(text), &epic); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	slog.Info("parsed EPIC content", "summary", epic.Summary)
	return &epic, nil
}
