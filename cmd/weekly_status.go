package cmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/confluence"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var (
	flagWSStartDate       string
	flagWSEndDate         string
	flagWSConfluence      bool
	flagWSConfluenceParent string
)

var weeklyStatusCmd = &cobra.Command{
	Use:   "weekly-status",
	Short: "Generate a weekly status report from JIRA activity",
	Long: `Query JIRA for your work during a date range, fetch issue details and
comments, and generate a narrative status report using an LLM.

Examples:
  jira-cli weekly-status
  jira-cli weekly-status --start-date 2026-03-30 --end-date 2026-04-03
  jira-cli weekly-status --confluence
  jira-cli weekly-status --confluence --confluence-parent-id 123456`,
	RunE: runWeeklyStatus,
}

func init() {
	weeklyStatusCmd.Flags().StringVar(&flagWSStartDate, "start-date", "", "Start of period (YYYY-MM-DD). Defaults to 7 days ago.")
	weeklyStatusCmd.Flags().StringVar(&flagWSEndDate, "end-date", "", "End of period (YYYY-MM-DD). Defaults to today.")
	weeklyStatusCmd.Flags().BoolVar(&flagWSConfluence, "confluence", false, "Post report to Confluence.")
	weeklyStatusCmd.Flags().StringVar(&flagWSConfluenceParent, "confluence-parent-id", "", "Confluence parent page ID.")
	rootCmd.AddCommand(weeklyStatusCmd)
}

func runWeeklyStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	// Default date range: 7 days ago through today
	startStr := flagWSStartDate
	endStr := flagWSEndDate
	now := time.Now()

	if endStr == "" {
		endStr = now.Format("2006-01-02")
	}
	if startStr == "" {
		startStr = now.AddDate(0, 0, -7).Format("2006-01-02")
	}

	startTime, err := time.Parse("2006-01-02", startStr)
	if err != nil {
		return fmt.Errorf("invalid start-date: %w", err)
	}
	endTime, err := time.Parse("2006-01-02", endStr)
	if err != nil {
		return fmt.Errorf("invalid end-date: %w", err)
	}
	endTime = endTime.Add(24*time.Hour - time.Second)

	pterm.FgLightWhite.Printfln("Weekly status: %s to %s", startStr, endStr)

	db, err := cache.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	// Check cache
	cacheKey := fmt.Sprintf("weekly:%s:%s:%s", cfg.JiraProject, startStr, endStr)

	if flagVerbose >= 2 {
		pterm.FgLightCyan.Printfln("cache: key=%s", cacheKey)
	}

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	assigneeJQL := cfg.Assignee()
	if email := cfg.AssigneeEmail(); email != "" {
		assigneeJQL = fmt.Sprintf("%q", email)
	}
	jql := fmt.Sprintf(
		`project = %s AND updatedBy = %s AND updated >= "%s" ORDER BY updated DESC`,
		cfg.JiraProject, assigneeJQL, startStr,
	)

	spinner := format.StatusPrinter("Searching JIRA...")
	issues, err := client.SearchJQL(jql, 100)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("JIRA search failed: %w", err)
	}

	if len(issues) == 0 {
		pterm.FgYellow.Println("No issues found for the specified date range.")
		return nil
	}

	// Check if cache is still valid
	var latestUpdate time.Time
	for _, issue := range issues {
		if issue.Updated.After(latestUpdate) {
			latestUpdate = issue.Updated
		}
	}

	if flagVerbose >= 2 {
		pterm.FgLightCyan.Printfln("cache: latest JIRA update=%s", latestUpdate.Format(time.RFC3339))
	}

	var status *llm.WeeklyStatusContent
	if cachedJSON, cachedAt, ok := db.GetWeeklyCache(cacheKey); ok {
		if flagVerbose >= 2 {
			pterm.FgLightCyan.Printfln("cache: found entry, cached_at=%s", cachedAt.Format(time.RFC3339))
		}
		if !latestUpdate.After(cachedAt) {
			if err := json.Unmarshal([]byte(cachedJSON), &status); err == nil {
				if flagVerbose >= 2 {
					pterm.FgLightGreen.Println("cache: HIT (no JIRA changes since cached_at)")
				}
				pterm.FgLightWhite.Println("Using cached result (no JIRA changes since last run)")
			} else {
				if flagVerbose >= 2 {
					pterm.FgLightRed.Printfln("cache: corrupt entry, discarding: %v", err)
				}
				status = nil
			}
		} else {
			if flagVerbose >= 2 {
				pterm.FgLightYellow.Printfln("cache: MISS (JIRA updated %s > cached %s)",
					latestUpdate.Format(time.RFC3339), cachedAt.Format(time.RFC3339))
			}
		}
	} else {
		if flagVerbose >= 2 {
			pterm.FgLightYellow.Println("cache: MISS (no cached entry)")
		}
	}

	if status == nil {
		db.UpsertIssues(cfg.JiraProject, issues)

		// Build map of issue updated times for cache freshness checks
		updatedByKey := make(map[string]time.Time, len(issues))
		for _, issue := range issues {
			updatedByKey[issue.Key] = issue.Updated
		}
		freshKeys := db.GetFreshDetailKeys(updatedByKey)

		var items []llm.IssueWithComments
		var cachedDetails []*jira.IssueDetail
		var fetchedDetails []*jira.IssueDetail
		for i, issue := range issues {
			var detail *jira.IssueDetail

			if freshKeys[issue.Key] {
				// Cache hit — use cached detail
				if cached, ok := db.GetIssueDetail(issue.Key, issue.Updated); ok {
					detail = cached
					if flagVerbose >= 2 {
						pterm.FgLightGreen.Printfln("cache: detail HIT %s", issue.Key)
					}
					cachedDetails = append(cachedDetails, detail)
				}
			}

			if detail == nil {
				// Cache miss — fetch from API
				spinner := format.StatusPrinter(fmt.Sprintf("Fetching details (%d/%d) %s...", i+1, len(issues), issue.Key))
				var err error
				detail, err = client.GetIssue(issue.Key)
				spinner.Stop()
				if err != nil {
					slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
					continue
				}
				fetchedDetails = append(fetchedDetails, detail)
				if flagVerbose >= 2 {
					pterm.FgLightYellow.Printfln("cache: detail MISS %s (fetched from API)", issue.Key)
				}
			}

			// Comments: use cached if detail was cached, otherwise fetch
			var comments []jira.Comment
			if freshKeys[issue.Key] {
				comments, _ = db.GetCommentsByKeys([]string{issue.Key}, startTime)
			}
			if len(comments) == 0 {
				var err error
				comments, err = client.GetComments(issue.Key)
				if err != nil {
					slog.Warn("failed to get comments", "key", issue.Key, "error", err)
				}
				if len(comments) > 0 {
					db.UpsertComments(comments)
				}
			}

			var relevantComments []jira.Comment
			for _, c := range comments {
				if c.Created.After(startTime) && c.Created.Before(endTime) {
					relevantComments = append(relevantComments, c)
				}
			}

			hasActivity := len(relevantComments) > 0 || (issue.Updated.After(startTime) && issue.Updated.Before(endTime))
			if !hasActivity {
				continue
			}

			item := llm.IssueWithComments{
				Issue:    *detail,
				Comments: relevantComments,
			}
			if detail.ParentKey != "" {
				item.Parent = &jira.IssueDetail{
					Key:     detail.ParentKey,
					Summary: detail.ParentSummary,
				}
			}
			items = append(items, item)
		}

		// Cache any newly fetched details
		if len(fetchedDetails) > 0 {
			db.UpsertIssueDetails(fetchedDetails)
		}
		if flagVerbose >= 2 {
			pterm.FgLightCyan.Printfln("cache: details %d cached, %d fetched", len(cachedDetails), len(fetchedDetails))
		}

		if len(items) == 0 {
			pterm.FgYellow.Println("No issues with activity found in the specified date range.")
			return nil
		}

		spinner = format.StatusPrinter("Generating status report...")
		status, err = llm.GenerateWeeklyStatus(cfg, items, startStr, endStr)
		spinner.Stop()
		if err != nil {
			return fmt.Errorf("LLM generation failed: %w", err)
		}

		if resultJSON, err := json.Marshal(status); err == nil {
			if err := db.SetWeeklyCache(cacheKey, string(resultJSON)); err != nil {
				slog.Warn("cache: failed to write", "key", cacheKey, "error", err)
				if flagVerbose >= 2 {
					pterm.FgLightRed.Printfln("cache: FILL FAILED key=%s: %v", cacheKey, err)
				}
			} else if flagVerbose >= 2 {
				pterm.FgLightGreen.Printfln("cache: FILL key=%s (%d bytes)", cacheKey, len(resultJSON))
			}
		}
	}

	// Output
	data := &format.WeeklyStatusData{UserName: status.UserName}
	for _, p := range status.Projects {
		data.Projects = append(data.Projects, format.WeeklyProject{
			ProjectName: p.ProjectName,
			IssueKey:    p.IssueKey,
			Bullets:     p.Bullets,
		})
	}

	fmt.Println()
	if flagFormat == "pretty" {
		format.DisplayWeeklyStatus(data, cfg.JiraServer)
	} else {
		fmt.Print(format.RenderWeeklyStatus(data, cfg.JiraServer, flagFormat))
	}

	// Confluence posting
	if flagWSConfluence {
		parentID := flagWSConfluenceParent
		if parentID == "" {
			return fmt.Errorf("--confluence-parent-id is required")
		}

		confClient := confluence.NewClient(cfg)
		pageTitle := fmt.Sprintf("Weekly Status: %s to %s", startStr, endStr)
		storageBody := confluence.WeeklyStatusToStorage(status.UserName, cfg.JiraServer, status.Projects)

		spinner = format.StatusPrinter("Posting to Confluence...")
		parentPage, err := confClient.GetPage(parentID)
		if err != nil {
			spinner.Stop()
			return fmt.Errorf("failed to get confluence parent page: %w", err)
		}

		existing, _ := confClient.GetPageByTitle(parentPage.SpaceID, pageTitle)

		var childPageID string
		if existing != nil {
			existingPage, _, err := confClient.GetPageBody(existing.ID)
			if err != nil {
				spinner.Stop()
				return fmt.Errorf("failed to get existing page: %w", err)
			}
			if err := confClient.UpdatePage(existing.ID, pageTitle, storageBody, existingPage.Version.Number); err != nil {
				spinner.Stop()
				return fmt.Errorf("failed to update confluence page: %w", err)
			}
			childPageID = existing.ID
			spinner.Stop()
			pterm.FgGreen.Printfln("Confluence page updated: %s/wiki/pages/%s", cfg.JiraServer, existing.ID)
		} else {
			newPage, err := confClient.CreatePage(parentPage.SpaceID, parentID, pageTitle, storageBody)
			if err != nil {
				spinner.Stop()
				return fmt.Errorf("failed to create confluence page: %w", err)
			}
			childPageID = newPage.ID
			spinner.Stop()
			pterm.FgGreen.Printfln("Confluence page created: %s/wiki/pages/%s", cfg.JiraServer, newPage.ID)
		}

		// Update parent page index
		parentWithBody, parentBody, err := confClient.GetPageBody(parentID)
		if err != nil {
			slog.Warn("failed to read parent page body", "error", err)
		} else {
			childURL := fmt.Sprintf("%s/wiki/spaces/%s/pages/%s",
				cfg.JiraServer, parentPage.SpaceKey, childPageID)
			entryLink := fmt.Sprintf(`<li><a href="%s">%s</a></li>`, childURL, pageTitle)
			newBody := confluence.InsertIndexEntry(parentBody, entryLink, pageTitle)
			if err := confClient.UpdatePage(parentID, parentWithBody.Title, newBody, parentWithBody.Version.Number); err != nil {
				slog.Warn("failed to update parent page index", "error", err)
			}
		}
	}

	return nil
}
