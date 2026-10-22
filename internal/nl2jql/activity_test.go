package nl2jql

import (
	"errors"
	"testing"
	"time"
)

func TestParseActivityRequestSupported(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, input, start, end, project string
		inferred                         bool
	}{
		{
			name:  "ISO dates and project qualifier",
			input: "  Which Jira issues did I work on between 2026-10-12 and 2026-10-16 in project PROJ?  ",
			start: "2026-10-12", end: "2026-10-16", project: "PROJ",
		},
		{
			name:  "abbreviated month and inferred year",
			input: "Show me tickets I worked on during Oct 12 - Oct 16 for TEST?",
			start: "2026-10-12", end: "2026-10-16", project: "TEST", inferred: true,
		},
		{
			name:  "full month and explicit year",
			input: "my work activity from October 12, 2026 to October 16, 2026",
			start: "2026-10-12", end: "2026-10-16",
		},
		{
			name:  "single day range",
			input: "did I work on during 2026-10-20 and 2026-10-20",
			start: "2026-10-20", end: "2026-10-20",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, recognized, err := ParseActivityRequest(tc.input, now)
			if err != nil || !recognized {
				t.Fatalf("ParseActivityRequest recognized=%v err=%v", recognized, err)
			}
			if got.Input != tc.input || got.StartDate != tc.start || got.EndDate != tc.end || got.Project != tc.project || got.YearInferred != tc.inferred {
				t.Fatalf("request = %+v", got)
			}
		})
	}
}

func TestParseActivityRequestRecognitionAndClarification(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		input      string
		recognized bool
	}{
		{name: "recommendation is not activity", input: "Which issues should I work on next?"},
		{name: "quoted phrase is literal", input: `find issues that contain the words "I worked on during Oct 12 and Oct 16"`},
		{name: "past personal phrase without range", input: "I worked on some issues", recognized: true},
		{name: "other user", input: "What did Alice work on during Oct 12 and Oct 16?", recognized: true},
		{name: "unsupported added constraint", input: "I worked on during Oct 12 and Oct 16 that are open", recognized: true},
		{name: "unsupported owner constraint", input: "I worked on during Oct 12 and Oct 16 assigned to Alice", recognized: true},
		{name: "unsupported timestamp constraint", input: "I worked on during Oct 12 and Oct 16 updated yesterday", recognized: true},
		{name: "unsupported other-user past phrase", input: "Alice worked on during Oct 12 and Oct 16", recognized: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, recognized, err := ParseActivityRequest(tc.input, now)
			if recognized != tc.recognized {
				t.Fatalf("recognized = %v, want %v (err=%v)", recognized, tc.recognized, err)
			}
			if tc.recognized && !errors.Is(err, ErrAmbiguousIntent) {
				t.Fatalf("error = %v, want ErrAmbiguousIntent", err)
			}
			if !tc.recognized && err != nil {
				t.Fatalf("unrecognized request returned error: %v", err)
			}
		})
	}
}

func TestParseActivityRequestDateValidation(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	for _, input := range []string{
		"I worked on during 2025-02-29 and 2025-03-01",
		"I worked on during 2026-10-16 and 2026-10-12",
		"I worked on during Dec 30 and Jan 2",
		"I worked on during Oct 12, 2026 and Oct 16",
		"I worked on during 9999-12-31 and 9999-12-31",
	} {
		_, recognized, err := ParseActivityRequest(input, now)
		if !recognized || !errors.Is(err, ErrAmbiguousIntent) {
			t.Errorf("ParseActivityRequest(%q) recognized=%v err=%v; want recognized ambiguity", input, recognized, err)
		}
	}

	got, recognized, err := ParseActivityRequest("I worked on during Dec 30, 2026 and Jan 2, 2027", now)
	if err != nil || !recognized || got.StartDate != "2026-12-30" || got.EndDate != "2027-01-02" || got.YearInferred {
		t.Fatalf("explicit cross-year range = %+v, recognized=%v, err=%v", got, recognized, err)
	}

	got, recognized, err = ParseActivityRequest("I worked on during Feb 29, 2024 and Feb 29, 2024", now)
	if err != nil || !recognized || got.StartDate != "2024-02-29" || got.EndDate != "2024-02-29" {
		t.Fatalf("leap-day range = %+v, recognized=%v, err=%v", got, recognized, err)
	}
}
