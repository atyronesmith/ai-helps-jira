package cmd

import (
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

	db, err := cache.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	if flagRefresh {
		slog.Info("refresh requested, clearing cache")
		if err := db.Clear(cfg.JiraProject); err != nil {
			return err
		}
	}

	lastFetch := db.LastFetch(cfg.JiraProject, cfg.Assignee())

	var boards []jira.BoardInfo
	var openIssues []jira.Issue

	if flagCacheOnly {
		// Cache only: no API calls at all
		if lastFetch.IsZero() {
			return fmt.Errorf("no cached data for project %s — run without --cache-only first", cfg.JiraProject)
		}
		pterm.FgLightWhite.Printf("Using cached data from %s\n",
			lastFetch.Format("2006-01-02 15:04"))
		slog.Info("cache-only mode", "last_fetch", lastFetch)

		boards, err = db.GetBoards(cfg.JiraProject)
		if err != nil {
			return err
		}
		openIssues, err = db.GetIssues(cfg.JiraProject)
		if err != nil {
			return err
		}
	} else if !lastFetch.IsZero() && !flagRefresh {
		// Delta fetch: only get updated issues from API
		// Subtract 5 min buffer to account for clock skew
		since := lastFetch.Add(-5 * time.Minute)
		pterm.FgLightWhite.Printf("Using cache, fetching changes since %s\n",
			since.Format("2006-01-02 15:04"))
		slog.Info("delta fetch", "since", since, "last_fetch", lastFetch)

		client, err := jira.NewClient(cfg)
		if err != nil {
			return err
		}

		spinner := format.StatusPrinter("Fetching recent changes...")
		delta, err := client.GetOpenIssuesSince(since)
		spinner.Stop()
		if err != nil {
			return err
		}

		slog.Info("delta returned", "count", len(delta))
		if len(delta) > 0 {
			if err := db.UpsertIssues(cfg.JiraProject, delta); err != nil {
				return err
			}
		}
		db.RemoveDone(cfg.JiraProject)
		db.LogFetch(cfg.JiraProject, cfg.Assignee())

		// Reconstruct from cache
		boards, err = db.GetBoards(cfg.JiraProject)
		if err != nil {
			return err
		}
		openIssues, err = db.GetIssues(cfg.JiraProject)
		if err != nil {
			return err
		}
	} else {
		// Full fetch: get everything from API
		pterm.FgLightWhite.Println("Connecting to JIRA...")
		client, err := jira.NewClient(cfg)
		if err != nil {
			return err
		}

		spinner := format.StatusPrinter("Fetching board issues...")
		boards, err = client.GetBoardIssues()
		spinner.Stop()
		if err != nil {
			return err
		}

		spinner = format.StatusPrinter("Fetching open issues...")
		openIssues, err = client.GetOpenIssues()
		spinner.Stop()
		if err != nil {
			return err
		}

		// Cache everything
		var allIssues []jira.Issue
		for _, b := range boards {
			allIssues = append(allIssues, b.Issues...)
		}
		allIssues = append(allIssues, openIssues...)
		if len(allIssues) > 0 {
			if err := db.UpsertIssues(cfg.JiraProject, allIssues); err != nil {
				return err
			}
		}
		db.RemoveDone(cfg.JiraProject)
		db.LogFetch(cfg.JiraProject, cfg.Assignee())
	}

	slog.Info("summary complete", "boards", len(boards), "open_issues", len(openIssues))
	format.DisplaySummary(boards, openIssues)

	outfile := flagOutfile
	if outfile == "" {
		outfile = fmt.Sprintf("%s.md", cfg.JiraProject)
	}
	return format.WriteSummaryMarkdown(boards, openIssues, outfile,
		cfg.JiraServer, flagSlackMarkdown)
}
