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
	flagIssueSummary  string
	flagIssueType     string
	flagIssueDesc     string
	flagIssuePriority string
	flagIssueParent   string
	flagIssueAssignee string
	flagIssueLabels   string
)

var createIssueCmd = &cobra.Command{
	Use:   "create-issue",
	Short: "Create a JIRA issue",
	Long: `Create a JIRA issue of any type (Task, Bug, Story, Epic, Sub-task, etc.).

Examples:
  jira-cli create-issue --summary "Fix login bug" --type Bug
  jira-cli create-issue --summary "New feature" --type Story --priority High
  jira-cli create-issue --summary "Subtask" --type Sub-task --parent PROJ-123`,
	RunE: runCreateIssue,
}

func init() {
	createIssueCmd.Flags().StringVar(&flagIssueSummary, "summary", "", "Issue summary/title (required).")
	createIssueCmd.Flags().StringVar(&flagIssueType, "type", "", "Issue type: Task, Bug, Story, Epic, Sub-task (required).")
	createIssueCmd.Flags().StringVar(&flagIssueDesc, "description", "", "Issue description.")
	createIssueCmd.Flags().StringVar(&flagIssuePriority, "priority", "", "Priority: Highest, High, Medium, Low, Lowest.")
	createIssueCmd.Flags().StringVar(&flagIssueParent, "parent", "", "Parent issue key.")
	createIssueCmd.Flags().StringVar(&flagIssueAssignee, "assignee", "", "Assignee account ID.")
	createIssueCmd.Flags().StringVar(&flagIssueLabels, "labels", "", "Comma-separated labels.")
	_ = createIssueCmd.MarkFlagRequired("summary")
	_ = createIssueCmd.MarkFlagRequired("type")
	rootCmd.AddCommand(createIssueCmd)
}

func runCreateIssue(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	var labels []string
	if flagIssueLabels != "" {
		labels = strings.Split(flagIssueLabels, ",")
	}

	issue, err := client.CreateIssue(jira.CreateIssueParams{
		ProjectKey:  cfg.JiraProject,
		IssueType:   flagIssueType,
		Summary:     flagIssueSummary,
		Description: flagIssueDesc,
		Priority:    flagIssuePriority,
		Labels:      labels,
		Parent:      flagIssueParent,
		AssigneeID:  flagIssueAssignee,
	})
	if err != nil {
		return err
	}

	pterm.FgGreen.Printfln("Created %s: %s", issue.Key, flagIssueSummary)
	fmt.Printf("  %s/browse/%s\n", cfg.JiraServer, issue.Key)
	return nil
}
