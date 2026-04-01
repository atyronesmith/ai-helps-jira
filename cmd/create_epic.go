package cmd

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

var flagDescription string
var flagNoInteractive bool

var createEpicCmd = &cobra.Command{
	Use:   "create-epic",
	Short: "Create a new EPIC with LLM-assisted content generation",
	RunE:  runCreateEpic,
}

func init() {
	createEpicCmd.Flags().StringVarP(&flagDescription, "description", "d", "",
		"One-line description of what the EPIC should accomplish.")
	createEpicCmd.Flags().BoolVar(&flagNoInteractive, "no-interactive", false,
		"Skip review and create EPIC directly.")
	rootCmd.AddCommand(createEpicCmd)
}

func runCreateEpic(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagUser, flagProject)
	if err != nil {
		return err
	}
	setupLogging()

	// Prompt for description if not provided
	description := flagDescription
	if description == "" {
		fmt.Print("Brief EPIC description: ")
		reader := bufio.NewReader(os.Stdin)
		description, _ = reader.ReadString('\n')
		description = strings.TrimSpace(description)
		if description == "" {
			return fmt.Errorf("description is required")
		}
	}

	slog.Info("starting create-epic", "description", description)

	spinner := format.StatusPrinter("Generating EPIC content with Claude...")
	epic, err := llm.GenerateEpicContent(cfg, description)
	spinner.Stop()
	if err != nil {
		return err
	}
	slog.Info("EPIC content generated", "summary", epic.Summary)

	if !flagNoInteractive {
		format.DisplayEpicPreview(
			epic.Summary, epic.Description,
			epic.AcceptanceCriteria, epic.Priority, epic.Labels,
		)
		if !confirm("Create this EPIC?") {
			pterm.FgYellow.Println("EPIC creation cancelled.")
			return nil
		}
	}

	pterm.FgLightWhite.Println("Connecting to JIRA...")
	client, err := jira.NewClient(cfg)
	if err != nil {
		return err
	}

	spinner = format.StatusPrinter("Creating EPIC in JIRA...")
	fullDescription := llm.BuildDescription(epic)
	issue, err := client.CreateEpic(epic.Summary, fullDescription, epic.Priority, epic.Labels)
	spinner.Stop()
	if err != nil {
		return err
	}

	pterm.FgGreen.Printfln("\nEPIC created: %s - %s/browse/%s",
		issue.Key, cfg.JiraServer, issue.Key)
	return nil
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}
