package cmd

import (
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var (
	flagLinkInward  string
	flagLinkOutward string
	flagLinkType    string
)

var linkIssuesCmd = &cobra.Command{
	Use:   "link-issues",
	Short: "Create a link between two JIRA issues",
	Long: `Create a link between two JIRA issues.

Examples:
  jira-cli link-issues --inward PROJ-123 --outward PROJ-456 --type Blocks
  jira-cli link-issues --inward PROJ-100 --outward PROJ-200 --type "is caused by"`,
	RunE: runLinkIssues,
}

func init() {
	linkIssuesCmd.Flags().StringVar(&flagLinkInward, "inward", "", "Inward issue key (required).")
	linkIssuesCmd.Flags().StringVar(&flagLinkOutward, "outward", "", "Outward issue key (required).")
	linkIssuesCmd.Flags().StringVar(&flagLinkType, "type", "", "Link type name, e.g. Blocks, Relates (required).")
	_ = linkIssuesCmd.MarkFlagRequired("inward")
	_ = linkIssuesCmd.MarkFlagRequired("outward")
	_ = linkIssuesCmd.MarkFlagRequired("type")
	rootCmd.AddCommand(linkIssuesCmd)
}

func runLinkIssues(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	if err := client.LinkIssues(flagLinkInward, flagLinkOutward, flagLinkType); err != nil {
		return err
	}

	pterm.FgGreen.Printfln("Linked %s → %s (%s)", flagLinkInward, flagLinkOutward, flagLinkType)
	return nil
}
