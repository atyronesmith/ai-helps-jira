package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// ErrIncompleteCompletion identifies output that was cut off at the provider's
// configured output limit. It is safe to inspect with errors.Is.
var ErrIncompleteCompletion = errors.New("LLM completion reached its output limit")

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
	return NewProviderContext(context.Background(), cfg)
}

// NewProviderContext constructs a provider using ctx for initialization that may
// contact an identity service, such as Vertex Application Default Credentials.
func NewProviderContext(ctx context.Context, cfg *config.Config) (Provider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		kind := translationerror.Cancellation
		if errors.Is(err, context.DeadlineExceeded) {
			kind = translationerror.Timeout
		}
		return nil, translationerror.New(kind, err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	provider := config.GetEnvOrSecret("LLM_PROVIDER")

	switch provider {
	case "openai":
		baseURL := config.GetEnvOrSecret("LLM_BASE_URL")
		if baseURL == "" {
			return nil, translationerror.New(translationerror.Unsupported, errors.New("LLM_BASE_URL required when LLM_PROVIDER=openai"))
		}
		var err error
		baseURL, err = normalizeChatURL(baseURL)
		if err != nil {
			return nil, translationerror.New(translationerror.Unsupported, err)
		}
		model := config.GetEnvOrSecret("LLM_MODEL")
		if model == "" {
			return nil, translationerror.New(translationerror.Unsupported, errors.New("LLM_MODEL required when LLM_PROVIDER=openai"))
		}
		slog.Info("using OpenAI-compatible provider", "base_url", baseURL, "model", model)
		return &OpenAIProvider{
			baseURL: baseURL,
			apiKey:  config.GetEnvOrSecret("LLM_API_KEY"),
			model:   model,
			http:    providerHTTPClient(120 * time.Second),
		}, nil

	case "ollama":
		baseURL := config.GetEnvOrSecret("OLLAMA_BASE_URL")
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		var err error
		baseURL, err = normalizeChatURL(baseURL)
		if err != nil {
			return nil, translationerror.New(translationerror.Unsupported, err)
		}
		model := config.GetEnvOrSecret("LLM_MODEL")
		if model == "" {
			return nil, translationerror.New(translationerror.Unsupported, errors.New("LLM_MODEL required when LLM_PROVIDER=ollama"))
		}
		slog.Info("using Ollama provider", "base_url", baseURL, "model", model)
		return &OpenAIProvider{
			baseURL: baseURL,
			model:   model,
			http:    providerHTTPClient(300 * time.Second),
		}, nil

	case "vertex", "":
		if cfg.VertexProjectID == "" {
			if provider == "vertex" {
				return nil, translationerror.New(translationerror.Authentication, errors.New("ANTHROPIC_VERTEX_PROJECT_ID required for Vertex authentication"))
			}
			return nil, translationerror.New(translationerror.Unsupported, errors.New("no LLM provider configured"))
		}
		model := config.GetEnvOrSecret("LLM_MODEL")
		if model == "" {
			model = "claude-sonnet-4-6"
		}
		slog.Info("using Vertex AI provider", "project", cfg.VertexProjectID, "region", cfg.VertexRegion, "model", model)
		return &VertexProvider{
			client:    anthropic.NewClient(vertex.WithGoogleAuth(ctx, cfg.VertexRegion, cfg.VertexProjectID), option.WithMaxRetries(0), option.WithHTTPClient(providerHTTPClient(120*time.Second))),
			model:     model,
			projectID: cfg.VertexProjectID,
			region:    cfg.VertexRegion,
		}, nil

	default:
		return nil, translationerror.New(translationerror.Unsupported, fmt.Errorf("unknown LLM_PROVIDER %q", provider))
	}
}

func providerHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func normalizeChatURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return "", fmt.Errorf("invalid LLM base URL: expected an HTTP(S) URL without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/v1") {
		u.Path = strings.TrimSuffix(u.Path, "/v1")
	}
	u.Path += "/v1/chat/completions"
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// Complete sends a chat completion request via the Anthropic SDK.
func (p *VertexProvider) Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	slog.Info("vertex completion", "model", p.model, "max_tokens", maxTokens)

	// Every caller of Complete parses the result as JSON, so prefill the
	// assistant turn with "{" -- this forces Claude to continue directly
	// into a JSON object instead of adding a prose preamble. The prefill
	// text isn't echoed back in resp.Content, so it's re-added below.
	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: int64(maxTokens),
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
			anthropic.NewAssistantMessage(anthropic.NewTextBlock("{")),
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", translationerror.New(translationerror.Cancellation, fmt.Errorf("LLM request canceled: %w", context.Canceled))
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", translationerror.New(translationerror.Timeout, fmt.Errorf("LLM request timed out: %w", context.DeadlineExceeded))
		}
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
			return "", translationerror.New(translationerror.Authentication, errors.New("Vertex authentication failed"))
		}
		return "", translationerror.New(translationerror.Connectivity, errors.New("Vertex LLM provider unreachable"))
	}

	if resp.StopReason == anthropic.StopReasonMaxTokens {
		return "", translationerror.New(translationerror.InvalidOutput, ErrIncompleteCompletion)
	}
	if resp.StopReason != "" && resp.StopReason != anthropic.StopReasonEndTurn && resp.StopReason != anthropic.StopReasonStopSequence {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("Vertex completion did not finish normally"))
	}

	slog.Info("vertex response", "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)

	for _, block := range resp.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return "{" + block.Text, nil
		}
	}
	return "", translationerror.New(translationerror.InvalidOutput, errors.New("no usable text in Vertex response"))
}

