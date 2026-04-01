package cmd

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var (
	flagDigestRelease   string
	flagDigestDays      int
	flagDigestCacheOnly bool
)

var digestCmd = &cobra.Command{
	Use:   "digest [issue-key]",
	Short: "Generate a progress digest for a Feature/Initiative and its linked Epics",
	Long: `Traverse issue links from a Feature or Initiative to find child Epics,
fetch recent comments, and generate an executive digest using Claude.

Shows progress updates, blockers, and things that should have started
but haven't. All fetched data is cached for subsequent runs.

Examples:
  jira-cli digest FEAT-123
  jira-cli digest FEAT-123 --days 14
  jira-cli digest --release "2.0"
  jira-cli digest FEAT-123 --cache-only`,
	Args: cobra.RangeArgs(0, 1),
	RunE: runDigest,
}

func init() {
	digestCmd.Flags().StringVar(&flagDigestRelease, "release", "",
		"Digest all issues in a fixVersion/release.")
	digestCmd.Flags().IntVar(&flagDigestDays, "days", 7,
		"Include comments from the last N days.")
	digestCmd.Flags().BoolVar(&flagDigestCacheOnly, "cache-only", false,
		"Use cached data only, no API calls.")
	rootCmd.AddCommand(digestCmd)
}

func runDigest(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	db, err := cache.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	since := time.Now().AddDate(0, 0, -flagDigestDays)

	if flagDigestRelease != "" {
		return runReleaseDigest(cfg, db, since)
	}

	if len(args) == 0 {
		return fmt.Errorf("issue key or --release is required")
	}

	return runIssueDigest(cfg, db, args[0], since)
}

func runReleaseDigest(cfg *config.Config, db *cache.Cache, since time.Time) error {
	slog.Info("release digest", "release", flagDigestRelease)

	if flagDigestCacheOnly {
		return fmt.Errorf("--cache-only is not supported with --release (no way to know which issues are in the release without an API call)")
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	spinner := format.StatusPrinter(fmt.Sprintf("Fetching issues in release %q...", flagDigestRelease))
	issues, err := client.SearchByRelease(flagDigestRelease)
	spinner.Stop()
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		pterm.FgYellow.Printfln("No issues found in release %q", flagDigestRelease)
		return nil
	}

	pterm.FgLightWhite.Printfln("Found %d issues in release %q\n", len(issues), flagDigestRelease)

	for _, issue := range issues {
		if err := runIssueDigest(cfg, db, issue.Key, since); err != nil {
			pterm.FgRed.Printfln("Error processing %s: %v", issue.Key, err)
			slog.Error("digest error", "key", issue.Key, "error", err)
		}
	}
	return nil
}

func runIssueDigest(cfg *config.Config, db *cache.Cache, issueKey string, since time.Time) error {
	slog.Info("issue digest", "key", issueKey, "since", since)

	var parent *jira.IssueDetail
	var links []jira.IssueLink
	var allComments []jira.Comment

	if flagDigestCacheOnly {
		// Load from cache
		cachedLinks, err := db.GetIssueLinks(issueKey)
		if err != nil {
			return err
		}
		if len(cachedLinks) == 0 {
			return fmt.Errorf("no cached data for %s — run without --cache-only first", issueKey)
		}
		links = cachedLinks

		// Reconstruct parent from cache (minimal info)
		parent = &jira.IssueDetail{Key: issueKey, Summary: "(cached)"}

		var epicKeys []string
		for _, l := range links {
			epicKeys = append(epicKeys, l.TargetKey)
		}
		allComments, err = db.GetCommentsByKeys(epicKeys, since)
		if err != nil {
			return err
		}
		pterm.FgLightWhite.Println("Using cached data")
	} else {
		// Fetch from JIRA
		client, err := jira.NewClient(cfg)
		if err != nil {
			return err
		}

		spinner := format.StatusPrinter(fmt.Sprintf("Fetching %s and links...", issueKey))
		p, l, err := client.GetIssueWithLinks(issueKey)
		spinner.Stop()
		if err != nil {
			return err
		}
		parent = p
		links = l

		// Cache the links
		if err := db.UpsertIssueLinks(links); err != nil {
			slog.Warn("failed to cache links", "error", err)
		}

		// Filter to find linked Epics (by target issue type, case-insensitive)
		var epicLinks []jira.IssueLink
		for _, l := range links {
			if strings.EqualFold(l.TargetType, "Epic") {
				epicLinks = append(epicLinks, l)
			}
		}

		if len(epicLinks) == 0 {
			// No epics — use all links instead
			pterm.FgLightWhite.Printfln("No Epic links found for %s, using all %d linked issues", issueKey, len(links))
			epicLinks = links
		}
		links = epicLinks

		// Fetch comments for each linked epic
		for i, l := range links {
			spinner = format.StatusPrinter(fmt.Sprintf("Fetching comments (%d/%d) %s...", i+1, len(links), l.TargetKey))
			comments, err := client.GetComments(l.TargetKey)
			spinner.Stop()
			if err != nil {
				slog.Warn("failed to fetch comments", "key", l.TargetKey, "error", err)
				continue
			}

			// Cache all comments
			if err := db.UpsertComments(comments); err != nil {
				slog.Warn("failed to cache comments", "error", err)
			}

			// Filter to --days window
			for _, c := range comments {
				if c.Created.After(since) {
					allComments = append(allComments, c)
				}
			}
		}
	}

	slog.Info("digest data ready", "parent", issueKey, "links", len(links),
		"comments", len(allComments), "days", flagDigestDays)

	if len(links) == 0 {
		pterm.FgYellow.Printfln("No linked issues found for %s", issueKey)
		return nil
	}

	// Generate digest with LLM
	spinner := format.StatusPrinter("Generating digest with Claude...")
	digest, err := llm.GenerateDigest(cfg, parent, links, allComments)
	spinner.Stop()
	if err != nil {
		return err
	}

	// Convert LLM types to format types (avoids circular import)
	displayDigest := &format.DigestData{
		OverallStatus: digest.OverallStatus,
		Summary:       digest.Summary,
	}
	for _, p := range digest.ProgressUpdates {
		displayDigest.Progress = append(displayDigest.Progress, format.DigestProgress{
			EpicKey: p.EpicKey, EpicSummary: p.EpicSummary,
			Status: p.Status, Update: p.Update,
		})
	}
	for _, b := range digest.Blockers {
		displayDigest.Blockers = append(displayDigest.Blockers, format.DigestBlocker{
			EpicKey: b.EpicKey, EpicSummary: b.EpicSummary,
			Blocker: b.Blocker, Impact: b.Impact,
		})
	}
	for _, n := range digest.NotStarted {
		displayDigest.NotStarted = append(displayDigest.NotStarted, format.DigestNotStarted{
			EpicKey: n.EpicKey, EpicSummary: n.EpicSummary, Reason: n.Reason,
		})
	}

	format.DisplayDigest(parent, displayDigest)

	// Write markdown if outfile specified
	outfile := flagOutfile
	if outfile != "" {
		return format.WriteDigestMarkdown(parent, displayDigest, outfile,
			cfg.JiraServer, flagSlackMarkdown)
	}

	return nil
}
