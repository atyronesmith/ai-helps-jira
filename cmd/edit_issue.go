package cmd

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var (
	flagEditSummary  string
	flagEditDesc     string
	flagEditPriority string
	flagEditAssignee string
	flagEditLabels   string
)

var editIssueCmd = &cobra.Command{
	Use:   "edit-issue [issue-key]",
	Short: "Update fields on an existing JIRA issue",
	Long: `Update fields on an existing JIRA issue. Pass only the flags you want to change.

Examples:
  jira-cli edit-issue PROJ-123 --summary "New title"
  jira-cli edit-issue PROJ-123 --priority High --assignee ACCOUNT_ID
  jira-cli edit-issue PROJ-123 --labels "backend,urgent"`,
	Args: cobra.ExactArgs(1),
	RunE: runEditIssue,
}

func init() {
	editIssueCmd.Flags().StringVar(&flagEditSummary, "summary", "", "New summary/title.")
	editIssueCmd.Flags().StringVar(&flagEditDesc, "description", "", "New description.")
	editIssueCmd.Flags().StringVar(&flagEditPriority, "priority", "", "New priority.")
	editIssueCmd.Flags().StringVar(&flagEditAssignee, "assignee", "", "New assignee account ID.")
	editIssueCmd.Flags().StringVar(&flagEditLabels, "labels", "", "Comma-separated labels.")
	rootCmd.AddCommand(editIssueCmd)
}

func runEditIssue(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	issueKey := args[0]
	fields := make(map[string]any)

	if cmd.Flags().Changed("summary") {
		fields["summary"] = flagEditSummary
	}
	if cmd.Flags().Changed("description") {
		fields["description"] = flagEditDesc
	}
	if cmd.Flags().Changed("priority") {
		fields["priority"] = flagEditPriority
	}
	if cmd.Flags().Changed("assignee") {
		fields["assignee"] = flagEditAssignee
	}
	if cmd.Flags().Changed("labels") {
		fields["labels"] = strings.Split(flagEditLabels, ",")
	}

	if len(fields) == 0 {
		return fmt.Errorf("no fields to update — pass at least one flag")
	}

	if err := client.EditIssue(issueKey, fields); err != nil {
		return err
	}

	pterm.FgGreen.Printfln("Updated %s", issueKey)
	return nil
}
