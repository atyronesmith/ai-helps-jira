// Package nlfeedback stores examples collected by the NL-to-JQL trainer.
package nlfeedback

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/google/uuid"
)

const CurrentSchemaVersion = 2

// Verdict describes how a generated query was judged.
type Verdict string

const (
	VerdictGood      Verdict = "good"
	VerdictCorrected Verdict = "corrected"
	VerdictRejected  Verdict = "rejected"
	VerdictSkipped   Verdict = "skipped"
)

// Record is one trainer example. Its explicit fields intentionally exclude
// credentials and other authentication material.
type Record struct {
	SchemaVersion              int                     `json:"schema_version"`
	ID                         string                  `json:"id"`
	RecordedAt                 time.Time               `json:"recorded_at"`
	Input                      string                  `json:"input"`
	NormalizedInput            string                  `json:"normalized_input,omitempty"`
	Route                      string                  `json:"route"`
	GeneratedJQL               string                  `json:"generated_jql,omitempty"`
	ParseError                 string                  `json:"parse_error,omitempty"`
	Validation                 string                  `json:"validation,omitempty"`
	Verdict                    Verdict                 `json:"verdict"`
	CorrectedJQL               string                  `json:"corrected_jql,omitempty"`
	Correction                 string                  `json:"correction,omitempty"`
	TranslatorVersion          string                  `json:"translator_version,omitempty"`
	DefaultProject             string                  `json:"default_project,omitempty"`
	Outcome                    string                  `json:"outcome,omitempty"`
	ErrorCategory              string                  `json:"error_category,omitempty"`
	Provider                   string                  `json:"provider,omitempty"`
	Model                      string                  `json:"model,omitempty"`
	EffectiveUser              string                  `json:"effective_user,omitempty"`
	DurationNanos              int64                   `json:"duration_nanos,omitempty"`
	ProviderCalls              int                     `json:"provider_calls,omitempty"`
	Mode                       string                  `json:"mode,omitempty"`
	MetadataFetchedAt          time.Time               `json:"metadata_fetched_at,omitempty"`
	MetadataApplicabilityKnown bool                    `json:"metadata_applicability_known,omitempty"`
	MetadataStale              bool                    `json:"metadata_stale,omitempty"`
	Fixture                    *llm.TranslationFixture `json:"fixture,omitempty"`
	Replay                     *ReplaySpec             `json:"replay,omitempty"`
}

// Summary contains corpus size and counts grouped by route and verdict.
type Summary struct {
	Total                  int             `json:"total"`
	ByRoute                map[string]int  `json:"by_route"`
	ByVerdict              map[Verdict]int `json:"by_verdict"`
	Failures               int             `json:"failures"`
	AcceptedSuccesses      int             `json:"accepted_successes"`
	DeterministicSuccesses int             `json:"deterministic_successes"`
	ProviderCalls          int             `json:"provider_calls"`
	TotalDurationNanos     int64           `json:"total_duration_nanos"`
	AcceptanceRate         float64         `json:"acceptance_rate"`
}

// DefaultPath returns the default location for the feedback JSONL corpus.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".jira-cli", "nl2jql-feedback.jsonl")
	}
	return filepath.Join(home, ".jira-cli", "nl2jql-feedback.jsonl")
}

// Append validates and appends one complete JSON line, filling schema, ID,
// and timestamp defaults when absent.
func Append(path string, record Record) error {
	if path == "" {
		return errors.New("feedback path is empty")
	}
	if err := CheckPrivatePath(path); err != nil {
		return err
	}
	applyDefaults(&record)
	if err := validate(record); err != nil {
		return err
	}

	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal feedback record: %w", err)
	}
	line = append(line, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create feedback directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open feedback corpus: %w", err)
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("set feedback corpus permissions: %w", err)
	}
	n, err := f.Write(line)
	if err != nil {
		return fmt.Errorf("append feedback record: %w", err)
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return nil
}

