package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

func TestCleanJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "json code fence",
			input: "```json\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "plain code fence",
			input: "```\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "with leading whitespace",
			input: "  ```json\n{\"key\": \"value\"}\n```  ",
			want:  `{"key": "value"}`,
		},
		{
			name:  "multiline JSON in fence",
			input: "```json\n{\n  \"key\": \"value\",\n  \"arr\": [1, 2]\n}\n```",
			want:  "{\n  \"key\": \"value\",\n  \"arr\": [1, 2]\n}",
		},
		{
			name:  "no fence just whitespace",
			input: "  {\"key\": \"value\"}  ",
			want:  `{"key": "value"}`,
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "fence with language tag",
			input: "```JSON\n{\"a\": 1}\n```",
			want:  `{"a": 1}`,
		},
		{
			name:  "nested backticks in content",
			input: "```json\n{\"code\": \"use `x`\"}\n```",
			want:  "{\"code\": \"use `x`\"}",
		},
		{
			name:  "prose preamble before fenced json",
			input: "To show all open issues, use this JQL:\n\n```json\n{\"jql\": \"project = FIELDCTO\"}\n```",
			want:  `{"jql": "project = FIELDCTO"}`,
		},
		{
			name:  "prose preamble and trailing text, no fence",
			input: `Here is your answer: {"jql": "project = FIELDCTO"} Hope that helps!`,
			want:  `{"jql": "project = FIELDCTO"}`,
		},
		{
			name:  "brace in string value ignored for depth",
			input: `Sure! {"reason": "uses { and } in text"} done`,
			want:  `{"reason": "uses { and } in text"}`,
		},
		{
			name:  "no JSON present",
			input: "I cannot help with that.",
			want:  "I cannot help with that.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanJSON(tc.input)
			if got != tc.want {
				t.Errorf("cleanJSON(%q)\n  got:  %q\n  want: %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestOpenAIProvider_RequestsJSONMode(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		jsonResp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"jql": "project = X"}`}},
			},
		}
		enc, _ := json.Marshal(jsonResp)
		w.Write(enc)
	}))
	defer ts.Close()

	p := &OpenAIProvider{baseURL: ts.URL, model: "test-model", http: ts.Client()}
	if _, err := p.Complete(context.Background(), "sys", "prompt", 100); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("expected response_format in request body, got %v", gotBody)
	}
	if rf["type"] != "json_object" {
		t.Errorf("expected response_format.type=json_object, got %v", rf["type"])
	}
}

func TestOpenAIProvider_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer ts.Close()

	p := &OpenAIProvider{baseURL: ts.URL, model: "test-model", http: ts.Client()}
	_, err := p.Complete(context.Background(), "sys", "prompt", 100)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("error should mention authentication failure, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Errorf("error should mention LLM_API_KEY, got: %s", err.Error())
	}
}

func TestOpenAIProvider_Unreachable(t *testing.T) {
	p := &OpenAIProvider{baseURL: "http://127.0.0.1:1", model: "test-model", http: &http.Client{}}
	_, err := p.Complete(context.Background(), "sys", "prompt", 100)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error should mention unreachable, got: %s", err.Error())
	}
}

func TestNewProviderContextRejectsPreCanceledContext(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "openai")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewProviderContext(ctx, nil)
	if translationerror.KindOf(err) != translationerror.Cancellation {
		t.Fatalf("kind = %q, err %v", translationerror.KindOf(err), err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error does not preserve context cause: %v", err)
	}
}

func TestOpenAIProviderDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer target.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer start.Close()
	p := &OpenAIProvider{baseURL: start.URL, model: "test", http: providerHTTPClient(time.Second)}
	_, _ = p.Complete(context.Background(), "sys", "prompt", 10)
	if got := redirected.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func TestOpenAIProviderBoundsResponseBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", (1<<20)+10))
	}))
	defer ts.Close()
	p := &OpenAIProvider{baseURL: ts.URL, model: "test", http: ts.Client()}
	_, err := p.Complete(context.Background(), "sys", "prompt", 10)
	if translationerror.KindOf(err) != translationerror.InvalidOutput {
		t.Fatalf("kind = %q, err %v", translationerror.KindOf(err), err)
	}
}

func TestOpenAIProvider_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := &OpenAIProvider{baseURL: ts.URL, model: "test-model", http: ts.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := p.Complete(ctx, "sys", "prompt", 100)
	if err == nil {
		t.Fatal("expected error for timed out request")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got: %s", err.Error())
	}
}

func TestNormalizeChatURL(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://api.example", "https://api.example/v1/chat/completions"},
		{"https://api.example/", "https://api.example/v1/chat/completions"},
		{"https://api.example/v1", "https://api.example/v1/chat/completions"},
		{"https://api.example/v1/", "https://api.example/v1/chat/completions"},
		{"https://api.example/proxy/v1", "https://api.example/proxy/v1/chat/completions"},
	} {
		got, err := normalizeChatURL(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("normalizeChatURL(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"ftp://api.example", "https://u:p@api.example", "https://api.example?x=y", "https://api.example?", "https://api.example#fragment", "https://api.example#", "/relative"} {
		if _, err := normalizeChatURL(input); err == nil {
			t.Errorf("normalizeChatURL(%q) accepted invalid URL", input)
		}
	}
}

func TestOpenAIProvider_OneRequestAndNoResponseSecrets(t *testing.T) {
	requests := 0
	const secret = "private-upstream-detail"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, secret)
	}))
	defer ts.Close()
	p := &OpenAIProvider{baseURL: ts.URL, model: "test", http: ts.Client()}
	_, err := p.Complete(context.Background(), "sys", "prompt", 10)
	if err == nil {
		t.Fatal("expected error")
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1", requests)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked response body: %v", err)
	}
	if got := translationerror.KindOf(err); got != translationerror.Connectivity {
		t.Errorf("kind = %q", got)
	}
}

func TestOpenAIProvider_ClassifiesAuthAndCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "sensitive response")
	}))
	defer ts.Close()
	p := &OpenAIProvider{baseURL: ts.URL, model: "test", http: ts.Client()}
	_, err := p.Complete(context.Background(), "sys", "prompt", 10)
	if got := translationerror.KindOf(err); got != translationerror.Authentication {
		t.Errorf("auth kind = %q, err %v", got, err)
	}
	if strings.Contains(err.Error(), "sensitive response") {
		t.Fatal("authentication error leaked response body")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Complete(ctx, "sys", "prompt", 10)
	if got := translationerror.KindOf(err); got != translationerror.Cancellation {
		t.Errorf("cancel kind = %q, err %v", got, err)
	}
}
