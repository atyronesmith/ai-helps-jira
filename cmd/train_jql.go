package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/nlfeedback"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
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
	if err := nlfeedback.CheckPrivatePath(feedbackPath()); err != nil {
		return err
	}
	setupLogging()
	cfg, err := config.LoadTranslation(flagUser, flagProject)
	if err != nil {
		return err
	}
	var db *cache.Cache
	if c, openErr := cache.OpenForConfig(cfg); openErr == nil {
		db = c
		defer c.Close()
	}
	projects := cachedProjectList(db)
	translationCtx := llm.WithTranslationMetadata(commandContext(cmd), db)
	fixture, fixtureErr := llm.CaptureTranslationFixture(translationCtx, cfg, projects)
	if fixtureErr != nil {
		return fixtureErr
	}
	translate := func(input string) (*llm.QueryResult, error) {
		fixture.Now = time.Now()
		return queryTranslator(llm.WithTranslationFixture(translationCtx, fixture), cfg, input, projects)
	}
	provider, model := trainerProviderIdentity(cfg)
	return runInteractiveTrainerWithMetadata(cmd.InOrStdin(), cmd.OutOrStdout(), feedbackPath(), cfg.JiraProject, binaryVersion, translate, trainerMetadata{provider: provider, model: model, effectiveUser: cfg.JiraUser, fixture: &fixture})
}

func trainerProviderIdentity(cfg *config.Config) (string, string) {
	provider := strings.ToLower(config.GetEnvOrSecret("LLM_PROVIDER"))
	if provider == "" {
		if cfg != nil && cfg.VertexProjectID != "" {
			provider = "vertex"
		} else {
			provider = "unknown"
		}
	}
	if provider != "vertex" && provider != "openai" && provider != "ollama" {
		provider = "unknown"
	}
	model := config.GetEnvOrSecret("LLM_MODEL")
	if model == "" && provider == "vertex" {
		model = "claude-sonnet-4-6"
	}
	if model == "" {
		model = "unknown"
	}
	return provider, model
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
	fmt.Fprintf(w, "Failures: %d\nSkips: %d\n", summary.Failures, summary.ByVerdict[nlfeedback.VerdictSkipped])
	fmt.Fprintf(w, "Accepted good successes / all attempts: %d/%d\nAcceptance rate: %.1f%%\n", summary.AcceptedSuccesses, summary.Total, summary.AcceptanceRate*100)
	denominator := summary.Total
	if denominator == 0 {
		denominator = 1
	}
	fmt.Fprintf(w, "Deterministic successful coverage / all attempts: %d/%d\nProvider calls: %d\nMean latency: %s\n", summary.DeterministicSuccesses, summary.Total, summary.ProviderCalls, time.Duration(summary.TotalDurationNanos/int64(denominator)))
	return nil
}

type trainerTranslator func(input string) (*llm.QueryResult, error)

func runInteractiveTrainer(in io.Reader, out io.Writer, path, defaultProject, version string, translate trainerTranslator) error {
	return runInteractiveTrainerWithMetadata(in, out, path, defaultProject, version, translate, trainerMetadata{})
}

type trainerMetadata struct {
	provider, model, effectiveUser string
	fixture                        *llm.TranslationFixture
}

func runInteractiveTrainerWithMetadata(in io.Reader, out io.Writer, path, defaultProject, version string, translate trainerTranslator, metadata trainerMetadata) error {
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
		return scanner.Text(), true
	}
	for {
		input, ok := writePrompt("Natural-language query (or 'quit'): ")
		if !ok {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil
		}
		if strings.EqualFold(strings.TrimSpace(input), "quit") || strings.EqualFold(strings.TrimSpace(input), "q") {
			return nil
		}
		if strings.TrimSpace(input) == "" {
			continue
		}
		started := time.Now()
		result, err := translate(input)
		duration := time.Since(started)
		record := baseFeedbackRecord(input, result, version, defaultProject, metadata)
		record.ID = uuid.NewString()
		record.Verdict, record.DurationNanos = nlfeedback.VerdictSkipped, duration.Nanoseconds()
		if result == nil || err != nil {
			record.Outcome = "failure"
			record.Validation = "not_generated"
			if result != nil {
				record.Validation = validationLabel(result.JQL, defaultProject)
			}
			record.ErrorCategory = safeErrorCategory(err)
			fmt.Fprintln(out, "Translation failed.")
		} else {
			record.Outcome, record.Validation = "success", validationLabel(result.JQL, defaultProject)
			printTranslation(out, defaultProject, result)
		}
		if e := nlfeedback.Append(path, record); e != nil {
			return e
		}
		stop, e := reviewAttempt(scanner, out, path, record, result != nil && err == nil)
		if e != nil {
			return e
		}
		if stop {
			return nil
		}
	}
}

