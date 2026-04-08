package cmd

import (
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var transitionCmd = &cobra.Command{
	Use:   "transition [issue-key] [transition-id]",
	Short: "Change issue workflow status",
	Long: `Transition an issue to a new workflow status.
Use get-transitions to find available transition IDs.

Examples:
  jira-cli get-transitions PROJ-123
  jira-cli transition PROJ-123 31`,
	Args: cobra.ExactArgs(2),
	RunE: runTransition,
}

func init() {
	rootCmd.AddCommand(transitionCmd)
}

func runTransition(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	if err := client.TransitionIssue(args[0], args[1]); err != nil {
		return err
	}

	pterm.FgGreen.Printfln("Transitioned %s", args[0])
	return nil
}
