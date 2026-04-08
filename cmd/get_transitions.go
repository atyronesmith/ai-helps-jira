package cmd

import (
	"fmt"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var getTransitionsCmd = &cobra.Command{
	Use:   "get-transitions [issue-key]",
	Short: "List available workflow transitions for an issue",
	Args:  cobra.ExactArgs(1),
	RunE:  runGetTransitions,
}

func init() {
	rootCmd.AddCommand(getTransitionsCmd)
}

func runGetTransitions(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	transitions, err := client.GetTransitions(args[0])
	if err != nil {
		return err
	}

	if len(transitions) == 0 {
		pterm.FgYellow.Println("No transitions available.")
		return nil
	}

	pterm.FgLightWhite.Printfln("Transitions for %s:", args[0])
	for _, t := range transitions {
		fmt.Printf("  %s — %s (target: %s)\n", t.ID, t.Name, t.To)
	}
	return nil
}
