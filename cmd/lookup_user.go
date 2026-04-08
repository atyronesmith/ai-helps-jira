package cmd

import (
	"fmt"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var lookupUserCmd = &cobra.Command{
	Use:   "lookup-user [query]",
	Short: "Search for JIRA users by name or email",
	Args:  cobra.ExactArgs(1),
	RunE:  runLookupUser,
}

func init() {
	rootCmd.AddCommand(lookupUserCmd)
}

func runLookupUser(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	users, err := client.SearchUsers(args[0])
	if err != nil {
		return err
	}

	if len(users) == 0 {
		pterm.FgYellow.Println("No users found.")
		return nil
	}

	pterm.FgLightWhite.Printfln("Users matching %q:", args[0])
	for _, u := range users {
		fmt.Printf("  %s — %s (%s)\n", u.DisplayName, u.EmailAddress, u.AccountID)
	}
	return nil
}