// openAIRequest is the OpenAI chat completions request format.
type openAIRequest struct {
	Model          string             `json:"model"`
	Messages       []openAIMessage    `json:"messages"`
	Temperature    float64            `json:"temperature"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	ResponseFormat *openAIResponseFmt `json:"response_format,omitempty"`
}

// openAIResponseFmt requests strict JSON output. Supported by OpenAI, Ollama,
// and most OpenAI-compatible backends; every caller of Complete parses JSON,
// so this is always set as a best-effort guard against chatty small models.
type openAIResponseFmt struct {
	Type string `json:"type"`
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
		FinishReason string `json:"finish_reason"`
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
		Model:          p.model,
		Temperature:    0,
		MaxTokens:      maxTokens,
		ResponseFormat: &openAIResponseFmt{Type: "json_object"},
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := p.baseURL
	if !strings.HasSuffix(url, "/chat/completions") {
		url, err = normalizeChatURL(url)
		if err != nil {
			return "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", translationerror.New(translationerror.InvalidOutput, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", translationerror.New(translationerror.Cancellation, fmt.Errorf("LLM request canceled: %w", context.Canceled))
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", translationerror.New(translationerror.Timeout, fmt.Errorf("LLM request timed out: %w", context.DeadlineExceeded))
		}
		return "", translationerror.New(translationerror.Connectivity, errors.New("LLM provider unreachable"))
	}
	defer resp.Body.Close()

	const maxProviderResponseBytes = 1 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return "", translationerror.New(translationerror.Cancellation, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", translationerror.New(translationerror.Timeout, context.DeadlineExceeded)
		}
		return "", translationerror.New(translationerror.Connectivity, errors.New("could not read LLM response"))
	}
	if len(respBody) > maxProviderResponseBytes {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("LLM response exceeds size limit"))
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", translationerror.New(translationerror.Authentication, fmt.Errorf("LLM authentication failed (status %d); check LLM_API_KEY", resp.StatusCode))
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return "", translationerror.New(translationerror.InvalidOutput, fmt.Errorf("LLM API rejected the request (status %d)", resp.StatusCode))
		}
		return "", translationerror.New(translationerror.Connectivity, fmt.Errorf("LLM API returned status %d", resp.StatusCode))
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("could not decode LLM response"))
	}

	if result.Error != nil {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("LLM returned an error response"))
	}

	if len(result.Choices) == 0 {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("no choices in LLM response"))
	}
	choice := result.Choices[0]
	if reason := strings.TrimSpace(choice.FinishReason); reason == "length" {
		return "", translationerror.New(translationerror.InvalidOutput, ErrIncompleteCompletion)
	} else if reason != "" && reason != "stop" {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("LLM completion did not finish normally"))
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", translationerror.New(translationerror.InvalidOutput, errors.New("LLM returned empty content"))
	}

	slog.Info("openai response", "prompt_tokens", result.Usage.PromptTokens, "completion_tokens", result.Usage.CompletionTokens)
	return choice.Message.Content, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// cleanJSON extracts a JSON object from an LLM response, tolerating markdown
// code fences and chatty prose before/after the JSON that smaller models add
// despite being told to respond with JSON only.
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)

	if fenced, ok := extractFencedBlock(s); ok {
		s = fenced
	}

	if strings.HasPrefix(s, "{") {
		return s
	}

	if obj := extractJSONObject(s); obj != "" {
		return obj
	}
	return s
}

// extractFencedBlock returns the contents of the first ``` ... ``` block
// found anywhere in s, skipping an optional language tag on the opening line.
func extractFencedBlock(s string) (string, bool) {
	start := strings.Index(s, "```")
	if start == -1 {
		return "", false
	}
	rest := s[start+3:]
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		tag := strings.TrimSpace(rest[:nl])
		if !strings.ContainsAny(tag, "{}\"") {
			rest = rest[nl+1:]
		}
	}
	end := strings.Index(rest, "```")
	if end == -1 {
		return "", false
	}
	return strings.TrimSpace(rest[:end]), true
}

// extractJSONObject finds the first balanced {...} object in s, ignoring any
// surrounding prose. It tracks string literals so braces inside string values
// don't throw off the depth count.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start == -1 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