// Read returns every record in a JSONL corpus. A missing or empty file is an
// empty corpus. Blank lines are ignored; malformed nonblank lines report their
// one-based line number.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open feedback corpus: %w", err)
	}
	defer f.Close()

	var records []Record
	positions := map[string]int{}
	scanner := bufio.NewScanner(f)
	// A trainer record can contain a sizable natural-language prompt.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("decode feedback corpus line %d: %w", lineNumber, err)
		}
		if err := validate(record); err != nil {
			return nil, fmt.Errorf("invalid feedback corpus line %d: %w", lineNumber, err)
		}
		if index, exists := positions[record.ID]; exists {
			records[index] = record
		} else {
			positions[record.ID] = len(records)
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read feedback corpus: %w", err)
	}
	return records, nil
}

// Summarize returns counts for the supplied records.
func Summarize(records []Record) Summary {
	records = latestSnapshots(records)
	summary := Summary{
		Total:     len(records),
		ByRoute:   make(map[string]int),
		ByVerdict: make(map[Verdict]int),
	}
	for _, record := range records {
		outcome := record.Outcome
		if record.SchemaVersion == 1 && outcome == "" {
			outcome = "success"
		}
		summary.ByRoute[record.Route]++
		summary.ByVerdict[record.Verdict]++
		if outcome == "failure" {
			summary.Failures++
		}
		if outcome == "success" && record.Verdict == VerdictGood {
			summary.AcceptedSuccesses++
		}
		if outcome == "success" && record.Route == "fast_path" {
			summary.DeterministicSuccesses++
		}
		summary.ProviderCalls += record.ProviderCalls
		summary.TotalDurationNanos += record.DurationNanos
	}
	if summary.Total > 0 {
		summary.AcceptanceRate = float64(summary.AcceptedSuccesses) / float64(summary.Total)
	}
	return summary
}

func latestSnapshots(records []Record) []Record {
	positions := map[string]int{}
	result := make([]Record, 0, len(records))
	for _, record := range records {
		if record.ID == "" {
			result = append(result, record)
			continue
		}
		if index, exists := positions[record.ID]; exists {
			result[index] = record
		} else {
			positions[record.ID] = len(result)
			result = append(result, record)
		}
	}
	return result
}

func applyDefaults(record *Record) {
	if record.SchemaVersion == 0 {
		record.SchemaVersion = CurrentSchemaVersion
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = time.Now().UTC()
	}
}

func validate(record Record) error {
	if record.SchemaVersion != 1 && record.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema version %d", record.SchemaVersion)
	}
	if record.ID == "" {
		return errors.New("record ID is required")
	}
	if record.RecordedAt.IsZero() {
		return errors.New("recorded_at is required")
	}
	if strings.TrimSpace(record.Input) == "" {
		return errors.New("input is required")
	}
	if strings.TrimSpace(record.Route) == "" {
		return errors.New("route is required")
	}
	if !validVerdict(record.Verdict) {
		return fmt.Errorf("unknown verdict %q", record.Verdict)
	}
	if record.SchemaVersion == CurrentSchemaVersion && record.Validation != "" && record.Validation != "valid" && record.Validation != "invalid" && record.Validation != "not_generated" {
		return errors.New("validation must be valid, invalid, or not_generated")
	}
	if record.Outcome != "" && record.Outcome != "success" && record.Outcome != "failure" {
		return errors.New("outcome must be success or failure")
	}
	if record.ErrorCategory != "" && record.ErrorCategory != "unsupported" && record.ErrorCategory != "invalid_output" && record.ErrorCategory != "authentication" && record.ErrorCategory != "connectivity" && record.ErrorCategory != "timeout" && record.ErrorCategory != "cancellation" && record.ErrorCategory != "unknown" {
		return errors.New("invalid error category")
	}
	if record.Outcome == "failure" && record.Verdict == VerdictGood {
		return errors.New("failed translations cannot be marked good")
	}
	if record.SchemaVersion == CurrentSchemaVersion && (record.ProviderCalls < 0 || record.DurationNanos < 0) {
		return errors.New("provider calls and duration cannot be negative")
	}
	if record.Verdict == VerdictCorrected && strings.TrimSpace(record.CorrectedJQL) == "" && strings.TrimSpace(record.Correction) == "" {
		return errors.New("corrected verdict requires corrected_jql or correction")
	}
	return nil
}

func validVerdict(verdict Verdict) bool {
	switch verdict {
	case VerdictGood, VerdictCorrected, VerdictRejected, VerdictSkipped:
		return true
	default:
		return false
	}
}
