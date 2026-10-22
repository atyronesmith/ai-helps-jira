package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestTranslationErrorsStopWithoutJiraSearch(t *testing.T) {
	for _, tool := range []string{"query", "translate", "digest"} {
		for _, failure := range []struct {
			name                string
			err                 error
			askForClarification bool
		}{
			{name: "ambiguity", err: fmt.Errorf("%w: activity phrase unclear", nl2jql.ErrAmbiguousIntent), askForClarification: true},
			{name: "invalid_output", err: errors.New("provider output was invalid")},
		} {
			t.Run(tool+"/"+failure.name, func(t *testing.T) {
				h := newTestHandlers(t)
				setTestJIRAEnv(t)
				const original = "worked on my open issues last week"
				var translationCalls atomic.Int32
				translate := func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
					translationCalls.Add(1)
					return nil, failure.err
				}
				h.translate = translate
				h.digestTranslate = translate

				var message string
				var result *mcp.CallToolResult
				var callErr error
				switch tool {
				case "query":
					result, callErr = h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
				case "translate":
					result, callErr = h.HandleTranslateQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
				case "digest":
					result, callErr = h.HandleDigest(context.Background(), makeRequest(map[string]any{"issues": original}))
				}

				if callErr != nil {
					t.Fatalf("handler returned Go error: %v", callErr)
				}
				if got := translationCalls.Load(); got != 1 {
					t.Fatalf("translator called %d times, want exactly once", got)
				}
				if result == nil {
					t.Fatal("handler returned nil result")
				}
				assertIsError(t, result)
				message = resultText(result)
				if message == "" {
					t.Fatal("handler returned no error text")
				}
				for _, phrase := range []string{
					"JQL generation failed: " + failure.err.Error(),
					"No Jira issue search was executed",
					"Stop and report this error",
					"Do not silently rephrase or retry variants",
					"add or drop constraints, or change dates",
					"Do not seek credentials or inspect local authentication config",
					"do not bypass MCP via shell, curl, CLI, or direct REST",
				} {
					if !strings.Contains(message, phrase) {
						t.Errorf("%s error missing %q: %s", tool, phrase, message)
					}
				}
				if got := strings.Contains(message, "Ask the user for clarification"); got != failure.askForClarification {
					t.Errorf("clarification guidance present=%v, want %v: %s", got, failure.askForClarification, message)
				}
			})
		}
	}
}