func safeErrorCategory(err error) string {
	if err == nil {
		return "unknown"
	}
	switch translationerror.KindOf(err) {
	case translationerror.Unsupported, translationerror.InvalidOutput, translationerror.Authentication, translationerror.Connectivity, translationerror.Timeout, translationerror.Cancellation:
		return string(translationerror.KindOf(err))
	default:
		return "unknown"
	}
}

func reviewAttempt(scanner *bufio.Scanner, out io.Writer, path string, record nlfeedback.Record, success bool) (bool, error) {
	for {
		prompt := "Action [good/corrected/correction/reject/skip/quit]: "
		if !success {
			prompt = "Failure action [corrected/correction/reject/skip/quit]: "
		}
		fmt.Fprint(out, prompt)
		if !scanner.Scan() {
			return false, scanner.Err()
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "good", "g":
			if !success {
				fmt.Fprintln(out, "Failed translations cannot be marked good.")
				continue
			}
			record.Verdict = nlfeedback.VerdictGood
		case "corrected", "c":
			fmt.Fprint(out, "Corrected JQL (syntax only; never executed): ")
			if !scanner.Scan() {
				return false, scanner.Err()
			}
			corrected := scanner.Text()
			// Corrections are private review data, not executable search input.
			// Check syntax without imposing the execution boundary's project
			// scope rule or silently adding scope to the user's correction.
			if err := jql.Validate(corrected, jql.Options{}); err != nil {
				fmt.Fprintf(out, "Corrected JQL is invalid: %v\n", err)
				continue
			}
			record.Verdict, record.CorrectedJQL = nlfeedback.VerdictCorrected, corrected
			record.Correction = ""
			if err := nlfeedback.Append(path, record); err != nil {
				return false, err
			}
			fmt.Fprint(out, "Optional note (blank for none): ")
			if !scanner.Scan() {
				return false, scanner.Err()
			}
			record.Correction = scanner.Text()
		case "correction", "note", "n":
			fmt.Fprint(out, "Intended meaning / correction: ")
			if !scanner.Scan() {
				return false, scanner.Err()
			}
			note := scanner.Text()
			if strings.TrimSpace(note) == "" {
				fmt.Fprintln(out, "Correction cannot be empty.")
				continue
			}
			record.Verdict, record.Correction = nlfeedback.VerdictCorrected, note
			record.CorrectedJQL = ""
		case "reject", "rejected", "r":
			record.Verdict = nlfeedback.VerdictRejected
		case "skip", "s":
			record.Verdict = nlfeedback.VerdictSkipped
			if err := nlfeedback.Append(path, record); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "Skipped.")
			return false, nil
		case "quit", "q":
			record.Verdict = nlfeedback.VerdictSkipped
			if err := nlfeedback.Append(path, record); err != nil {
				return false, err
			}
			return true, nil
		default:
			fmt.Fprintln(out, "Unknown action.")
			continue
		}
		if err := nlfeedback.Append(path, record); err != nil {
			return false, err
		}
		fmt.Fprintln(out, "Recorded.")
		return false, nil
	}
}

func validationLabel(query, project string) string {
	if strings.TrimSpace(query) == "" {
		return "not_generated"
	}
	if jql.Validate(query, jql.Options{DefaultProject: project}) != nil {
		return "invalid"
	}
	return "valid"
}

func baseFeedbackRecord(input string, result *llm.QueryResult, version, project string, metadata trainerMetadata) nlfeedback.Record {
	r := nlfeedback.Record{Input: input, Route: "not_generated", TranslatorVersion: version, DefaultProject: project, Provider: metadata.provider, Model: metadata.model, EffectiveUser: metadata.effectiveUser, Fixture: metadata.fixture}
	if result != nil {
		r.NormalizedInput, r.Route, r.GeneratedJQL, r.ParseError, r.Mode, r.ProviderCalls = result.NormalizedInput, result.Route, result.JQL, result.ParseError, string(result.Mode), result.ProviderCalls
		if r.Route == "" {
			r.Route = "not_generated"
		}
		r.MetadataFetchedAt, r.MetadataApplicabilityKnown, r.MetadataStale = result.MetadataFetchedAt, result.MetadataApplicabilityKnown, result.MetadataStale
	}
	return r
}
