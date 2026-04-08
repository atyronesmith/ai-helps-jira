package cmd

import (
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var attachFileCmd = &cobra.Command{
	Use:   "attach-file [issue-key] [file-path]",
	Short: "Upload a file attachment to a JIRA issue",
	Args:  cobra.ExactArgs(2),
	RunE:  runAttachFile,
}

func init() {
	rootCmd.AddCommand(attachFileCmd)
}

func runAttachFile(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadJIRAOnly(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	filename, err := client.AttachFile(args[0], args[1])
	if err != nil {
		return err
	}

	pterm.FgGreen.Printfln("Attached %s to %s", filename, args[0])
	return nil
}
