package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestTranslateQueryAttemptsAreVisibleWithoutSearch(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			h := newTestHandlers(t)
			storeDir := t.TempDir()
			h.store = openTestStore(t, storeDir, "translation-test", 0, 0)
			setTestJIRAEnv(t)
			var jiraCalls atomic.Int32
			jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jiraCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer jiraServer.Close()
			t.Setenv("JIRA_SERVER", jiraServer.URL)

			const original = "  List MY open bugs  "
			const normalized = "list MY open bugs"
			const generated = "project = TEST AND assignee = currentUser() AND statusCategory != Done"
			h.translate = func(_ context.Context, _ *config.Config, input string) (*llm.QueryResult, error) {
				if input != original {
					t.Fatalf("input=%q, want exact original %q", input, original)
				}
				result := &llm.QueryResult{Input: original, NormalizedInput: normalized, JQL: generated, Route: llm.RouteFastPath}
				if fail {
					return result, translationerror.New(translationerror.Timeout, errors.New("private upstream diagnostic marker"))
				}
				return result, nil
			}

			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			result, err := h.withDiagnostics("jira_translate_query", h.HandleTranslateQuery)(context.Background(), makeRequest(map[string]any{"natural_query": original}))
			if err != nil || result == nil || result.IsError != fail {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if jiraCalls.Load() != 0 {
				t.Fatalf("translation made %d Jira requests", jiraCalls.Load())
			}
			history := h.store.List()
			if len(history) != 1 || history[0].Type != ResultAttempt {
				t.Fatalf("history=%+v", history)
			}
			attempt := history[0].Data.(*AttemptResultData)
			trace := attempt.Trace
			wantOutcome, wantCategory := "success", ""
			if fail {
				wantOutcome, wantCategory = "failure", "timeout"
			}
			if attempt.Outcome != wantOutcome || attempt.ErrorCategory != wantCategory || trace == nil {
				t.Fatalf("attempt=%+v", attempt)
			}
			wantValidation := "local_valid"
			if fail {
				wantValidation = "not_checked"
			}
			if trace.Query != original || trace.NormalizedInput != normalized || trace.GeneratedJQL != generated || trace.DisplayJQL != generated || trace.ExecutedJQL != "" || trace.Validation != wantValidation || trace.Duration <= 0 {
				t.Fatalf("trace=%+v", trace)
			}
			response := ""
			if len(result.Content) > 0 {
				if content, ok := result.Content[0].(mcp.TextContent); ok {
					response = content.Text
				}
			}
			if !fail && !strings.Contains(response, `"scope": "local_structure"`) {
				t.Fatalf("translation response lacks structural scope: %s", response)
			}
			if strings.Contains(logs.String(), original) || strings.Contains(logs.String(), "private upstream diagnostic marker") {
				t.Fatalf("logs contain private input or upstream detail: %s", logs.String())
			}
			if !strings.Contains(logs.String(), "translation started") || !strings.Contains(logs.String(), "translation completed") || !strings.Contains(logs.String(), "route=fast_path") {
				t.Fatalf("incomplete bounded lifecycle logs: %s", logs.String())
			}
			if err := h.store.Close(); err != nil {
				t.Fatalf("close attempt store: %v", err)
			}
			reopened := openTestStore(t, storeDir, "translation-test", 0, 0)
			persisted, ok := reopened.Get(history[0].ID)
			if !ok || persisted.Type != ResultAttempt || persisted.Data.(*AttemptResultData).Trace == nil || persisted.Data.(*AttemptResultData).Trace.Query != original {
				t.Fatalf("translation attempt did not survive restart: %+v", persisted)
			}
		})
	}
}
