package cmd

import (
	"fmt"
	"log/slog"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var (
	flagShowJQL    bool
	flagMaxResults int
)

var queryCmd = &cobra.Command{
	Use:   "query [natural language query]",
	Short: "Search JIRA issues using natural language",
	Long: `Translate a natural language query into JQL using an LLM,
execute the search against JIRA, and display matching issues.

Examples:
  jira-cli query "show me all critical bugs from last week"
  jira-cli query "unresolved stories assigned to me" --show-jql
  jira-cli query "epics created this month" --max 10
  jira-cli query "tickets worked on by jsmith this week"`,
	Args: cobra.ExactArgs(1),
	RunE: runQuery,
}

func init() {
	queryCmd.Flags().BoolVar(&flagShowJQL, "show-jql", false,
		"Print the generated JQL before results.")
	queryCmd.Flags().IntVar(&flagMaxResults, "max", 50,
		"Maximum number of results to return.")
	rootCmd.AddCommand(queryCmd)
}

func runQuery(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	naturalQuery := args[0]
	slog.Info("starting query", "input", naturalQuery)

	// Step 1: LLM translates natural language to JQL
	spinner := format.StatusPrinter("Translating query to JQL...")
	result, err := llm.GenerateJQL(cfg, naturalQuery)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("failed to generate JQL: %w", err)
	}
	slog.Info("JQL generated", "jql", result.JQL)

	// Step 2: Execute JQL against JIRA
	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	spinner = format.StatusPrinter("Searching JIRA...")
	issues, err := client.SearchJQL(result.JQL, flagMaxResults)
	spinner.Stop()
	if err != nil {
		pterm.FgRed.Printfln("JIRA search failed. The generated JQL may be invalid.")
		pterm.FgLightWhite.Printfln("JQL: %s", result.JQL)
		return fmt.Errorf("jira search: %w", err)
	}

	// Step 3: Cache results
	if len(issues) > 0 {
		db, err := cache.Open()
		if err != nil {
			slog.Warn("failed to open cache for query results", "error", err)
		} else {
			if err := db.UpsertIssues(cfg.JiraProject, issues); err != nil {
				slog.Warn("failed to cache query results", "error", err)
			}
			db.Close()
		}
	}

	// Step 4: Display results
	jqlDisplay := ""
	if flagShowJQL {
		jqlDisplay = result.JQL
	}
	format.DisplayQueryResults(issues, jqlDisplay)

	return nil
}
