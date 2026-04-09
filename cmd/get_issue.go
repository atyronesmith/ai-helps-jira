package cmd

import (
	"fmt"
	"time"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var (
	flagNoComments bool
	flagNoLinks    bool
)

var getIssueCmd = &cobra.Command{
	Use:   "get-issue [issue-key]",
	Short: "Get full details of a JIRA issue",
	Args:  cobra.ExactArgs(1),
	RunE:  runGetIssue,
}

func init() {
	getIssueCmd.Flags().BoolVar(&flagNoComments, "no-comments", false, "Skip fetching comments.")
	getIssueCmd.Flags().BoolVar(&flagNoLinks, "no-links", false, "Skip fetching linked issues.")
	rootCmd.AddCommand(getIssueCmd)
}

func runGetIssue(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
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

	issueKey := args[0]

	// Check detail cache first (zero time = return cached regardless of age)
	detail, ok := db.GetIssueDetail(issueKey, time.Time{})
	if ok {
		if flagVerbose >= 2 {
			pterm.FgLightGreen.Printfln("cache: detail HIT %s", issueKey)
		}
	} else {
		if flagVerbose >= 2 {
			pterm.FgLightYellow.Printfln("cache: detail MISS %s", issueKey)
		}
		detail, err = client.GetIssue(issueKey)
		if err != nil {
			return err
		}
		db.UpsertIssueDetail(detail)
	}

	pterm.FgLightWhite.Printfln("%s: %s", detail.Key, detail.Summary)
	fmt.Printf("  Type:     %s\n", detail.IssueType)
	fmt.Printf("  Status:   %s\n", detail.Status)
	fmt.Printf("  Priority: %s\n", detail.Priority)
	if detail.Assignee != "" {
		fmt.Printf("  Assignee: %s\n", detail.Assignee)
	}
	if detail.ParentKey != "" {
		fmt.Printf("  Parent:   %s (%s)\n", detail.ParentKey, detail.ParentSummary)
	}
	if len(detail.Labels) > 0 {
		fmt.Printf("  Labels:   %v\n", detail.Labels)
	}
	if detail.Description != "" {
		fmt.Printf("\n%s\n", detail.Description)
	}

	if !flagNoLinks {
		// Check link cache first
		links, _ := db.GetIssueLinks(issueKey)
		if len(links) == 0 {
			_, links, err = client.GetIssueWithLinks(issueKey)
			if err == nil && len(links) > 0 {
				db.UpsertIssueLinks(links)
			}
		} else if flagVerbose >= 2 {
			pterm.FgLightGreen.Printfln("cache: links HIT %s (%d links)", issueKey, len(links))
		}
		if len(links) > 0 {
			fmt.Println()
			pterm.FgLightWhite.Println("Linked Issues:")
			for _, l := range links {
				fmt.Printf("  %s [%s] %s — %s\n", l.TargetKey, l.TargetStatus, l.TargetSummary, l.LinkType)
			}
		}
	}

	if !flagNoComments {
		// Check comment cache first
		comments, _ := db.GetCommentsByKeys([]string{issueKey}, time.Time{})
		if len(comments) == 0 {
			comments, err = client.GetComments(issueKey)
			if err == nil && len(comments) > 0 {
				db.UpsertComments(comments)
			}
		} else if flagVerbose >= 2 {
			pterm.FgLightGreen.Printfln("cache: comments HIT %s (%d comments)", issueKey, len(comments))
		}
		if len(comments) > 0 {
			fmt.Println()
			pterm.FgLightWhite.Printfln("Comments (%d):", len(comments))
			for _, c := range comments {
				fmt.Printf("  %s — %s (%s)\n", c.AuthorName, c.Created.Format("2006-01-02 15:04"), c.IssueKey)
				fmt.Printf("    %s\n", c.Body)
			}
		}
	}

	return nil
}
