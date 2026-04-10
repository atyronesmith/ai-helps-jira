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
	flagSimilarText      string
	flagSimilarThreshold float64
	flagSimilarMax       int
)

var findSimilarCmd = &cobra.Command{
	Use:   "find-similar [issue-key]",
	Short: "Find duplicate or related issues using AI similarity analysis",
	Long: `Analyze a JIRA issue (or freeform text) against open project issues
to find duplicates and related tickets.

Examples:
  jira-cli find-similar PROJ-123
  jira-cli find-similar --text "users cannot login after password reset"
  jira-cli find-similar PROJ-123 --threshold 0.8
  jira-cli find-similar PROJ-123 -f pretty`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFindSimilar,
}

func init() {
	findSimilarCmd.Flags().StringVar(&flagSimilarText, "text", "",
		"Freeform text to find similar issues for (instead of an issue key).")
	findSimilarCmd.Flags().Float64Var(&flagSimilarThreshold, "threshold", 0.5,
		"Minimum confidence threshold (0.0-1.0). Default 0.5.")
	findSimilarCmd.Flags().IntVar(&flagSimilarMax, "max", 200,
		"Maximum candidate issues to compare against. Default 200.")
	rootCmd.AddCommand(findSimilarCmd)
}

func runFindSimilar(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	issueKey := ""
	if len(args) > 0 {
		issueKey = args[0]
	}

	if issueKey == "" && flagSimilarText == "" {
		return fmt.Errorf("provide an issue key or --text")
	}

	db, err := cache.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	provider, err := llm.NewProvider(cfg)
	if err != nil {
		return err
	}

	// Fetch candidate issues
	spinner := format.StatusPrinter("Fetching open issues...")
	jql := fmt.Sprintf(
		"project = %s AND status NOT IN (Done, Closed, Resolved) ORDER BY updated DESC",
		cfg.JiraProject,
	)
	issues, err := client.SearchJQL(jql, flagSimilarMax)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("JIRA search failed: %w", err)
	}

	if len(issues) == 0 {
		pterm.FgGreen.Println("No open issues found.")
		return nil
	}

	// Fetch details for candidates (cache-aware)
	updatedByKey := make(map[string]time.Time, len(issues))
	for _, issue := range issues {
		updatedByKey[issue.Key] = issue.Updated
	}
	freshKeys := db.GetFreshDetailKeys(updatedByKey)

	var candidates []*jira.IssueDetail
	var fetchedDetails []*jira.IssueDetail
	for _, issue := range issues {
		var detail *jira.IssueDetail
		if freshKeys[issue.Key] {
			if cached, ok := db.GetIssueDetail(issue.Key, issue.Updated); ok {
				detail = cached
			}
		}
		if detail == nil {
			detail, err = client.GetIssue(issue.Key)
			if err != nil {
				slog.Warn("failed to get issue details", "key", issue.Key, "error", err)
				continue
			}
			fetchedDetails = append(fetchedDetails, detail)
		}
		candidates = append(candidates, detail)
	}
	if len(fetchedDetails) > 0 {
		db.UpsertIssueDetails(fetchedDetails)
	}

	pterm.FgLightWhite.Printfln("Comparing against %d candidates...", len(candidates))

	var result *llm.SimilarityResult

	if issueKey != "" {
		// Find the target issue
		var target *jira.IssueDetail
		for _, c := range candidates {
			if c.Key == issueKey {
				target = c
				break
			}
		}
		if target == nil {
			spinner = format.StatusPrinter(fmt.Sprintf("Fetching %s...", issueKey))
			target, err = client.GetIssue(issueKey)
			spinner.Stop()
			if err != nil {
				return fmt.Errorf("get issue %s: %w", issueKey, err)
			}
		}

		filtered := llm.PrepareCandidates(issueKey, candidates)
		spinner = format.StatusPrinter("Analyzing similarity...")
		result, err = llm.FindSimilar(provider, target, filtered, flagSimilarThreshold)
		spinner.Stop()
		if err != nil {
			return fmt.Errorf("similarity analysis: %w", err)
		}
	} else {
		spinner = format.StatusPrinter("Analyzing similarity...")
		result, err = llm.FindSimilarByText(provider, flagSimilarText, candidates, flagSimilarThreshold)
		spinner.Stop()
		if err != nil {
			return fmt.Errorf("similarity analysis: %w", err)
		}
	}

	fmt.Println()
	if flagFormat == "pretty" {
		format.DisplaySimilarIssues(result.TargetKey, result.TargetText, result.Matches, cfg.JiraServer)
	} else {
		fmt.Print(format.RenderSimilarIssues(result.TargetKey, result.TargetText, result.Matches, cfg.JiraServer, flagFormat))
	}

	return nil
}
