package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	flagUser          string
	flagProject       string
	flagSlackMarkdown bool
	flagOutfile       string
)

var rootCmd = &cobra.Command{
	Use:   "jira-cli",
	Short: "JIRA CLI - daily summaries and LLM-assisted EPIC creation",
	Long: `JIRA CLI - daily summaries and LLM-assisted EPIC creation.

Examples:
  jira-cli summary
  jira-cli --slack-markdown summary
  jira-cli --slack-markdown -o report.md summary
  jira-cli -u user@company.com -p PROJ summary
  jira-cli create-epic -d "Build onboarding flow"`,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagUser, "user", "u", "",
		"JIRA user (user@company.com). Defaults to currentUser().")
	rootCmd.PersistentFlags().StringVarP(&flagProject, "project", "p", "",
		"JIRA project key. Overrides JIRA_PROJECT env var.")
	rootCmd.PersistentFlags().BoolVar(&flagSlackMarkdown, "slack-markdown", false,
		"Output Slack-compatible mrkdwn instead of full markdown.")
	rootCmd.PersistentFlags().StringVarP(&flagOutfile, "outfile", "o", "",
		"Output file path. Defaults to {project}.md.")
}

func SetVersionInfo(version, commit, date string) {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var loggingSetup bool

func setupLogging() {
	if loggingSetup {
		return
	}
	loggingSetup = true

	dir := filepath.Join(os.Getenv("HOME"), ".jira-cli")
	_ = os.MkdirAll(dir, 0o755)
	logFile := filepath.Join(dir, "jira-cli.log")

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot open log file: %v\n", err)
		return
	}

	handler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	slog.Info("logging initialized", "file", logFile)
}
