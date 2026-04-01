package cmd

import (
	"fmt"
	"log/slog"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var flagApply bool

var enrichCmd = &cobra.Command{
	Use:   "enrich [issue-key]",
	Short: "Enrich a sparse JIRA ticket with AI-generated content",
	Long: `Fetch a JIRA ticket, generate enriched content using an LLM
(description, acceptance criteria, labels, priority), and preview
the suggestions. Use --apply to update the issue in JIRA.

Examples:
  jira-cli enrich PROJ-123
  jira-cli enrich PROJ-123 --apply`,
	Args: cobra.ExactArgs(1),
	RunE: runEnrich,
}

func init() {
	enrichCmd.Flags().BoolVar(&flagApply, "apply", false,
		"Apply the enrichment to the JIRA issue.")
	rootCmd.AddCommand(enrichCmd)
}

func runEnrich(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	issueKey := args[0]
	slog.Info("starting enrich", "key", issueKey)

	// Step 1: Fetch issue from JIRA
	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	spinner := format.StatusPrinter("Fetching issue...")
	issue, err := client.GetIssue(issueKey)
	spinner.Stop()
	if err != nil {
		return err
	}
	slog.Info("issue fetched", "key", issue.Key, "type", issue.IssueType,
		"status", issue.Status)

	// Step 2: Generate enrichment with LLM
	spinner = format.StatusPrinter("Generating suggestions with Claude...")
	enrichment, err := llm.GenerateEnrichment(cfg, issue)
	spinner.Stop()
	if err != nil {
		return err
	}

	// Step 3: Preview
	format.DisplayEnrichPreview(issue, enrichment.Description,
		enrichment.AcceptanceCriteria, enrichment.Priority, enrichment.Labels)

	// Step 4: Apply if requested
	if flagApply {
		if !confirm("Apply these changes to " + issueKey + "?") {
			pterm.FgYellow.Println("Enrichment cancelled.")
			return nil
		}

		fullDescription := llm.BuildEnrichedDescription(enrichment)
		spinner = format.StatusPrinter("Updating issue...")
		err = client.UpdateIssue(issueKey, fullDescription, enrichment.Labels)
		spinner.Stop()
		if err != nil {
			return err
		}
		pterm.FgGreen.Printfln("\nIssue updated: %s - %s/browse/%s",
			issueKey, cfg.JiraServer, issueKey)
	} else {
		fmt.Println()
		pterm.FgLightWhite.Println("Run with --apply to update the issue in JIRA.")
	}

	return nil
}
