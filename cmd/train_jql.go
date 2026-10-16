package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nlfeedback"
)

var flagFeedbackFile string

var trainJQLCmd = &cobra.Command{
	Use:   "train-jql",
	Short: "Collect feedback on natural-language JQL translations",
	Long:  "Interactively review generated JQL and save examples for translator evaluation.",
	Args:  cobra.NoArgs,
	RunE:  runTrainJQL,
}

var trainJQLReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Summarize the JQL feedback corpus",
	Args:  cobra.NoArgs,
	RunE:  runTrainJQLReport,
}

func init() {
	trainJQLCmd.PersistentFlags().StringVar(&flagFeedbackFile, "feedback-file", "", "Feedback JSONL path (default ~/.jira-cli/nl2jql-feedback.jsonl).")
	trainJQLCmd.AddCommand(trainJQLReportCmd)
	rootCmd.AddCommand(trainJQLCmd)
}

func feedbackPath() string {
	if flagFeedbackFile != "" {
		return flagFeedbackFile
	}
	return nlfeedback.DefaultPath()
}

func runTrainJQL(cmd *cobra.Command, _ []string) error {
	setupLogging()
	cfg, err := config.LoadTranslation(flagUser, flagProject)
	if err != nil {
		return err
	}
	var db *cache.Cache
	if c, openErr := cache.Open(); openErr == nil {
		db = c
		defer c.Close()
	}
	translate := func(input string) (*llm.QueryResult, error) {
		projects := cachedProjectList(db)
		return queryTranslator(context.Background(), cfg, input, projects)
	}
	return runInteractiveTrainer(cmd.InOrStdin(), cmd.OutOrStdout(), feedbackPath(), cfg.JiraProject, binaryVersion, translate)
}

func runTrainJQLReport(cmd *cobra.Command, _ []string) error {
	records, err := nlfeedback.Read(feedbackPath())
	if err != nil {
		return err
	}
	summary := nlfeedback.Summarize(records)
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Feedback records: %d\n", summary.Total)
	fmt.Fprintln(w, "By route:")
	if len(summary.ByRoute) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	routes := make([]string, 0, len(summary.ByRoute))
	for key := range summary.ByRoute {
		routes = append(routes, key)
	}
	sort.Strings(routes)
	for _, key := range routes {
		n := summary.ByRoute[key]
		fmt.Fprintf(w, "  %s: %d\n", key, n)
	}
	fmt.Fprintln(w, "By verdict:")
	for _, verdict := range []nlfeedback.Verdict{nlfeedback.VerdictGood, nlfeedback.VerdictCorrected, nlfeedback.VerdictRejected, nlfeedback.VerdictSkipped} {
		fmt.Fprintf(w, "  %s: %d\n", verdict, summary.ByVerdict[verdict])
	}
	return nil
}

type trainerTranslator func(input string) (*llm.QueryResult, error)

func runInteractiveTrainer(in io.Reader, out io.Writer, path, defaultProject, version string, translate trainerTranslator) error {
	if translate == nil {
		return errors.New("translator is required")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	writePrompt := func(prompt string) (string, bool) {
		fmt.Fprint(out, prompt)
		if !scanner.Scan() {
			return "", false
		}
		return strings.TrimSpace(scanner.Text()), true
	}
queryLoop:
	for {
		input, ok := writePrompt("Natural-language query (or 'quit'): ")
		if !ok {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil
		}
		if strings.EqualFold(input, "quit") || strings.EqualFold(input, "q") {
			return nil
		}
		if input == "" {
			continue
		}
		result, err := translate(input)
		if err != nil {
			fmt.Fprintln(out, "Translation failed.")
			continue
		}
		if result == nil {
			fmt.Fprintln(out, "Translation returned no result.")
			continue
		}
		printTranslation(out, defaultProject, result)
		verdict := nlfeedback.Verdict("")
		var correctedJQL, correction string
	reviewLoop:
		for {
			action, ok := writePrompt("Action [good/corrected/correction/reject/skip/quit]: ")
			if !ok {
				if err := scanner.Err(); err != nil {
					return err
				}
				return nil
			}
			switch strings.ToLower(action) {
			case "good", "g":
				verdict = nlfeedback.VerdictGood
			case "corrected", "c":
				correctedJQL, ok = writePrompt("Corrected JQL: ")
				if !ok {
					if err := scanner.Err(); err != nil {
						return err
					}
					return nil
				}
				if validationErr := jql.Validate(correctedJQL, jql.Options{DefaultProject: defaultProject}); validationErr != nil {
					fmt.Fprintf(out, "Corrected JQL is invalid: %v\n", validationErr)
					correctedJQL = ""
					continue
				}
				correction, ok = writePrompt("Optional note (blank for none): ")
				if !ok {
					if err := scanner.Err(); err != nil {
						return err
					}
					return nil
				}
				verdict = nlfeedback.VerdictCorrected
			case "correction", "note", "n":
				correctedJQL = ""
				correction, ok = writePrompt("Intended meaning / correction: ")
				if !ok {
					if err := scanner.Err(); err != nil {
						return err
					}
					return nil
				}
				if correction == "" {
					fmt.Fprintln(out, "Correction cannot be empty.")
					continue
				}
				verdict = nlfeedback.VerdictCorrected
			case "reject", "rejected", "r":
				verdict = nlfeedback.VerdictRejected
			case "skip", "s":
				fmt.Fprintln(out, "Skipped.")
				continue queryLoop
			case "quit", "q":
				return nil
			default:
				fmt.Fprintln(out, "Unknown action. Choose good, corrected, correction, reject, skip, or quit.")
				continue
			}
			break reviewLoop
		}

		validation := "valid"
		if validationErr := jql.Validate(result.JQL, jql.Options{DefaultProject: defaultProject}); validationErr != nil {
			validation = validationErr.Error()
		}
		record := nlfeedback.Record{
			Input: input, NormalizedInput: result.NormalizedInput, Route: result.Route,
			GeneratedJQL: result.JQL, ParseError: result.ParseError, Validation: validation, Verdict: verdict,
			CorrectedJQL: correctedJQL, Correction: correction,
			TranslatorVersion: version, DefaultProject: defaultProject,
		}
		if err := nlfeedback.Append(path, record); err != nil {
			return err
		}
		fmt.Fprintln(out, "Recorded.")
	}
}
