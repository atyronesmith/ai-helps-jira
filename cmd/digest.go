package cmd

import (
	"fmt"
	"log/slog"
	"regexp"
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
	flagDigestDays      int
	flagDigestCacheOnly bool
	flagDigestShowJQL   bool
)

var digestCmd = &cobra.Command{
	Use:   "digest [query or issue-keys]",
	Short: "Generate a progress digest for Features/Initiatives and their child work items",
	Long: `Traverse issue hierarchy from Features or Initiatives to find child Epics,
fetch recent comments, and generate an executive digest using Claude.

Shows progress updates, blockers, and things that should have started
but haven't. All fetched data is cached for subsequent runs.

By default, only shows changes since the last time digest was run for
the same query. Use --days to override with an explicit window.

You can specify issues by key, or describe what you want in natural language:

Examples:
  jira-cli digest FEAT-123
  jira-cli digest FEAT-123 FEAT-456
  jira-cli digest "Features targeting release-2.0"
  jira-cli digest "top 5 initiatives for team platform-core"
  jira-cli digest "Features assigned to jsmith in the current sprint"
  jira-cli digest "Features targeting release-2.0. Show only changes in the last day"
  jira-cli digest FEAT-123 --days 14
  jira-cli digest FEAT-123 --cache-only`,
	Args: cobra.MinimumNArgs(1),
	RunE: runDigest,
}

func init() {
	digestCmd.Flags().IntVar(&flagDigestDays, "days", 0,
		"Include comments from the last N days. Default: since last digest run, or 7 days if never run.")
	digestCmd.Flags().BoolVar(&flagDigestCacheOnly, "cache-only", false,
		"Use cached data only, no API calls.")
	digestCmd.Flags().BoolVar(&flagDigestShowJQL, "show-jql", false,
		"Show the generated JQL when using natural language queries.")
	rootCmd.AddCommand(digestCmd)
}

// issueKeyPattern matches JIRA issue keys like PROJ-123.
var issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

// allIssueKeys returns true if every arg looks like a JIRA issue key.
func allIssueKeys(args []string) bool {
	for _, a := range args {
		if !issueKeyPattern.MatchString(a) {
			return false
		}
	}
	return true
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

	// Discover issue keys — either literal keys or natural language query
	issueKeys, queryKey, nlDays, err := discoverIssueKeys(cfg, args)
	if err != nil {
		return err
	}
	if len(issueKeys) == 0 {
		pterm.FgYellow.Println("No issues found.")
		return nil
	}

	// Determine the "since" cutoff
	since := resolveSince(db, queryKey, nlDays, cmd)
	pterm.FgLightWhite.Printfln("Including comments since %s", since.Format("2006-01-02 15:04"))

	// Run digest for each discovered key
	for _, key := range issueKeys {
		if err := runIssueDigest(cfg, db, key, since); err != nil {
			pterm.FgRed.Printfln("Error processing %s: %v", key, err)
			slog.Error("digest error", "key", key, "error", err)
		}
	}

	// Record this run
	if err := db.LogDigestRun(queryKey); err != nil {
		slog.Warn("failed to log digest run", "error", err)
	}

	return nil
}

// resolveSince determines the time cutoff for comments.
// Priority: --days flag > natural language days > last digest run > 7 day default.
func resolveSince(db *cache.Cache, queryKey string, nlDays int, cmd *cobra.Command) time.Time {
	// Explicit --days flag takes highest priority
	if cmd.Flags().Changed("days") {
		return time.Now().AddDate(0, 0, -flagDigestDays)
	}

	// Days extracted from natural language query (e.g. "changes in the last day")
	if nlDays > 0 {
		slog.Info("using LLM-extracted days", "days", nlDays)
		return time.Now().AddDate(0, 0, -nlDays)
	}

	// Check last digest run
	lastRun := db.LastDigestRun(queryKey)
	if !lastRun.IsZero() {
		slog.Info("using last digest run time", "last_run", lastRun)
		return lastRun
	}

	// First run — default to 7 days
	return time.Now().AddDate(0, 0, -7)
}

