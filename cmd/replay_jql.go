package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/nlfeedback"
)

var replayJQLCmd = &cobra.Command{
	Use:   "replay [corpus.jsonl]",
	Short: "Replay reviewed translation fixtures locally without Jira or a live provider",
	Long: "Replay every feedback attempt with a frozen clock, metadata, and canned provider. " +
		"Uses --feedback-file when no path is supplied. Emits JSON and exits unsuccessfully " +
		"for missing fixtures, unreviewed expectations, or regressions. Corrections remain review data.",
	Args: cobra.MaximumNArgs(1),
	RunE: runReplayJQL,
}

func init() {
	trainJQLCmd.AddCommand(replayJQLCmd)
}

func runReplayJQL(cmd *cobra.Command, args []string) error {
	path := feedbackPath()
	if len(args) == 1 {
		path = args[0]
	}
	// Unlike a collection report, a missing replay corpus must not look green.
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("open replay corpus: %w", err)
	}
	records, err := nlfeedback.Read(path)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("replay corpus contains no attempts")
	}
	report := nlfeedback.Replay(commandContext(cmd), records)
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if report.Passed != report.Total {
		return fmt.Errorf("corpus replay failed: %d/%d expectations passed (%d unscored)", report.Passed, report.Total, report.Unscored)
	}
	return nil
}
