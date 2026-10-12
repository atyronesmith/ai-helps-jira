package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var (
	flagShowJQL    bool
	flagMaxResults int
)

var queryCmd = &cobra.Command{
	Use:   "query [natural language query]",
	Short: "Search JIRA issues using natural language",
	Long: `Translate a natural language query into JQL (a deterministic parser
handles common queries; an LLM handles the complex ones it cannot), then
execute the search against JIRA and display matching issues.

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

	// Open the cache once and reuse it for the project-list warm-up and the
	// result cache below. A warm-up never fails a query, so a cache-open error
	// is logged and the query proceeds with the env-only project vocabulary.
	var db *cache.Cache
	if c, err := cache.Open(); err != nil {
		slog.Warn("failed to open cache", "error", err)
	} else {
		db = c
		defer c.Close()
	}

	// Step 1: translate the query to JQL -- a deterministic parser first, LLM
	// fallback for anything it cannot parse -- folding the tenant's cached
	// project list so human names like "Support" resolve to "SUP".
	spinner := format.StatusPrinter("Translating query to JQL...")
	projects := fetchProjectList(cfg, db)
	result, err := llm.GenerateJQLWithProjects(context.Background(), cfg, naturalQuery, projects)
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
	if db != nil && len(issues) > 0 {
		if err := db.UpsertIssues(cfg.JiraProject, issues); err != nil {
			slog.Warn("failed to cache query results", "error", err)
		}
	}

	// Step 4: Display results
	//
	// The deterministic fast path is a closed grammar and can be trusted
	// silently; the LLM-routed tiers cannot, so their JQL is echoed without
	// needing --show-jql. Either way, relative dates ("-365d") are resolved
	// to absolute ones for this display only -- the JQL actually sent to
	// JIRA above is untouched -- so an opaque rolling window is visible for
	// what it is next to the request that produced it.
	jqlDisplay, routeDisplay := "", ""
	if flagShowJQL || result.Route != llm.RouteFastPath {
		jqlDisplay = jql.ResolveRelativeDatesForDisplay(result.JQL, time.Now())
		routeDisplay = result.Route
	}
	format.DisplayQueryResults(issues, jqlDisplay, routeDisplay)

	return nil
}
