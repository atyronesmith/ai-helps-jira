package mcpserver

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// DependencyObservation describes a completed operation, including an explicit
// dashboard connection test. Polling never creates an upstream probe. A
// successful observation is not a guarantee about the next request.
type DependencyObservation struct {
	Name      string    `json:"name"`
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checked_at"`
	Required  bool      `json:"required"`
}

type attemptTraceKey struct{}

// Per-call classifications preserve typed failures when an MCP handler returns
// a tool-error result. The value is owned by one synchronous dispatch and never
// retains upstream error text in history.
type attemptFailureKey struct{}
type attemptFailure struct{ category string }

func attemptTrace(ctx context.Context) *QueryResultData {
	trace, _ := ctx.Value(attemptTraceKey{}).(*QueryResultData)
	return trace
}

// observeTranslation retains diagnostics only when a registered natural-language
// tool has a lifecycle trace. It never adds a translation or validation request.
func (h *Handlers) observeTranslation(ctx context.Context, cfg *config.Config, result *llm.QueryResult, err error) {
	if result != nil && (result.ProviderCalls > 0 || (result.Route == llm.RouteFallback && err != nil)) {
		h.dependencies.observe("provider", err)
	}
	trace := attemptTrace(ctx)
	if trace == nil {
		return
	}
	trace.ErrorStage = "translation"
	if result != nil {
		trace.NormalizedInput, trace.Route, trace.GeneratedJQL = result.NormalizedInput, result.Route, result.JQL
		trace.DisplayJQL = jql.ResolveRelativeDatesForDisplay(result.JQL, trace.StartedAt)
		trace.JQL = trace.DisplayJQL
	}
	if err != nil {
		trace.ErrorCategory = diagnosticErrorCategory(err)
		return
	}
	trace.ErrorStage = ""
	if result != nil {
		trace.Validation = "local_valid"
		if jql.Validate(result.JQL, jql.Options{DefaultProject: cfg.JiraProject}) != nil {
			trace.Validation = "local_invalid"
		}
	}
}

type dependencyObservations struct {
	mu     sync.RWMutex
	values map[string]DependencyObservation
}

func newDependencyObservations() *dependencyObservations {
	d := &dependencyObservations{values: make(map[string]DependencyObservation)}
	for _, name := range []string{"mcp", "history", "cache", "jira", "provider"} {
		d.values[name] = DependencyObservation{Name: name, State: "unknown", Required: true}
	}
	return d
}

func (d *dependencyObservations) observe(name string, err error) {
	if d == nil {
		return
	}
	state := "ok"
	if err != nil {
		state = "failed"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.values[name] = DependencyObservation{Name: name, State: state, CheckedAt: time.Now().UTC(), Required: true}
}

const observationMaxAge = 5 * time.Minute

func (d *dependencyObservations) snapshot(now time.Time) ([]DependencyObservation, string) {
	if d == nil {
		d = newDependencyObservations()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	observations := make([]DependencyObservation, 0, len(d.values))
	for _, name := range []string{"mcp", "history", "cache", "jira", "provider"} {
		obs := d.values[name]
		if !obs.CheckedAt.IsZero() && now.Sub(obs.CheckedAt) > observationMaxAge {
			obs.State = "stale"
		}
		observations = append(observations, obs)
	}
	return observations, observedReadiness(observations)
}

func observedReadiness(observations []DependencyObservation) string {
	readiness := "ready"
	for _, obs := range observations {
		if obs.Required && obs.State == "failed" {
			return "not_ready"
		}
		if obs.Required && obs.State != "ok" {
			readiness = "unknown"
		}
	}
	return readiness
}

// Only bounded categories go into durable diagnostics; upstream error messages
// and tool arguments can contain credentials, URLs, or response bodies.
func diagnosticErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	if kind := translationerror.KindOf(err); kind != "" {
		return string(kind)
	}
	var status *jira.HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode == 401 || status.StatusCode == 403 {
			return "authentication"
		}
		return "upstream_http"
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout"
		}
		return "connectivity"
	}
	return "unknown"
}

// withDiagnostics records call outcomes separately from any rich report produced
// by a tool. Query owns its detailed lifecycle. Translation-only attempts keep
// translation evidence without becoming query results.
func (h *Handlers) withDiagnostics(name string, next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now().UTC()
		failure := &attemptFailure{}
		ctx = context.WithValue(ctx, attemptFailureKey{}, failure)
		var trace *QueryResultData
		if name == "jira_digest" || name == "jira_translate_query" {
			input := getString(req, "natural_query")
			if name == "jira_digest" {
				input = getString(req, "issues")
			}
			trace = &QueryResultData{Query: input, StartedAt: started, Validation: "not_checked"}
			ctx = context.WithValue(ctx, attemptTraceKey{}, trace)
		}
		// Reaching a registered tool establishes MCP dispatch, even if its operation fails.
		h.dependencies.observe("mcp", nil)
		result, err := next(ctx, req)
		failed := err != nil || (result != nil && result.IsError)
		if trace != nil && trace.ErrorCategory != "" {
			failed = true
		}
		if name != "jira_query" && h.store != nil {
			outcome, category := "success", ""
			if failed {
				outcome, category = "failure", "tool_error"
				if err != nil {
					category = diagnosticErrorCategory(err)
				}
				if failure.category != "" {
					category = failure.category
				}
			}
			completed := time.Now().UTC()
			if trace != nil {
				trace.Outcome, trace.CompletedAt, trace.Duration = outcome, completed, completed.Sub(started)
				if trace.ErrorCategory != "" {
					category = trace.ErrorCategory
				}
			}
			h.store.Save(ResultAttempt, name, &AttemptResultData{Tool: name, Outcome: outcome, ErrorCategory: category, StartedAt: started, CompletedAt: completed, Duration: completed.Sub(started), Trace: trace})
		}
		if h.store != nil {
			h.dependencies.observe("history", h.store.PersistenceError())
		}
		return result, err
	}
}
