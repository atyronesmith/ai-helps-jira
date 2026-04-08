package cmd

import (
	"fmt"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var flagCommentBody string

var addCommentCmd = &cobra.Command{
	Use:   "add-comment [issue-key]",
	Short: "Add a comment to a JIRA issue",
	Long: `Add a comment to a JIRA issue.

Examples:
  jira-cli add-comment PROJ-123 --body "This is done."`,
	Args: cobra.ExactArgs(1),
	RunE: runAddComment,
}

func init() {
	addCommentCmd.Flags().StringVar(&flagCommentBody, "body", "", "Comment text (required).")
	_ = addCommentCmd.MarkFlagRequired("body")
	rootCmd.AddCommand(addCommentCmd)
}

func runAddComment(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	comment, err := client.AddComment(args[0], flagCommentBody)
	if err != nil {
		return err
	}

	pterm.FgGreen.Printfln("Comment added to %s", args[0])
	fmt.Printf("  ID: %s, Author: %s\n", comment.ID, comment.AuthorName)
	return nil
}
