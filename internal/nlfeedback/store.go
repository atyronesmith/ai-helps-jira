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

	"github.com/google/uuid"
)

const CurrentSchemaVersion = 1

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
	SchemaVersion     int       `json:"schema_version"`
	ID                string    `json:"id"`
	RecordedAt        time.Time `json:"recorded_at"`
	Input             string    `json:"input"`
	NormalizedInput   string    `json:"normalized_input,omitempty"`
	Route             string    `json:"route"`
	GeneratedJQL      string    `json:"generated_jql,omitempty"`
	ParseError        string    `json:"parse_error,omitempty"`
	Validation        string    `json:"validation,omitempty"`
	Verdict           Verdict   `json:"verdict"`
	CorrectedJQL      string    `json:"corrected_jql,omitempty"`
	Correction        string    `json:"correction,omitempty"`
	TranslatorVersion string    `json:"translator_version,omitempty"`
	DefaultProject    string    `json:"default_project,omitempty"`
}

// Summary contains corpus size and counts grouped by route and verdict.
type Summary struct {
	Total     int             `json:"total"`
	ByRoute   map[string]int  `json:"by_route"`
	ByVerdict map[Verdict]int `json:"by_verdict"`
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
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read feedback corpus: %w", err)
	}
	return records, nil
}

// Summarize returns counts for the supplied records.
func Summarize(records []Record) Summary {
	summary := Summary{
		Total:     len(records),
		ByRoute:   make(map[string]int),
		ByVerdict: make(map[Verdict]int),
	}
	for _, record := range records {
		summary.ByRoute[record.Route]++
		summary.ByVerdict[record.Verdict]++
	}
	return summary
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
	if record.SchemaVersion != CurrentSchemaVersion {
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
