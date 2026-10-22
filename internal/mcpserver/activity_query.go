package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
	"github.com/mark3labs/mcp-go/mcp"
)

func (h *Handlers) handleActivityQuery(
	ctx context.Context,
	req mcp.CallToolRequest,
	parsed nl2jql.ActivityRequest,
	parseErr error,
	maxRows int,
	attempt *QueryResultData,
	finish func() string,
) *mcp.CallToolResult {
	attempt.Route = "activity"
	attempt.ErrorStage, attempt.ErrorCategory = "translation", "unknown"
	attempt.Validation = "not_applicable"
	if parseErr != nil {
		attempt.ErrorCategory = diagnosticErrorCategory(parseErr)
		return translationErrorResult(parseErr)
	}
	projectArg := strings.TrimSpace(getString(req, "project"))
	if parsed.Project != "" && projectArg != "" && !strings.EqualFold(parsed.Project, projectArg) {
		err := fmt.Errorf("%w: activity wording names project %s but the project argument selects %s", nl2jql.ErrAmbiguousIntent, parsed.Project, projectArg)
		attempt.ErrorCategory = diagnosticErrorCategory(err)
		return translationErrorResult(err)
	}
	project := projectArg
	if project == "" {
		project = parsed.Project
	}
	cfg, err := config.LoadJIRAOnly(getString(req, "user"), project)
	if err != nil {
		attempt.ErrorStage, attempt.ErrorCategory = "configuration", "configuration"
		return errorResult(err.Error())
	}
	attempt.User, attempt.JiraServer = cfg.Assignee(), cfg.JiraServer
	attempt.ErrorStage, attempt.ErrorCategory = "client", "configuration"
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	attempt.ErrorStage, attempt.ErrorCategory = "activity", "unknown"
	result, activityErr := h.activityLookup(ctx, cfg, jira.WorkActivityOptions{
		StartDate:     parsed.StartDate,
		EndDate:       parsed.EndDate,
		Project:       cfg.JiraProject,
		MaxIssues:     1000,
		MaxEventPages: 20,
	})
	attempt.Activity = &result
	attempt.ActivityYearInferred = parsed.YearInferred
	attempt.ActivityDisplayLimit = maxRows
	h.dependencies.observe("jira", activityErr)
	if result.CandidateJQL != "" {
		attempt.GeneratedJQL = result.CandidateJQL
		if result.CandidateExecuted {
			attempt.ExecutedJQL = result.CandidateJQL
		}
	}
	complete := result.CandidateComplete && result.Complete
	if complete && activityErr == nil {
		attempt.Outcome, attempt.ErrorStage, attempt.ErrorCategory = "success", "", ""
	} else if activityErr == nil || result.CandidateExecuted || result.Candidates > 0 || result.Scanned > 0 || len(result.Matches) > 0 {
		attempt.Outcome = "partial"
	} else {
		attempt.Outcome = "failure"
	}
	if activityErr != nil {
		attempt.ErrorStage, attempt.ErrorCategory = "activity", diagnosticErrorCategory(activityErr)
		attempt.ActivityError = "activity lookup failed (" + attempt.ErrorCategory + ")"
	} else if !complete {
		attempt.ErrorStage, attempt.ErrorCategory = "activity", "incomplete"
		attempt.ActivityError = "activity lookup incomplete"
	}

	var text strings.Builder
	fmt.Fprintf(&text, "## Activity request: %s\n\n", parsed.Input)
	if parsed.YearInferred {
		fmt.Fprintf(&text, "Years omitted; both dates were interpreted in %s.\n\n", parsed.StartDate[:4])
	}
	text.WriteString(format.RenderActivity(result, maxRows))
	resultID := finish()
	fmt.Fprintf(&text, "\n---\nRich dashboard: %s\n", h.viewURL(resultID))
	if activityErr != nil {
		return errorResult(fmt.Sprintf("Jira activity lookup failed: %v\n\n%s", activityErr, text.String()))
	}
	if !complete {
		return errorResult("Jira activity lookup did not complete.\n\n" + text.String())
	}
	return textResult(text.String())
}
