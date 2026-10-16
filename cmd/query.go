package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
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
	flagShowJQL        bool
	flagDryRun         bool
	flagMaxResults     int
	flagExactCount     bool
	queryTranslator    = llm.GenerateJQLWithProjects
	queryProjectLoader = fetchProjectList
	querySearcher      = func(ctx context.Context, cfg *config.Config, query string, opts jira.SearchOptions) (jira.SearchResult, error) {
		client, err := jira.NewClient(cfg)
		if err != nil {
			return jira.SearchResult{}, err
		}
		return client.SearchJQLResult(ctx, query, opts)
	}
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
		"Maximum number of issue rows to display (default 50).")
	queryCmd.Flags().BoolVar(&flagExactCount, "count", false,
		"Enumerate all matching issues and report an exact count (page size 100, at most 100 pages, 60-second deadline).")
	queryCmd.Flags().BoolVar(&flagDryRun, "dry-run", false,
		"Translate and locally validate JQL without searching JIRA.")
	rootCmd.AddCommand(queryCmd)
}

func runQuery(cmd *cobra.Command, args []string) error {
	if cmd.Flags().Changed("max") && flagMaxResults <= 0 {
		return fmt.Errorf("--max must be greater than zero")
	}
	loadConfig := config.Load
	if flagDryRun {
		loadConfig = config.LoadTranslation
	}
	cfg, err := loadConfig(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	naturalQuery := args[0]
	if !flagDryRun {
		slog.Info("starting query")
	}

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
	var projects []jira.Project
	if flagDryRun {
		projects = cachedProjectList(db)
	} else {
		projects = queryProjectLoader(cfg, db)
	}
	result, err := queryTranslator(cmd.Context(), cfg, naturalQuery, projects)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("failed to generate JQL: %w", err)
	}
	exactCount := flagExactCount || llm.IsCountQuery(naturalQuery)
	return finishQuery(cmd, cfg, result, db, flagDryRun, flagMaxResults, exactCount, flagShowJQL, querySearcher)
}

type querySearchFunc func(ctx context.Context, cfg *config.Config, query string, opts jira.SearchOptions) (jira.SearchResult, error)

func finishQuery(cmd *cobra.Command, cfg *config.Config, result *llm.QueryResult, db *cache.Cache, dryRun bool, max int, exactCount bool, showJQL bool, search querySearchFunc) error {
	if dryRun {
		printTranslation(cmd.OutOrStdout(), cfg.JiraProject, result)
		return nil
	}
	spinner := format.StatusPrinter("Searching JIRA...")
	searchResult, err := search(cmd.Context(), cfg, result.JQL, jira.SearchOptions{Limit: max, PageSize: 100, ExactCount: exactCount, MaxPages: 100})
	spinner.Stop()
	if db != nil && err == nil && searchResult.Complete && len(searchResult.Issues) > 0 {
		if err := db.UpsertIssues(cfg.JiraProject, searchResult.Issues); err != nil {
			slog.Warn("failed to cache query results", "error", err)
		}
	}
	jqlDisplay, routeDisplay := "", ""
	if showJQL || result.Route != llm.RouteFastPath {
		jqlDisplay = jql.ResolveRelativeDatesForDisplay(result.JQL, time.Now())
		routeDisplay = result.Route
	}
	format.DisplayQueryResults(searchResult, jqlDisplay, routeDisplay)
	if err != nil {
		pterm.FgRed.Printfln("JIRA search stopped before completion.")
		pterm.FgLightWhite.Printfln("JQL: %s", result.JQL)
		return fmt.Errorf("jira search: %w", err)
	}
	return nil
}

func printTranslation(w io.Writer, defaultProject string, result *llm.QueryResult) {
	if result == nil {
		return
	}
	validation := "valid"
	if err := jql.Validate(result.JQL, jql.Options{DefaultProject: defaultProject}); err != nil {
		validation = err.Error()
	}
	fmt.Fprintf(w, "Input: %s\n", result.Input)
	fmt.Fprintf(w, "Normalized input: %s\n", result.NormalizedInput)
	fmt.Fprintf(w, "Route: %s\n", result.Route)
	if strings.TrimSpace(result.ParseError) != "" {
		fmt.Fprintf(w, "Parse error: %s\n", result.ParseError)
	}
	fmt.Fprintf(w, "JQL: %s\n", result.JQL)
	fmt.Fprintf(w, "Local validation: %s\n", validation)
}