// discoverIssueKeys resolves issue keys from args. If all args are JIRA keys
// (e.g. FEAT-123), uses them directly. Otherwise treats the args as a natural
// language query and uses the LLM to generate JQL.
// Returns the keys, a stable query key for last-run tracking, and an optional
// days value extracted from the natural language (0 means not specified).
func discoverIssueKeys(cfg *config.Config, args []string) ([]string, string, int, error) {
	if allIssueKeys(args) {
		queryKey := "keys:" + strings.Join(args, ",")
		return args, queryKey, 0, nil
	}

	if flagDigestCacheOnly {
		return nil, "", 0, fmt.Errorf("--cache-only requires explicit issue keys (e.g. FEAT-123), not natural language")
	}

	// Natural language query — join all args into one query string
	naturalQuery := strings.Join(args, " ")
	queryKey := "nl:" + naturalQuery

	// Generate JQL via LLM
	spinner := format.StatusPrinter("Translating query to JQL...")
	result, err := llm.GenerateJQL(cfg, naturalQuery)
	spinner.Stop()
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to generate JQL: %w", err)
	}

	if flagDigestShowJQL {
		pterm.FgLightWhite.Printfln("JQL: %s", result.JQL)
	}
	slog.Info("digest JQL generated", "query", naturalQuery, "jql", result.JQL, "days", result.Days)

	// Execute JQL
	client, err := jira.NewClient(cfg)
	if err != nil {
		return nil, "", 0, err
	}

	spinner = format.StatusPrinter("Searching JIRA...")
	issues, err := client.SearchJQL(result.JQL, 50)
	spinner.Stop()
	if err != nil {
		pterm.FgRed.Println("JIRA search failed. The generated JQL may be invalid.")
		pterm.FgLightWhite.Printfln("JQL: %s", result.JQL)
		return nil, "", 0, fmt.Errorf("jira search: %w", err)
	}

	if len(issues) == 0 {
		return nil, queryKey, result.Days, nil
	}

	pterm.FgLightWhite.Printfln("Found %d issues\n", len(issues))
	keys := make([]string, len(issues))
	for i, issue := range issues {
		keys[i] = issue.Key
	}
	return keys, queryKey, result.Days, nil
}

// isContainerType returns true for issue types that contain child issues
// and should be traversed further down the hierarchy.
func isContainerType(issueType string) bool {
	switch strings.ToLower(issueType) {
	case "initiative", "feature":
		return true
	}
	return false
}

// collectEpics walks the issue hierarchy (Initiative → Feature → Epic)
// collecting both explicit issue links and parent-child relationships.
// Returns all leaf-level links and their comments.
// Traverses up to 3 levels deep with cycle detection.
func collectEpics(client *jira.Client, db *cache.Cache, issueKey string,
	since time.Time, depth int, seen map[string]bool) ([]jira.IssueLink, []jira.Comment, error) {

	if depth > 3 || seen[issueKey] {
		return nil, nil, nil
	}
	seen[issueKey] = true

	// Fetch explicit links + subtasks
	spinner := format.StatusPrinter(fmt.Sprintf("Fetching %s and links...", issueKey))
	_, links, err := client.GetIssueWithLinks(issueKey)
	spinner.Stop()
	if err != nil {
		return nil, nil, err
	}

	// Also fetch child issues via parent relationship (JQL parent = KEY)
	spinner = format.StatusPrinter(fmt.Sprintf("Fetching %s child issues...", issueKey))
	children, err := client.GetChildIssues(issueKey)
	spinner.Stop()
	if err != nil {
		slog.Warn("failed to fetch children", "key", issueKey, "error", err)
	} else {
		// Deduplicate: children may overlap with subtasks/links
		existing := make(map[string]bool)
		for _, l := range links {
			existing[l.TargetKey] = true
		}
		for _, child := range children {
			if !existing[child.TargetKey] {
				links = append(links, child)
			}
		}
	}

	// Cache all links at this level
	if err := db.UpsertIssueLinks(links); err != nil {
		slog.Warn("failed to cache links", "error", err)
	}

	var epicLinks []jira.IssueLink
	var allComments []jira.Comment

	for _, l := range links {
		if seen[l.TargetKey] {
			continue
		}
		if isContainerType(l.TargetType) {
			// This is a Feature/Initiative — traverse deeper
			slog.Info("traversing container", "key", l.TargetKey, "type", l.TargetType, "depth", depth+1)
			childLinks, childComments, err := collectEpics(client, db, l.TargetKey, since, depth+1, seen)
			if err != nil {
				slog.Warn("failed to traverse child", "key", l.TargetKey, "error", err)
				continue
			}
			epicLinks = append(epicLinks, childLinks...)
			allComments = append(allComments, childComments...)
		} else {
			// Leaf issue (Epic, Story, Task, etc.) — collect it
			epicLinks = append(epicLinks, l)
		}
	}

	// Fetch comments for leaf issues found at this level
	for i, l := range epicLinks {
		if l.SourceKey != issueKey {
			// Comments already fetched by a deeper recursive call
			continue
		}
		spinner := format.StatusPrinter(fmt.Sprintf("Fetching comments (%d/%d) %s...", i+1, len(epicLinks), l.TargetKey))
		comments, err := client.GetComments(l.TargetKey)
		spinner.Stop()
		if err != nil {
			slog.Warn("failed to fetch comments", "key", l.TargetKey, "error", err)
			continue
		}

		if err := db.UpsertComments(comments); err != nil {
			slog.Warn("failed to cache comments", "error", err)
		}

		for _, c := range comments {
			if c.Created.After(since) {
				allComments = append(allComments, c)
			}
		}
	}

	return epicLinks, allComments, nil
}

