package cmd

import (
	"fmt"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var summarizeCommentsCmd = &cobra.Command{
	Use:   "summarize-comments [issue-key]",
	Short: "Summarize a JIRA issue's comment thread using AI",
	Long: `Fetch all comments for a JIRA issue and generate a structured summary
using an LLM: key decisions, action items, and open questions.

Examples:
  jira-cli summarize-comments PROJ-123
  jira-cli summarize-comments PROJ-123 -f pretty`,
	Args: cobra.ExactArgs(1),
	RunE: runSummarizeComments,
}

func init() {
	rootCmd.AddCommand(summarizeCommentsCmd)
}

func runSummarizeComments(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	issueKey := args[0]

	db, err := cache.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	// Fetch issue details (cache-aware)
	detail, ok := db.GetIssueDetail(issueKey, time.Time{})
	if ok {
		if flagVerbose >= 2 {
			pterm.FgLightGreen.Printfln("cache: detail HIT %s", issueKey)
		}
	} else {
		if flagVerbose >= 2 {
			pterm.FgLightYellow.Printfln("cache: detail MISS %s", issueKey)
		}
		detail, err = client.GetIssue(issueKey)
		if err != nil {
			return err
		}
		db.UpsertIssueDetail(detail)
	}

	// Fetch comments (cache-aware)
	comments, _ := db.GetCommentsByKeys([]string{issueKey}, time.Time{})
	if len(comments) > 0 && flagVerbose >= 2 {
		pterm.FgLightGreen.Printfln("cache: comments HIT %s (%d comments)", issueKey, len(comments))
	}
	if len(comments) == 0 {
		if flagVerbose >= 2 {
			pterm.FgLightYellow.Printfln("cache: comments MISS %s", issueKey)
		}
		comments, err = client.GetComments(issueKey)
		if err != nil {
			return err
		}
		if len(comments) > 0 {
			db.UpsertComments(comments)
		}
	}

	if len(comments) == 0 {
		pterm.FgYellow.Printfln("No comments found on %s.", issueKey)
		return nil
	}

	pterm.FgLightWhite.Printfln("%s: %s (%d comments)", detail.Key, detail.Summary, len(comments))

	spinner := format.StatusPrinter("Summarizing comments...")
	summary, err := llm.GenerateCommentSummary(cfg, detail, comments)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	// Convert to format types
	displaySummary := &format.CommentSummaryData{
		Summary:       summary.Summary,
		KeyDecisions:  summary.KeyDecisions,
		ActionItems:   summary.ActionItems,
		OpenQuestions: summary.OpenQuestions,
	}

	fmt.Println()
	if flagFormat == "pretty" {
		format.DisplayCommentSummary(detail, displaySummary)
	} else {
		fmt.Print(format.RenderCommentSummary(detail, displaySummary, cfg.JiraServer, flagFormat))
	}

	return nil
}
