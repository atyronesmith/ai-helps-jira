package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

func TestOpenAIProviderCompletionFinishReason(t *testing.T) {
	tests := []struct {
		name, reason, content string
		includeReason         bool
		wantErr               bool
		wantIncomplete        bool
	}{
		{name: "normal stop", reason: "stop", content: `{"ok":true}`, includeReason: true},
		{name: "legacy missing reason", content: `{"ok":true}`},
		{name: "length despite content", reason: "length", content: `{"ok":true}`, includeReason: true, wantErr: true, wantIncomplete: true},
		{name: "content filter", reason: "content_filter", content: "private response text", includeReason: true, wantErr: true},
		{name: "tool calls", reason: "tool_calls", content: `{"ok":true}`, includeReason: true, wantErr: true},
		{name: "function call", reason: "function_call", content: `{"ok":true}`, includeReason: true, wantErr: true},
		{name: "unknown", reason: "private-future-reason", content: `{"ok":true}`, includeReason: true, wantErr: true},
		{name: "empty content", reason: "stop", content: " \n\t", includeReason: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				choice := map[string]any{"message": map[string]string{"content": tc.content}}
				if tc.includeReason {
					choice["finish_reason"] = tc.reason
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{choice}})
			}))
			defer ts.Close()

			p := &OpenAIProvider{baseURL: ts.URL, model: "test", http: ts.Client()}
			got, err := p.Complete(context.Background(), "sys", "prompt", 10)
			if tc.wantErr {
				if err == nil || translationerror.KindOf(err) != translationerror.InvalidOutput {
					t.Fatalf("error = %v, want invalid_output", err)
				}
				if got != "" {
					t.Fatalf("content = %q, want empty", got)
				}
				if strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), tc.content) {
					t.Fatalf("error leaked response details: %v", err)
				}
				if errors.Is(err, ErrIncompleteCompletion) != tc.wantIncomplete {
					t.Fatalf("errors.Is(ErrIncompleteCompletion) = %t, want %t", errors.Is(err, ErrIncompleteCompletion), tc.wantIncomplete)
				}
			} else if err != nil || got != tc.content {
				t.Fatalf("Complete = (%q, %v), want (%q, nil)", got, err, tc.content)
			}
			if calls != 1 {
				t.Fatalf("request calls = %d, want 1", calls)
			}
		})
	}
}

func TestVertexProviderCompletionStopReason(t *testing.T) {
	tests := []struct {
		name, reason, content string
		want                  string
		wantErr               bool
		wantIncomplete        bool
	}{
		{name: "end turn", reason: "end_turn", content: `"ok":true}`, want: `{"ok":true}`},
		{name: "stop sequence", reason: "stop_sequence", content: `"ok":true}`, want: `{"ok":true}`},
		{name: "max tokens", reason: "max_tokens", content: `"ok":true}`, wantErr: true, wantIncomplete: true},
		{name: "refusal", reason: "refusal", content: "private response text", wantErr: true},
		{name: "unknown", reason: "private-future-reason", content: `"ok":true}`, wantErr: true},
		{name: "empty text", reason: "end_turn", content: " \n\t", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			const responseSecret = "private response text"
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "msg_test", "type": "message", "role": "assistant", "model": "test", "container": nil,
					"content":       []any{map[string]string{"type": "text", "text": tc.content}},
					"stop_reason":   tc.reason,
					"stop_sequence": nil,
					"usage": map[string]any{
						"cache_creation": map[string]int{"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": 0}, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
						"inference_geo": "", "input_tokens": 1, "output_tokens": 1,
						"server_tool_use": map[string]int{"web_search_requests": 0, "web_fetch_requests": 0}, "service_tier": "standard",
					},
				})
			}))
			defer ts.Close()

			client := anthropic.NewClient(
				option.WithBaseURL(ts.URL),
				option.WithAPIKey("synthetic-test-key"),
				option.WithHTTPClient(ts.Client()),
				option.WithMaxRetries(0),
			)
			p := &VertexProvider{client: client, model: "test"}
			got, err := p.Complete(context.Background(), "system", "prompt", 10)
			if tc.wantErr {
				if err == nil || translationerror.KindOf(err) != translationerror.InvalidOutput {
					t.Fatalf("error = %v, want invalid_output", err)
				}
				if got != "" {
					t.Fatalf("content = %q, want empty", got)
				}
				if strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), responseSecret) {
					t.Fatalf("error leaked response details: %v", err)
				}
				if errors.Is(err, ErrIncompleteCompletion) != tc.wantIncomplete {
					t.Fatalf("errors.Is(ErrIncompleteCompletion) = %t, want %t", errors.Is(err, ErrIncompleteCompletion), tc.wantIncomplete)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("Complete = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
			if calls != 1 {
				t.Fatalf("request calls = %d, want 1", calls)
			}
		})
	}
}
