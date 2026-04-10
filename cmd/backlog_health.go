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
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var (
	flagHealthStaleDays int
	flagHealthNoLLM     bool
)

var backlogHealthCmd = &cobra.Command{
	Use:   "backlog-health",
	Short: "Check backlog health: stale tickets, missing fields, orphaned issues",
	Long: `Scan all open issues for common backlog problems:
  - Stale tickets (in active status for 14+ days without updates)
  - Missing descriptions
  - Orphaned issues (no parent epic)
  - Unassigned tickets in active status
  - Missing labels

Optionally generates an AI executive summary with recommendations.

Examples:
  jira-cli backlog-health
  jira-cli backlog-health --stale-days 7
  jira-cli backlog-health --no-llm
  jira-cli backlog-health -f pretty`,
	RunE: runBacklogHealth,
}

func init() {
	backlogHealthCmd.Flags().IntVar(&flagHealthStaleDays, "stale-days", 14,
		"Days without update before an active issue is considered stale.")
	backlogHealthCmd.Flags().BoolVar(&flagHealthNoLLM, "no-llm", false,
		"Skip LLM summary generation (rule-based checks only).")
	rootCmd.AddCommand(backlogHealthCmd)
}

func runBacklogHealth(cmd *cobra.Command, args []string) error {
	var cfg *config.Config
	var err error
	if flagHealthNoLLM {
		cfg, err = config.LoadJIRAOnly(flagUser, flagProject)
	} else {
		cfg, err = config.Load(flagUser, flagProject)
	}
	if err != nil {
		return err
	}
	setupLogging()

	db, err := cache.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	// Fetch all open issues
	spinner := format.StatusPrinter("Querying open issues...")
	issues, err := client.GetOpenIssues()
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("JIRA search failed: %w", err)
	}

	if len(issues) == 0 {
		pterm.FgGreen.Println("No open issues found.")
		return nil
	}

	pterm.FgLightWhite.Printfln("Found %d open issues, fetching details...", len(issues))

	// Build updated map for cache freshness
	updatedByKey := make(map[string]time.Time, len(issues))
	for _, issue := range issues {
		updatedByKey[issue.Key] = issue.Updated
	}
	freshKeys := db.GetFreshDetailKeys(updatedByKey)

	// Fetch details for each issue (cache-aware)
	var details []*jira.IssueDetail
	var fetchedDetails []*jira.IssueDetail
	for i, issue := range issues {
		var detail *jira.IssueDetail

		if freshKeys[issue.Key] {
			if cached, ok := db.GetIssueDetail(issue.Key, issue.Updated); ok {
				detail = cached
				if flagVerbose >= 2 {
					pterm.FgLightGreen.Printfln("cache: detail HIT %s", issue.Key)
				}
			}
		}

		if detail == nil {
			if flagVerbose >= 2 {
				pterm.FgLightYellow.Printfln("cache: detail MISS %s", issue.Key)
			}
			spinner := format.StatusPrinter(fmt.Sprintf("Fetching details (%d/%d) %s...", i+1, len(issues), issue.Key))
			detail, err = client.GetIssue(issue.Key)
			spinner.Stop()
			if err != nil {
				slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
				continue
			}
			fetchedDetails = append(fetchedDetails, detail)
		}

		details = append(details, detail)
	}

	// Cache newly fetched details
	if len(fetchedDetails) > 0 {
		db.UpsertIssueDetails(fetchedDetails)
	}
	if flagVerbose >= 2 {
		pterm.FgLightCyan.Printfln("cache: %d cached, %d fetched", len(details)-len(fetchedDetails), len(fetchedDetails))
	}

	// Run rule-based checks
	findings := llm.CheckBacklogHealth(details, flagHealthStaleDays)

	// Group by category for display
	report := &format.BacklogHealthData{
		TotalIssues:  len(details),
		HealthyCount: len(details) - countUniqueKeys(findings),
		StaleDays:    flagHealthStaleDays,
	}
	report.Categories = groupFindings(findings)

	// Generate LLM summary
	if !flagHealthNoLLM && len(findings) > 0 {
		spinner = format.StatusPrinter("Generating health assessment...")
		summary, recs, err := llm.GenerateHealthSummary(cfg, len(details), findings)
		spinner.Stop()
		if err != nil {
			slog.Warn("LLM summary failed, showing rule-based results only", "error", err)
		} else {
			report.ExecutiveSummary = summary
			report.Recommendations = recs
		}
	}

	fmt.Println()
	if flagFormat == "pretty" {
		format.DisplayBacklogHealth(report, cfg.JiraServer)
	} else {
		fmt.Print(format.RenderBacklogHealth(report, cfg.JiraServer, flagFormat))
	}

	return nil
}

func countUniqueKeys(findings []llm.HealthFinding) int {
	seen := make(map[string]bool)
	for _, f := range findings {
		seen[f.Key] = true
	}
	return len(seen)
}

func groupFindings(findings []llm.HealthFinding) []format.HealthCategory {
	order := []string{"stale", "unassigned_active", "missing_description", "orphaned", "missing_labels"}
	labels := map[string]string{
		"stale":               "Stale Tickets",
		"unassigned_active":   "Unassigned Active",
		"missing_description": "Missing Description",
		"orphaned":            "Orphaned (No Parent)",
		"missing_labels":      "Missing Labels",
	}

	grouped := make(map[string][]format.HealthIssue)
	for _, f := range findings {
		grouped[f.Category] = append(grouped[f.Category], format.HealthIssue{
			Key:     f.Key,
			Summary: f.Summary,
			Detail:  f.Detail,
		})
	}

	var categories []format.HealthCategory
	for _, cat := range order {
		if issues, ok := grouped[cat]; ok {
			categories = append(categories, format.HealthCategory{
				Name:   labels[cat],
				Issues: issues,
			})
		}
	}
	return categories
}
