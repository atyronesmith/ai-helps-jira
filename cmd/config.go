package cmd

import (
	"fmt"
	"os"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show active configuration and cache statistics",
	RunE:  runConfig,
}

func init() {
	rootCmd.AddCommand(configCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	// Load config (best-effort — show what we can even if some vars are missing)
	cfg, cfgErr := config.LoadJIRAOnly(flagUser, flagProject)

	fmt.Println()
	pterm.DefaultSection.Println("JIRA")
	if cfg != nil {
		fmt.Printf("  Server:   %s\n", cfg.JiraServer)
		fmt.Printf("  Email:    %s\n", cfg.JiraEmail)
		fmt.Printf("  Project:  %s\n", cfg.JiraProject)
		if cfg.JiraUser != "" {
			fmt.Printf("  User:     %s\n", cfg.JiraUser)
		} else {
			fmt.Printf("  User:     currentUser()\n")
		}
	} else {
		pterm.FgYellow.Printfln("  %v", cfgErr)
	}

	pterm.DefaultSection.Println("LLM Provider")
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		if os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID") != "" {
			provider = "vertex"
		} else {
			provider = "(not configured)"
		}
	}
	fmt.Printf("  Provider: %s\n", provider)

	model := os.Getenv("LLM_MODEL")
	if model == "" && (provider == "vertex" || provider == "(not configured)") {
		model = "claude-sonnet-4-6 (default)"
	}
	if model != "" {
		fmt.Printf("  Model:    %s\n", model)
	}

	switch provider {
	case "vertex":
		fmt.Printf("  Project:  %s\n", os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID"))
		fmt.Printf("  Region:   %s\n", os.Getenv("CLOUD_ML_REGION"))
	case "openai":
		fmt.Printf("  Base URL: %s\n", os.Getenv("LLM_BASE_URL"))
	case "ollama":
		baseURL := os.Getenv("OLLAMA_BASE_URL")
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		fmt.Printf("  Base URL: %s\n", baseURL)
	}

	pterm.DefaultSection.Println("Cache")
	db, err := cache.Open()
	if err != nil {
		pterm.FgYellow.Printfln("  Could not open cache: %v", err)
	} else {
		defer db.Close()
		fmt.Printf("  Path:     %s\n", db.Path())
		stats := db.Stats()
		fmt.Println()
		data := [][]string{
			{"Table", "Rows"},
			{"issues", fmt.Sprintf("%d", stats.Issues)},
			{"issue_boards", fmt.Sprintf("%d", stats.IssueBoards)},
			{"issue_details", fmt.Sprintf("%d", stats.IssueDetails)},
			{"comments", fmt.Sprintf("%d", stats.Comments)},
			{"issue_links", fmt.Sprintf("%d", stats.IssueLinks)},
			{"weekly_cache", fmt.Sprintf("%d", stats.WeeklyCache)},
			{"digest_log", fmt.Sprintf("%d", stats.DigestLog)},
			{"fetch_log", fmt.Sprintf("%d", stats.FetchLog)},
		}
		table, _ := pterm.DefaultTable.WithHasHeader(true).WithData(data).Srender()
		fmt.Println(table)
	}

	fmt.Println()
	return nil
}
