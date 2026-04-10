package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

// Provider abstracts LLM completion so different backends can be used.
type Provider interface {
	Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error)
}

// VertexProvider uses Claude on Google Cloud Vertex AI.
type VertexProvider struct {
	client    anthropic.Client
	model     string
	projectID string
	region    string
}

// OpenAIProvider uses any OpenAI-compatible API (Ollama, DeepInfra, OpenAI, vLLM, etc.).
type OpenAIProvider struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewProvider creates an LLM provider based on environment configuration.
//
// Provider selection (checked in order):
//   - LLM_PROVIDER=openai  → OpenAI-compatible API (set LLM_BASE_URL, LLM_API_KEY, LLM_MODEL)
//   - LLM_PROVIDER=ollama  → Ollama (set OLLAMA_BASE_URL or defaults to localhost:11434, LLM_MODEL)
//   - LLM_PROVIDER=vertex  → Claude on Vertex AI (existing behavior)
//   - (default)            → Vertex AI if ANTHROPIC_VERTEX_PROJECT_ID is set, else error
func NewProvider(cfg *config.Config) (Provider, error) {
	provider := config.GetEnvOrSecret("LLM_PROVIDER")

	switch provider {
	case "openai":
		baseURL := config.GetEnvOrSecret("LLM_BASE_URL")
		if baseURL == "" {
			return nil, fmt.Errorf("LLM_BASE_URL required when LLM_PROVIDER=openai")
		}
		model := config.GetEnvOrSecret("LLM_MODEL")
		if model == "" {
			return nil, fmt.Errorf("LLM_MODEL required when LLM_PROVIDER=openai")
		}
		slog.Info("using OpenAI-compatible provider", "base_url", baseURL, "model", model)
		return &OpenAIProvider{
			baseURL: baseURL,
			apiKey:  config.GetEnvOrSecret("LLM_API_KEY"),
			model:   model,
			http:    &http.Client{Timeout: 120 * time.Second},
		}, nil

	case "ollama":
		baseURL := config.GetEnvOrSecret("OLLAMA_BASE_URL")
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		model := config.GetEnvOrSecret("LLM_MODEL")
		if model == "" {
			return nil, fmt.Errorf("LLM_MODEL required when LLM_PROVIDER=ollama")
		}
		slog.Info("using Ollama provider", "base_url", baseURL, "model", model)
		return &OpenAIProvider{
			baseURL: baseURL,
			model:   model,
			http:    &http.Client{Timeout: 300 * time.Second},
		}, nil

	case "vertex", "":
		if cfg.VertexProjectID == "" {
			if provider == "vertex" {
				return nil, fmt.Errorf("ANTHROPIC_VERTEX_PROJECT_ID required for vertex provider")
			}
			return nil, fmt.Errorf("no LLM provider configured — set LLM_PROVIDER (openai, ollama, vertex)")
		}
		model := config.GetEnvOrSecret("LLM_MODEL")
		if model == "" {
			model = "claude-sonnet-4-6"
		}
		ctx := context.Background()
		slog.Info("using Vertex AI provider", "project", cfg.VertexProjectID, "region", cfg.VertexRegion, "model", model)
		return &VertexProvider{
			client:    anthropic.NewClient(vertex.WithGoogleAuth(ctx, cfg.VertexRegion, cfg.VertexProjectID)),
			model:     model,
			projectID: cfg.VertexProjectID,
			region:    cfg.VertexRegion,
		}, nil

	default:
		return nil, fmt.Errorf("unknown LLM_PROVIDER %q — use openai, ollama, or vertex", provider)
	}
}

// Complete sends a chat completion request via the Anthropic SDK.
func (p *VertexProvider) Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	slog.Info("vertex completion", "model", p.model, "max_tokens", maxTokens)

	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: int64(maxTokens),
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("vertex api: %w", err)
	}

	slog.Info("vertex response", "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text in vertex response")
}

// openAIRequest is the OpenAI chat completions request format.
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete sends a chat completion request to an OpenAI-compatible API.
func (p *OpenAIProvider) Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	slog.Info("openai completion", "base_url", p.baseURL, "model", p.model, "max_tokens", maxTokens)

	reqBody := openAIRequest{
		Model:       p.model,
		Temperature: 0,
		MaxTokens:   maxTokens,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := p.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai api: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("openai api %s: %s (status %d)", url, truncateStr(string(respBody), 300), resp.StatusCode)
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("openai api error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in openai response")
	}

	slog.Info("openai response", "prompt_tokens", result.Usage.PromptTokens, "completion_tokens", result.Usage.CompletionTokens)
	return result.Choices[0].Message.Content, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// cleanJSON strips markdown code fences from LLM responses that wrap JSON
// in ```json ... ``` despite being told not to.
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
