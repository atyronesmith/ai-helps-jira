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
)

var (
	flagRefresh   bool
	flagCacheOnly bool
	summaryLoader = cache.LoadSummary
)

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show daily summary of assigned issues and sprint info",
	RunE:  runSummary,
}

func init() {
	summaryCmd.Flags().BoolVar(&flagRefresh, "refresh", false,
		"Force full fetch, bypass cache.")
	summaryCmd.Flags().BoolVar(&flagCacheOnly, "cache-only", false,
		"Show summary from cache only, no API calls.")
	rootCmd.AddCommand(summaryCmd)
}

func runSummary(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	slog.Info("starting summary", "project", cfg.JiraProject, "user", cfg.JiraUser)

	db, err := cache.OpenForConfig(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	lastFetch := db.LastFetch(cfg.JiraProject, cfg.Assignee())

	var boards []jira.BoardInfo
	var openIssues []jira.Issue

	if flagVerbose >= 2 {
		if lastFetch.IsZero() {
			pterm.FgLightCyan.Println("cache: no previous fetch recorded")
		} else {
			pterm.FgLightCyan.Printfln("cache: last fetch=%s", lastFetch.Format(time.RFC3339))
		}
	}

	if flagRefresh {
		slog.Info("refresh requested; forcing summary reconciliation")
	}
	if flagCacheOnly && !lastFetch.IsZero() {
		pterm.FgLightWhite.Printf("Using cached data from %s\n", lastFetch.Format("2006-01-02 15:04"))
	}
	if !flagCacheOnly {
		pterm.FgLightWhite.Println("Connecting to JIRA...")
	}
	spinner := format.StatusPrinter("Loading summary...")
	boards, openIssues, fetchedAt, err := summaryLoader(commandContext(cmd), db, cfg, flagCacheOnly, func(ctx context.Context, invalidated []string) ([]jira.BoardInfo, []jira.Issue, error) {
		client, err := jira.NewClient(cfg)
		if err != nil {
			return nil, nil, err
		}
		client = client.WithContext(ctx)
		return client.FetchSummary(ctx, invalidated)
	})
	spinner.Stop()
	if err != nil {
		return err
	}
	if flagVerbose >= 2 && !fetchedAt.IsZero() {
		pterm.FgLightCyan.Printfln("cache: fetched=%s", fetchedAt.Format("2006-01-02 15:04"))
	}

	slog.Info("summary complete", "boards", len(boards), "open_issues", len(openIssues))

	if flagFormat == "pretty" {
		format.DisplaySummary(boards, openIssues)
	} else {
		fmt.Println()
		fmt.Print(format.RenderSummary(boards, openIssues, cfg.JiraServer, flagFormat))
	}

	outfile := flagOutfile
	if outfile == "" {
		outfile = fmt.Sprintf("%s.md", cfg.JiraProject)
	}
	fileFormat := flagFormat
	if fileFormat == "pretty" {
		fileFormat = "markdown"
	}
	return format.WriteSummary(boards, openIssues, outfile, cfg.JiraServer, fileFormat)
}
