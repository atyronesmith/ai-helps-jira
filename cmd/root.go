package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	flagUser    string
	flagProject string
	flagFormat  string
	flagOutfile string
	flagVerbose int
)

var rootCmd = &cobra.Command{
	Use:   "jira-cli",
	Short: "JIRA CLI - summaries, search, reports, and full CRUD",
	Long: `JIRA CLI - daily summaries, natural language search, AI-powered reports, and full CRUD operations.

Examples:
  jira-cli summary
  jira-cli query "show me all critical bugs from last week"
  jira-cli digest FEAT-123
  jira-cli enrich PROJ-123 --apply
  jira-cli weekly-status --start-date 2026-03-30 --end-date 2026-04-03
  jira-cli get-issue PROJ-123
  jira-cli create-issue --summary "Fix login bug" --type Bug
  jira-cli edit-issue PROJ-123 --priority High
  jira-cli add-comment PROJ-123 --body "Done."
  jira-cli get-transitions PROJ-123
  jira-cli transition PROJ-123 31
  jira-cli lookup-user jsmith
  jira-cli create-epic -d "Build onboarding flow"
  jira-cli summarize-comments PROJ-123
  jira-cli backlog-health
  jira-cli config`,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagUser, "user", "u", "",
		"JIRA user (user@company.com). Defaults to currentUser().")
	rootCmd.PersistentFlags().StringVarP(&flagProject, "project", "p", "",
		"JIRA project key. Overrides JIRA_PROJECT env var.")
	rootCmd.PersistentFlags().StringVarP(&flagFormat, "format", "f", "markdown",
		`Output format: markdown, slack, text, pretty.`)
	rootCmd.PersistentFlags().StringVarP(&flagOutfile, "outfile", "o", "",
		"Output file path. Defaults to {project}.md.")
	rootCmd.PersistentFlags().CountVarP(&flagVerbose, "verbose", "v",
		"Increase verbosity (-v, -vv, etc.). Level 2 shows cache diagnostics.")
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
