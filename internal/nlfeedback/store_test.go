package nlfeedback

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendReadRoundTripAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "feedback.jsonl")
	input := Record{
		Input:           "find my open issues",
		NormalizedInput: "find my open issues",
		Route:           "deterministic",
		GeneratedJQL:    "assignee = currentUser() AND statusCategory != Done",
		ParseError:      "unexpected token near 'open'",
		Verdict:         VerdictGood,
	}
	if err := Append(path, input); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read() returned %d records, want 1", len(got))
	}
	record := got[0]
	if record.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", record.SchemaVersion, CurrentSchemaVersion)
	}
	if record.ID == "" {
		t.Error("ID default was not generated")
	}
	if record.RecordedAt.IsZero() || record.RecordedAt.Location() != time.UTC {
		t.Errorf("RecordedAt = %v, want nonzero UTC time", record.RecordedAt)
	}
	if record.Input != input.Input || record.GeneratedJQL != input.GeneratedJQL || record.ParseError != input.ParseError || record.Verdict != input.Verdict {
		t.Errorf("round trip mismatch: got %+v", record)
	}
}

func TestAppendPreservesExistingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	first := validRecord("first")
	second := validRecord("second")
	if err := Append(path, first); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Input != "first" || got[1].Input != "second" {
		t.Fatalf("records not preserved in order: %+v", got)
	}
}

func TestAppendCreatesPrivateDirectoryAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "nested", "feedback.jsonl")
	if err := Append(path, validRecord("private")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Dir(path), path} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 && target == filepath.Dir(path) {
			t.Errorf("directory permissions = %04o, want 0700", got)
		}
		if got := info.Mode().Perm(); got != 0o600 && target == path {
			t.Errorf("file permissions = %04o, want 0600", got)
		}
	}
}

func TestAppendRestrictsExistingFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	if err := os.WriteFile(path, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, validRecord("next")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file permissions = %04o, want 0600", got)
	}
}

func TestAppendRejectsInvalidRecords(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Record)
	}{
		{name: "missing input", modify: func(r *Record) { r.Input = " " }},
		{name: "missing route", modify: func(r *Record) { r.Route = "" }},
		{name: "unknown verdict", modify: func(r *Record) { r.Verdict = Verdict("unsure") }},
		{name: "unsupported schema", modify: func(r *Record) { r.SchemaVersion = CurrentSchemaVersion + 1 }},
		{name: "corrected without correction", modify: func(r *Record) { r.Verdict = VerdictCorrected }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validRecord("input")
			test.modify(&record)
			if err := Append(filepath.Join(t.TempDir(), "feedback.jsonl"), record); err == nil {
				t.Fatal("Append() succeeded, want validation error")
			}
		})
	}
}

func TestCorrectedVerdictAcceptsNaturalLanguageCorrection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	record := validRecord("find my open issues")
	record.Verdict = VerdictCorrected
	record.Correction = "Include only issues assigned to me."
	if err := Append(path, record); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(got) != 1 || got[0].Correction != record.Correction {
		t.Fatalf("natural-language correction not retained: %+v", got)
	}
}

func TestReadMalformedLineReportsLineNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	content := "{\"input\":\"one\",\"route\":\"parser\",\"verdict\":\"good\",\"schema_version\":1,\"id\":\"x\",\"recorded_at\":\"2026-01-01T00:00:00Z\"}\nnot-json\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Read(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("Read() error = %v, want line 2 error", err)
	}
}

func TestReadMissingAndEmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	missing, err := Read(filepath.Join(dir, "missing.jsonl"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("Read(missing) = %v, %v; want empty corpus, nil", missing, err)
	}
	emptyPath := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	empty, err := Read(emptyPath)
	if err != nil || len(empty) != 0 {
		t.Fatalf("Read(empty) = %v, %v; want empty corpus, nil", empty, err)
	}
}

func TestSummarizeCountsRoutesAndVerdicts(t *testing.T) {
	records := []Record{
		{Route: "parser", Verdict: VerdictGood},
		{Route: "parser", Verdict: VerdictCorrected},
		{Route: "fallback", Verdict: VerdictRejected},
	}
	got := Summarize(records)
	if got.Total != 3 || got.ByRoute["parser"] != 2 || got.ByRoute["fallback"] != 1 {
		t.Errorf("route summary = %+v", got)
	}
	if got.ByVerdict[VerdictGood] != 1 || got.ByVerdict[VerdictCorrected] != 1 || got.ByVerdict[VerdictRejected] != 1 {
		t.Errorf("verdict summary = %+v", got)
	}
}

func TestDefaultPath(t *testing.T) {
	if got := DefaultPath(); !strings.HasSuffix(got, filepath.Join(".jira-cli", "nl2jql-feedback.jsonl")) {
		t.Errorf("DefaultPath() = %q", got)
	}
}

func validRecord(input string) Record {
	return Record{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "test-id-" + input,
		RecordedAt:    time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		Input:         input,
		Route:         "parser",
		Verdict:       VerdictGood,
	}
}
