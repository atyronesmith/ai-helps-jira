package format

import (
	"fmt"

	"github.com/pterm/pterm"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var priorityColors = map[string]pterm.Color{
	"Highest": pterm.FgLightRed,
	"High":    pterm.FgRed,
	"Medium":  pterm.FgYellow,
	"Low":     pterm.FgGreen,
	"Lowest":  pterm.FgLightGreen,
}

var statusColors = map[string]pterm.Color{
	"To Do":       pterm.FgWhite,
	"In Progress": pterm.FgCyan,
	"In Review":   pterm.FgMagenta,
	"Done":        pterm.FgGreen,
}

func DisplaySummary(boards []jira.BoardInfo, openIssues []jira.Issue) {
	fmt.Println()
	for _, board := range boards {
		if board.BoardType == "scrum" && board.SprintName != "" {
			pterm.DefaultBox.WithTitle(fmt.Sprintf("Sprint - %s", board.Name)).
				Println(board.SprintName)
		} else {
			pterm.DefaultBox.WithTitle("Kanban Board").
				Println(board.Name)
		}
		printIssueTable(fmt.Sprintf("%s Issues", board.Name), board.Issues)
	}

	if len(boards) == 0 {
		pterm.FgLightWhite.Println("No board issues found.")
	}

	if len(openIssues) > 0 {
		printIssueTable("All Open Issues", openIssues)
	} else {
		pterm.FgGreen.Println("No open issues assigned to you!")
	}
	fmt.Println()
}

func printIssueTable(title string, issues []jira.Issue) {
	data := [][]string{{"Key", "Status", "Pri", "Summary"}}
	for _, issue := range issues {
		status := colorize(issue.Status, statusColors)
		pri := colorize(issue.Priority, priorityColors)
		summary := issue.Summary
		if len(summary) > 50 {
			summary = summary[:47] + "..."
		}
		data = append(data, []string{issue.Key, status, pri, summary})
	}

	table, _ := pterm.DefaultTable.
		WithHasHeader(true).
		WithData(data).
		Srender()

	if title != "" {
		pterm.DefaultSection.Println(title)
	}
	fmt.Println(table)
}

func colorize(text string, colors map[string]pterm.Color) string {
	if c, ok := colors[text]; ok {
		return pterm.NewStyle(c).Sprint(text)
	}
	return text
}

func DisplayEpicPreview(summary, description string, criteria []string, priority string, labels []string) {
	fmt.Println()
	pterm.DefaultBox.WithTitle("EPIC Summary").Println(summary)
	pterm.DefaultBox.WithTitle("Description").Println(description)

	if len(criteria) > 0 {
		items := make([]pterm.BulletListItem, len(criteria))
		for i, c := range criteria {
			items[i] = pterm.BulletListItem{Level: 0, Text: c}
		}
		pterm.DefaultSection.Println("Acceptance Criteria")
		pterm.DefaultBulletList.WithItems(items).Render()
	}

	fmt.Printf("  Priority: %s\n", priority)
	fmt.Printf("  Labels:   %s\n", joinLabels(labels))
	fmt.Println()
}

func joinLabels(labels []string) string {
	if len(labels) == 0 {
		return "(none)"
	}
	s := ""
	for i, l := range labels {
		if i > 0 {
			s += ", "
		}
		s += l
	}
	return s
}

// DisplayEnrichPreview shows current vs suggested enrichment fields.
func DisplayEnrichPreview(issue *jira.IssueDetail, desc string, criteria []string, priority string, labels []string) {
	fmt.Println()
	pterm.DefaultBox.WithTitle(fmt.Sprintf("%s — %s", issue.Key, issue.IssueType)).
		Println(issue.Summary)

	// Current description
	pterm.DefaultSection.Println("Current Description")
	if issue.Description != "" {
		fmt.Println(issue.Description)
	} else {
		pterm.FgLightWhite.Println("(empty)")
	}

	// Suggested description
	pterm.DefaultSection.Println("Suggested Description")
	fmt.Println(desc)

	// Acceptance criteria
	if len(criteria) > 0 {
		items := make([]pterm.BulletListItem, len(criteria))
		for i, c := range criteria {
			items[i] = pterm.BulletListItem{Level: 0, Text: c}
		}
		pterm.DefaultSection.Println("Acceptance Criteria")
		pterm.DefaultBulletList.WithItems(items).Render()
	}

	fmt.Printf("  Priority: %s → %s\n", issue.Priority, priority)
	fmt.Printf("  Labels:   %s → %s\n", joinLabels(issue.Labels), joinLabels(labels))
	fmt.Println()
}

// DisplayQueryResults renders issues from a JQL query.
func DisplayQueryResults(issues []jira.Issue, jql string) {
	fmt.Println()
	if jql != "" {
		pterm.FgLightWhite.Printfln("JQL: %s", jql)
		fmt.Println()
	}
	if len(issues) == 0 {
		pterm.FgYellow.Println("No issues found.")
		fmt.Println()
		return
	}
	printIssueTable("Query Results", issues)
	pterm.FgLightWhite.Printfln("(%d issues)", len(issues))
	fmt.Println()
}

// DigestData holds digest results for display (avoids circular import with llm package).
type DigestData struct {
	OverallStatus string
	Progress      []DigestProgress
	Blockers      []DigestBlocker
	NotStarted    []DigestNotStarted
	Summary       string
}

type DigestProgress struct {
	EpicKey, EpicSummary, Status, Update string
}

type DigestBlocker struct {
	EpicKey, EpicSummary, Blocker, Impact string
}

type DigestNotStarted struct {
	EpicKey, EpicSummary, Reason string
}

var overallStatusColors = map[string]pterm.Color{
	"on track": pterm.FgGreen,
	"at risk":  pterm.FgYellow,
	"blocked":  pterm.FgRed,
}

// DisplayDigest renders a progress digest for a Feature/Initiative.
func DisplayDigest(parent *jira.IssueDetail, digest *DigestData) {
	fmt.Println()
	pterm.DefaultBox.WithTitle(fmt.Sprintf("%s — %s", parent.Key, parent.IssueType)).
		Println(parent.Summary)

	statusText := colorize(digest.OverallStatus, overallStatusColors)
	fmt.Printf("  Overall Status: %s\n\n", statusText)

	// Progress updates
	if len(digest.Progress) > 0 {
		data := [][]string{{"Issue", "Status", "Update"}}
		for _, p := range digest.Progress {
			summary := p.Update
			if len(summary) > 50 {
				summary = summary[:47] + "..."
			}
			data = append(data, []string{p.EpicKey, p.Status, summary})
		}
		pterm.DefaultSection.Println("Progress Updates")
		table, _ := pterm.DefaultTable.WithHasHeader(true).WithData(data).Srender()
		fmt.Println(table)
	}

	// Blockers
	if len(digest.Blockers) > 0 {
		pterm.DefaultSection.Println("Blockers")
		for _, b := range digest.Blockers {
			pterm.FgRed.Printf("  %s: ", b.EpicKey)
			fmt.Printf("%s", b.Blocker)
			if b.Impact != "" {
				pterm.FgLightWhite.Printf(" (Impact: %s)", b.Impact)
			}
			fmt.Println()
		}
		fmt.Println()
	}

	// Not started
	if len(digest.NotStarted) > 0 {
		pterm.DefaultSection.Println("Not Started")
		for _, n := range digest.NotStarted {
			pterm.FgYellow.Printf("  %s: ", n.EpicKey)
			fmt.Printf("%s — %s\n", n.EpicSummary, n.Reason)
		}
		fmt.Println()
	}

	// Executive summary
	pterm.DefaultBox.WithTitle("Executive Summary").Println(digest.Summary)
	fmt.Println()
}

// BacklogHealthData holds backlog health check results for display.
type BacklogHealthData struct {
	TotalIssues      int
	HealthyCount     int
	StaleDays        int
	Categories       []HealthCategory
	ExecutiveSummary string
	Recommendations  []string
}

// HealthCategory groups health issues by problem type.
type HealthCategory struct {
	Name   string
	Issues []HealthIssue
}

// HealthIssue is a single issue flagged in a health check.
type HealthIssue struct {
	Key     string
	Summary string
	Detail  string
}

// DisplayBacklogHealth renders a backlog health report with pterm.
func DisplayBacklogHealth(report *BacklogHealthData, jiraServer string) {
	// Header stats
	problemCount := report.TotalIssues - report.HealthyCount
	healthPct := 0
	if report.TotalIssues > 0 {
		healthPct = report.HealthyCount * 100 / report.TotalIssues
	}

	var healthColor pterm.Color
	switch {
	case healthPct >= 80:
		healthColor = pterm.FgGreen
	case healthPct >= 60:
		healthColor = pterm.FgYellow
	default:
		healthColor = pterm.FgRed
	}

	pterm.DefaultBox.WithTitle("Backlog Health Check").
		Println(fmt.Sprintf("%d open issues — %d healthy, %d with problems (%s)",
			report.TotalIssues, report.HealthyCount, problemCount,
			pterm.NewStyle(healthColor).Sprintf("%d%% healthy", healthPct)))

	// Executive summary
	if report.ExecutiveSummary != "" {
		pterm.DefaultSection.Println("Executive Summary")
		fmt.Println(report.ExecutiveSummary)
		fmt.Println()
	}

	// Problem categories
	for _, cat := range report.Categories {
		data := [][]string{{"Key", "Summary", "Problem"}}
		for _, issue := range cat.Issues {
			summary := issue.Summary
			if len(summary) > 45 {
				summary = summary[:42] + "..."
			}
			data = append(data, []string{issue.Key, summary, issue.Detail})
		}
		pterm.DefaultSection.Printfln("%s (%d)", cat.Name, len(cat.Issues))
		table, _ := pterm.DefaultTable.WithHasHeader(true).WithData(data).Srender()
		fmt.Println(table)
	}

	// Recommendations
	if len(report.Recommendations) > 0 {
		items := make([]pterm.BulletListItem, len(report.Recommendations))
		for i, r := range report.Recommendations {
			items[i] = pterm.BulletListItem{Level: 0, Text: r}
		}
		pterm.DefaultSection.Println("Recommendations")
		pterm.DefaultBulletList.WithItems(items).Render()
	}

	if len(report.Categories) == 0 {
		pterm.FgGreen.Println("No problems found — backlog is healthy!")
	}
	fmt.Println()
}

// CommentSummaryData holds comment summary results for display.
type CommentSummaryData struct {
	Summary       string
	KeyDecisions  []string
	ActionItems   []string
	OpenQuestions []string
}

// DisplayCommentSummary renders a comment thread summary with pterm.
func DisplayCommentSummary(issue *jira.IssueDetail, summary *CommentSummaryData) {
	pterm.DefaultBox.WithTitle(fmt.Sprintf("%s — %s", issue.Key, issue.IssueType)).
		Println(issue.Summary)

	pterm.DefaultSection.Println("Summary")
	fmt.Println(summary.Summary)

	if len(summary.KeyDecisions) > 0 {
		items := make([]pterm.BulletListItem, len(summary.KeyDecisions))
		for i, d := range summary.KeyDecisions {
			items[i] = pterm.BulletListItem{Level: 0, Text: d}
		}
		pterm.DefaultSection.Println("Key Decisions")
		pterm.DefaultBulletList.WithItems(items).Render()
	}

	if len(summary.ActionItems) > 0 {
		items := make([]pterm.BulletListItem, len(summary.ActionItems))
		for i, a := range summary.ActionItems {
			items[i] = pterm.BulletListItem{Level: 0, Text: a}
		}
		pterm.DefaultSection.Println("Action Items")
		pterm.DefaultBulletList.WithItems(items).Render()
	}

	if len(summary.OpenQuestions) > 0 {
		items := make([]pterm.BulletListItem, len(summary.OpenQuestions))
		for i, q := range summary.OpenQuestions {
			items[i] = pterm.BulletListItem{Level: 0, Text: q}
		}
		pterm.DefaultSection.Println("Open Questions")
		pterm.DefaultBulletList.WithItems(items).Render()
	}

	fmt.Println()
}

func StatusPrinter(msg string) *pterm.SpinnerPrinter {
	s, _ := pterm.DefaultSpinner.Start(msg)
	return s
}
