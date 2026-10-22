package format

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

const (
	defaultActivityRows = 50
	maxActivityEvents   = 20
)

// RenderActivity renders a Markdown report of visible Jira activity attributed
// to the configured account. maxRows bounds issue rows; nonpositive values use
// a conservative default.
func RenderActivity(result jira.ActivityResult, maxRows int) string {
	if maxRows <= 0 {
		maxRows = defaultActivityRows
	}
	location, zoneLabel := activityLocation(result.TimeZone, result.TimeZoneAssumed)
	complete := result.CandidateComplete && result.Complete

	var b strings.Builder
	fmt.Fprintf(&b, "# Activity report: %s\n\n", markdownSafe(result.Project))
	fmt.Fprintf(&b, "- Account ID: %s\n", markdownSafe(result.AccountID))
	fmt.Fprintf(&b, "- Date range: %s through %s (inclusive)\n", markdownSafe(result.StartDate), markdownSafe(result.EndDate))
	fmt.Fprintf(&b, "- Time zone: %s\n", markdownSafe(zoneLabel))
	b.WriteString("- Attribution: the account authored the comment, worklog, or status change; current assignee is not used as a proxy.\n")
	b.WriteString("- Event dates: comments use their creation time; worklogs use their started time even if logged later; status changes use the history creation time.\n")
	b.WriteString("- Project scope: issues currently in the selected project. Issues moved out of the project are outside this result.\n")
	if result.CandidateExecuted {
		b.WriteString("- Candidate scan: executed\n")
	} else {
		b.WriteString("- Candidate scan: not executed\n")
	}
	fmt.Fprintf(&b, "- Candidates: %d; scanned: %d\n", result.Candidates, result.Scanned)
	if result.CandidateJQL != "" {
		fmt.Fprintf(&b, "- Candidate search: %s\n", markdownSafe(result.CandidateJQL))
	}
	if complete {
		fmt.Fprintf(&b, "- Matching issues: %d (complete; exact count)\n", len(result.Matches))
	} else {
		fmt.Fprintf(&b, "- Matching issues: %d observed; partial scan; total unknown\n", len(result.Matches))
	}
	b.WriteString("\n")

	if !complete {
		b.WriteString("**PARTIAL:** the candidate search or activity scan was incomplete, so the observed matches are not a complete total.\n\n")
	} else if len(result.Matches) == 0 {
		b.WriteString("No matching issues were found in the completed scan.\n\n")
	}
	if len(result.Problems) > 0 {
		b.WriteString("Scan notes:\n")
		for _, problem := range result.Problems {
			fmt.Fprintf(&b, "- %s\n", markdownSafe(problem))
		}
		b.WriteString("\n")
	}
	b.WriteString("Only records currently visible to this Jira account are included. Deleted, restricted, or moved-out records are excluded, and Jira reads are not an atomic snapshot.\n\n")

	shown := len(result.Matches)
	if shown > maxRows {
		shown = maxRows
	}
	if len(result.Matches) > maxRows {
		fmt.Fprintf(&b, "Displaying %d of %d observed matching issues; counts above reflect all observed matches.\n\n", maxRows, len(result.Matches))
	}
	for _, match := range result.Matches[:shown] {
		fmt.Fprintf(&b, "## %s — %s\n\n", markdownSafe(match.Issue.Key), markdownSafe(match.Issue.Summary))
		events := append([]jira.ActivityEvent(nil), match.Events...)
		sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
		eventCount := len(events)
		if eventCount > maxActivityEvents {
			eventCount = maxActivityEvents
		}
		for _, event := range events[:eventCount] {
			at := event.At.In(location).Format("2006-01-02 15:04:05 MST")
			fmt.Fprintf(&b, "- **%s** at %s", markdownSafe(activityKindLabel(event.Kind)), markdownSafe(at))
			if event.Detail != "" {
				fmt.Fprintf(&b, " — %s", markdownSafe(event.Detail))
			}
			b.WriteString("\n")
		}
		if len(events) == 0 {
			b.WriteString("- No matching activity evidence was returned for this issue.\n")
		}
		if len(events) > maxActivityEvents {
			fmt.Fprintf(&b, "- %d additional events omitted from this issue's display.\n", len(events)-maxActivityEvents)
		}
		b.WriteString("\n")
	}
	if len(result.Matches) > shown {
		fmt.Fprintf(&b, "%d more observed matching issues omitted from the display.\n", len(result.Matches)-shown)
	}
	return b.String()
}

func activityLocation(name string, assumed bool) (*time.Location, string) {
	if strings.TrimSpace(name) == "" {
		name = "UTC"
		assumed = true
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		location = time.UTC
		name = "UTC"
		assumed = true
	}
	if assumed {
		name += " (assumed fallback)"
	}
	return location, name
}

func activityKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case jira.ActivityKindComment:
		return "Comment"
	case jira.ActivityKindWorklog:
		return "Worklog"
	case jira.ActivityKindStatus:
		return "Status change"
	default:
		if kind == "" {
			return "Activity"
		}
		return kind
	}
}

// markdownSafe keeps data on one line and escapes Markdown structure so issue
// summaries, event details, and scan notes cannot inject links or table rows.
func markdownSafe(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, `\`, `\\`)
	for _, char := range []string{"`", "*", "_", "[", "]", "!", "|", "<", ">", "~", "#"} {
		value = strings.ReplaceAll(value, char, `\`+char)
	}
	return value
}
