package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

func GenerateEpicContent(cfg *config.Config, userDescription string) (*EpicContent, error) {
	ctx := context.Background()

	provider, err := NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM provider: %w", err)
	}

	text, err := provider.Complete(ctx, EpicSystemPrompt, fmt.Sprintf("Create an EPIC for: %s", userDescription), 4096)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}
	slog.Debug("raw LLM response", "text", text)

	var epic EpicContent
	if err := json.Unmarshal([]byte(cleanJSON(text)), &epic); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	slog.Info("parsed EPIC content", "summary", epic.Summary)
	return &epic, nil
}
