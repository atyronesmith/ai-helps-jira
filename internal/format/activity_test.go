package format

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestRenderActivityCompleteEmptyIsExact(t *testing.T) {
	result := jira.ActivityResult{
		Project: "PROJ", AccountID: "acct-1", TimeZone: "UTC",
		StartDate: "2026-10-01", EndDate: "2026-10-07",
		Candidates: 12, Scanned: 12, CandidateComplete: true, Complete: true,
	}
	got := RenderActivity(result, 0)
	for _, want := range []string{
		"Matching issues: 0 (complete; exact count)",
		"No matching issues were found in the completed scan.",
		"Date range: 2026-10-01 through 2026-10-07 (inclusive)",
		"Candidates: 12; scanned: 12",
		"comments use their creation time",
		"worklogs use their started time even if logged later",
		"status changes use the history creation time",
		"issues currently in the selected project",
		"Issues moved out of the project are outside this result",
		"Only records currently visible",
		"moved-out records are excluded",
		"not an atomic snapshot",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
}

func TestRenderActivityPartialEmptyHasUnknownTotal(t *testing.T) {
	result := jira.ActivityResult{
		Project: "PROJ", AccountID: "acct-1", TimeZone: "UTC",
		StartDate: "2026-10-01", EndDate: "2026-10-07",
		Candidates: 5, Scanned: 2, CandidateComplete: false, Complete: false,
		Problems: []string{"candidate search stopped early"},
	}
	got := RenderActivity(result, 10)
	for _, want := range []string{
		"Matching issues: 0 observed; partial scan; total unknown",
		"**PARTIAL:**",
		"candidate search stopped early",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "No matching issues were found") || strings.Contains(got, "No activity found") {
		t.Fatalf("partial empty report claimed no matches/activity:\n%s", got)
	}
}

func TestRenderActivityPartialPositiveDoesNotClaimExactTotal(t *testing.T) {
	result := jira.ActivityResult{
		Project: "PROJ", AccountID: "acct-1", TimeZone: "UTC",
		CandidateComplete: true, Complete: false,
		Matches: []jira.ActivityMatch{{Issue: jira.Issue{Key: "PROJ-1", Summary: "Observed"}}},
	}
	got := RenderActivity(result, 10)
	if !strings.Contains(got, "Matching issues: 1 observed; partial scan; total unknown") {
		t.Fatalf("partial result did not disclose unknown total:\n%s", got)
	}
	if strings.Contains(got, "exact count") {
		t.Fatalf("partial result claimed an exact count:\n%s", got)
	}
}

func TestRenderActivityDisplayTruncationKeepsFullCount(t *testing.T) {
	result := jira.ActivityResult{
		Project: "PROJ", AccountID: "acct-1", TimeZone: "UTC",
		CandidateComplete: true, Complete: true,
		Matches: []jira.ActivityMatch{
			{Issue: jira.Issue{Key: "PROJ-1", Summary: "One"}},
			{Issue: jira.Issue{Key: "PROJ-2", Summary: "Two"}},
			{Issue: jira.Issue{Key: "PROJ-3", Summary: "Three"}},
		},
	}
	got := RenderActivity(result, 2)
	if !strings.Contains(got, "Matching issues: 3 (complete; exact count)") ||
		!strings.Contains(got, "Displaying 2 of 3 observed matching issues") ||
		!strings.Contains(got, "1 more observed matching issues omitted") {
		t.Fatalf("display truncation lost the complete count or disclosure:\n%s", got)
	}
	if strings.Contains(got, "## PROJ-3") {
		t.Fatalf("third issue exceeded row limit:\n%s", got)
	}
}

func TestRenderActivityTimeZoneAndSafeSingleLineFormatting(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC)
	result := jira.ActivityResult{
		Project: "PROJ\n# injected", AccountID: "acct-1", TimeZone: "America/New_York",
		StartDate: "2026-10-20", EndDate: "2026-10-20",
		CandidateComplete: true, Complete: true,
		Matches: []jira.ActivityMatch{{
			Issue:  jira.Issue{Key: "PROJ-1", Summary: "bad | [summary]\n# heading"},
			Events: []jira.ActivityEvent{{Kind: jira.ActivityKindStatus, At: at, Detail: "Open -> Done | [detail]\nnext"}},
		}},
	}
	got := RenderActivity(result, 1)
	for _, want := range []string{
		`Time zone: America/New\_York`,
		"2026-10-20 08:30:00 EDT",
		`bad \| \[summary\] \# heading`,
		`Open -\> Done \| \[detail\] next`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing safely formatted %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n# injected") || strings.Contains(got, "\n# heading") || strings.Contains(got, "\nnext") {
		t.Fatalf("untrusted content injected a Markdown line:\n%s", got)
	}
}

func TestRenderActivityAssumedZoneAndEventDisplayBound(t *testing.T) {
	events := make([]jira.ActivityEvent, maxActivityEvents+2)
	for i := range events {
		events[i] = jira.ActivityEvent{Kind: "worklog", At: time.Date(2026, 10, 20, 0, i, 0, 0, time.UTC), Detail: fmt.Sprintf("event %d", i)}
	}
	result := jira.ActivityResult{
		Project: "PROJ", AccountID: "acct-1", TimeZone: "bad/zone", TimeZoneAssumed: true,
		CandidateComplete: true, Complete: true,
		Matches: []jira.ActivityMatch{{Issue: jira.Issue{Key: "PROJ-1"}, Events: events}},
	}
	got := RenderActivity(result, 1)
	if !strings.Contains(got, "Time zone: UTC (assumed fallback)") {
		t.Fatalf("fallback timezone was not identified:\n%s", got)
	}
	if !strings.Contains(got, "2 additional events omitted") {
		t.Fatalf("event display cap was not disclosed:\n%s", got)
	}
}