// collectEpicsCached walks cached links recursively to find all leaf issues.
func collectEpicsCached(db *cache.Cache, issueKey string, since time.Time,
	depth int, seen map[string]bool) ([]jira.IssueLink, []jira.Comment, error) {

	if depth > 3 || seen[issueKey] {
		return nil, nil, nil
	}
	seen[issueKey] = true

	cachedLinks, err := db.GetIssueLinks(issueKey)
	if err != nil {
		return nil, nil, err
	}

	var epicLinks []jira.IssueLink
	var allComments []jira.Comment

	for _, l := range cachedLinks {
		if seen[l.TargetKey] {
			continue
		}
		if isContainerType(l.TargetType) {
			childLinks, childComments, err := collectEpicsCached(db, l.TargetKey, since, depth+1, seen)
			if err != nil {
				slog.Warn("failed to traverse cached child", "key", l.TargetKey, "error", err)
				continue
			}
			epicLinks = append(epicLinks, childLinks...)
			allComments = append(allComments, childComments...)
		} else {
			epicLinks = append(epicLinks, l)
		}
	}

	// Fetch comments for leaf issues found at this level
	var leafKeys []string
	for _, l := range epicLinks {
		if l.SourceKey == issueKey {
			leafKeys = append(leafKeys, l.TargetKey)
		}
	}
	if len(leafKeys) > 0 {
		comments, err := db.GetCommentsByKeys(leafKeys, since)
		if err != nil {
			return nil, nil, err
		}
		allComments = append(allComments, comments...)
	}

	return epicLinks, allComments, nil
}

func runIssueDigest(cfg *config.Config, db *cache.Cache, issueKey string, since time.Time) error {
	slog.Info("issue digest", "key", issueKey, "since", since)

	var parent *jira.IssueDetail
	var links []jira.IssueLink
	var allComments []jira.Comment

	if flagDigestCacheOnly {
		// Load from cache — recursive traversal
		cachedLinks, cachedComments, err := collectEpicsCached(db, issueKey, since, 0, make(map[string]bool))
		if err != nil {
			return err
		}
		if len(cachedLinks) == 0 {
			return fmt.Errorf("no cached data for %s — run without --cache-only first", issueKey)
		}
		links = cachedLinks
		allComments = cachedComments

		// Reconstruct parent from cache (minimal info)
		parent = &jira.IssueDetail{Key: issueKey, Summary: "(cached)"}
		pterm.FgLightWhite.Println("Using cached data")
	} else {
		// Fetch from JIRA
		client, err := jira.NewClient(cfg)
		if err != nil {
			return err
		}

		// Fetch parent issue details
		spinner := format.StatusPrinter(fmt.Sprintf("Fetching %s...", issueKey))
		p, _, err := client.GetIssueWithLinks(issueKey)
		spinner.Stop()
		if err != nil {
			return err
		}
		parent = p

		// Recursively walk hierarchy: Initiative → Feature → Epic
		slog.Info("walking hierarchy", "root", issueKey, "type", parent.IssueType)
		links, allComments, err = collectEpics(client, db, issueKey, since, 0, make(map[string]bool))
		if err != nil {
			return err
		}
	}

	slog.Info("digest data ready", "parent", issueKey, "links", len(links),
		"comments", len(allComments))

	if len(links) == 0 {
		pterm.FgYellow.Printfln("No linked issues found for %s", issueKey)
		return nil
	}

	if len(allComments) == 0 {
		pterm.FgYellow.Printfln("%s: %d linked issues but no new comments since cutoff", issueKey, len(links))
		return nil
	}

	pterm.FgLightWhite.Printfln("Found %d issues, %d comments for %s", len(links), len(allComments), issueKey)

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
