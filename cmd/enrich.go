package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var (
	flagApply         bool
	flagExtractPrompt bool
	flagShowPrompt    bool
)

var enrichCmd = &cobra.Command{
	Use:   "enrich [issue-key]",
	Short: "Enrich a sparse JIRA ticket with AI-generated content",
	Long: `Fetch a JIRA ticket, generate enriched content using an LLM
(description, acceptance criteria, labels, priority), and preview
the suggestions. Use --apply to update the issue in JIRA.

The enrichment prompt is configurable. Place an ENHANCE.md (or any
ENHANCE.* file) in the current directory to use a custom prompt.
Use --extract-prompt to write the built-in prompt to ENHANCE.md
for customization.

Examples:
  jira-cli enrich PROJ-123
  jira-cli enrich PROJ-123 --apply
  jira-cli enrich --extract-prompt
  jira-cli enrich --show-prompt`,
	Args: cobra.RangeArgs(0, 1),
	RunE: runEnrich,
}

func init() {
	enrichCmd.Flags().BoolVar(&flagApply, "apply", false,
		"Apply the enrichment to the JIRA issue.")
	enrichCmd.Flags().BoolVar(&flagExtractPrompt, "extract-prompt", false,
		"Write the built-in enrichment prompt to ENHANCE.md for customization.")
	enrichCmd.Flags().BoolVar(&flagShowPrompt, "show-prompt", false,
		"Display the active enrichment prompt (from file or built-in).")
	rootCmd.AddCommand(enrichCmd)
}

func runEnrich(cmd *cobra.Command, args []string) error {
	// Handle --extract-prompt: write built-in prompt to ENHANCE.md
	if flagExtractPrompt {
		if err := os.WriteFile("ENHANCE.md", []byte(llm.EnrichSystemPrompt+"\n"), 0o644); err != nil {
			return fmt.Errorf("write ENHANCE.md: %w", err)
		}
		pterm.FgGreen.Println("Built-in prompt written to ENHANCE.md")
		pterm.FgLightWhite.Println("Edit this file to customize the enrichment prompt.")
		return nil
	}

	// Handle --show-prompt: display the active prompt
	if flagShowPrompt {
		prompt, source := llm.LoadEnrichPrompt()
		pterm.FgLightWhite.Printfln("Prompt source: %s\n", source)
		fmt.Println(prompt)
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("issue key is required (e.g., jira-cli enrich PROJ-123)")
	}

	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	issueKey := args[0]
	slog.Info("starting enrich", "key", issueKey)

	// Load prompt (custom file or built-in)
	prompt, source := llm.LoadEnrichPrompt()
	if source != "(built-in)" {
		pterm.FgLightWhite.Printfln("Using custom prompt from %s", source)
	}

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
	enrichment, err := llm.GenerateEnrichment(cfg, issue, prompt)
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
