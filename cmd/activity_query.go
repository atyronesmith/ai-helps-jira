package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
	"github.com/spf13/cobra"
)

var queryActivity = func(ctx context.Context, cfg *config.Config, opts jira.WorkActivityOptions) (jira.ActivityResult, error) {
	client, err := jira.NewClient(cfg)
	if err != nil {
		return jira.ActivityResult{}, err
	}
	return client.WithContext(ctx).WorkActivity(ctx, opts)
}

func runActivityQuery(cmdContext *cobra.Command, request nl2jql.ActivityRequest, parseErr error) error {
	if parseErr != nil {
		return fmt.Errorf("activity lookup: %w", parseErr)
	}
	if request.Project != "" && flagProject != "" && !strings.EqualFold(request.Project, flagProject) {
		return fmt.Errorf("%w: activity request names project %s, but --project selects %s", nl2jql.ErrAmbiguousIntent, request.Project, flagProject)
	}
	project := flagProject
	if request.Project != "" {
		project = request.Project
	}
	loadConfig := config.LoadJIRAOnly
	if flagDryRun {
		loadConfig = config.LoadTranslation
	}
	cfg, err := loadConfig(flagUser, project)
	if err != nil {
		return err
	}
	if project == "" {
		project = cfg.JiraProject
	}
	if flagDryRun {
		return printActivityPlan(cmdContext.OutOrStdout(), request, project)
	}

	ctx, cancel := context.WithTimeout(commandContext(cmdContext), 120*time.Second)
	defer cancel()
	result, activityErr := queryActivity(ctx, cfg, jira.WorkActivityOptions{
		StartDate: request.StartDate, EndDate: request.EndDate, Project: project,
		MaxIssues: 1000, MaxEventPages: 20,
	})
	if request.YearInferred {
		fmt.Fprintf(cmdContext.OutOrStdout(), "Years omitted; both dates were interpreted in year %s.\n", request.StartDate[:4])
	}
	fmt.Fprintf(cmdContext.OutOrStdout(), "Original request: %s\n\n", request.Input)
	fmt.Fprint(cmdContext.OutOrStdout(), format.RenderActivity(result, flagMaxResults))
	if activityErr != nil {
		return fmt.Errorf("jira activity lookup: %w", activityErr)
	}
	if !result.CandidateComplete || !result.Complete {
		return fmt.Errorf("jira activity lookup incomplete: %w", jira.ErrIncompleteSearch)
	}
	return nil
}

func printActivityPlan(w io.Writer, request nl2jql.ActivityRequest, project string) error {
	if request.YearInferred {
		fmt.Fprintf(w, "Years omitted; both dates were interpreted in year %s.\n", request.StartDate[:4])
	}
	fmt.Fprintf(w, "Activity lookup plan (dry run; no Jira request was made)\n")
	fmt.Fprintf(w, "Original request: %s\n", request.Input)
	fmt.Fprintf(w, "Project: %s\n", project)
	fmt.Fprintf(w, "Date range: %s through %s (inclusive)\n", request.StartDate, request.EndDate)
	fmt.Fprintln(w, "Evidence: comments, worklogs, and status changes authored by the configured account.")
	fmt.Fprintln(w, "Current assignee and updated timestamps are not used as activity evidence.")
	return nil
}
