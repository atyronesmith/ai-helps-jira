package format

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// RenderSummary formats a summary as a string in the given output format.
func RenderSummary(boards []jira.BoardInfo, openIssues []jira.Issue,
	jiraServer, outputFormat string) string {

	var b strings.Builder

	switch outputFormat {
	case "slack":
		for _, board := range boards {
			if board.BoardType == "scrum" && board.SprintName != "" {
				fmt.Fprintf(&b, "*Sprint: %s* (%s)\n\n", board.SprintName, board.Name)
			} else {
				fmt.Fprintf(&b, "*Kanban: %s*\n\n", board.Name)
			}
			appendIssueListSlack(&b, board.Issues, jiraServer)
		}
		if len(boards) == 0 {
			b.WriteString("_No board issues found._\n\n")
		}
		if len(openIssues) > 0 {
			b.WriteString("*All Open Issues*\n\n")
			appendIssueListSlack(&b, openIssues, jiraServer)
		} else {
			b.WriteString("No open issues assigned!\n\n")
		}

	case "text":
		for _, board := range boards {
			if board.BoardType == "scrum" && board.SprintName != "" {
				fmt.Fprintf(&b, "Sprint: %s (%s)\n\n", board.SprintName, board.Name)
			} else {
				fmt.Fprintf(&b, "Kanban: %s\n\n", board.Name)
			}
			appendIssueListText(&b, board.Issues)
		}
		if len(boards) == 0 {
			b.WriteString("No board issues found.\n\n")
		}
		if len(openIssues) > 0 {
			b.WriteString("All Open Issues\n\n")
			appendIssueListText(&b, openIssues)
		} else {
			b.WriteString("No open issues assigned!\n\n")
		}

	default: // "markdown"
		for _, board := range boards {
			if board.BoardType == "scrum" && board.SprintName != "" {
				fmt.Fprintf(&b, "## Sprint: %s (%s)\n\n", board.SprintName, board.Name)
			} else {
				fmt.Fprintf(&b, "## Kanban: %s\n\n", board.Name)
			}
			appendIssueTableMD(&b, board.Issues, jiraServer)
		}
		if len(boards) == 0 {
			b.WriteString("*No board issues found.*\n\n")
		}
		if len(openIssues) > 0 {
			b.WriteString("## All Open Issues\n\n")
			appendIssueTableMD(&b, openIssues, jiraServer)
		} else {
			b.WriteString("No open issues assigned!\n\n")
		}
	}

	return b.String()
}

// WriteSummary renders a summary and writes it to a file.
func WriteSummary(boards []jira.BoardInfo, openIssues []jira.Issue,
	outfile, jiraServer, outputFormat string) error {

	content := RenderSummary(boards, openIssues, jiraServer, outputFormat)
	if err := os.WriteFile(outfile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	slog.Info("summary written", "path", outfile)
	fmt.Printf("Summary written to %s\n", outfile)
	return nil
}

func appendIssueTableMD(b *strings.Builder, issues []jira.Issue, server string) {
	b.WriteString("| Key | Status | Priority | Summary |\n")
	b.WriteString("|-----|--------|----------|---------|\n")
	for _, issue := range issues {
		link := fmt.Sprintf("[%s](%s/browse/%s)", issue.Key, server, issue.Key)
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n",
			link, issue.Status, issue.Priority, issue.Summary)
	}
	b.WriteString("\n")
}

func appendIssueListSlack(b *strings.Builder, issues []jira.Issue, server string) {
	for _, issue := range issues {
		link := fmt.Sprintf("<%s/browse/%s|%s>", server, issue.Key, issue.Key)
		fmt.Fprintf(b, "- %s [%s] (%s) %s\n",
			link, issue.Status, issue.Priority, issue.Summary)
	}
	b.WriteString("\n")
}

func appendIssueListText(b *strings.Builder, issues []jira.Issue) {
	for _, issue := range issues {
		fmt.Fprintf(b, "  %-14s %-15s %-10s %s\n",
			issue.Key, issue.Status, issue.Priority, issue.Summary)
	}
	b.WriteString("\n")
}

// RenderDigest formats a digest report as a string in the given output format.
func RenderDigest(parent *jira.IssueDetail, digest *DigestData,
	jiraServer, outputFormat string) string {

	var b strings.Builder

	switch outputFormat {
	case "slack":
		fmt.Fprintf(&b, "*Digest: %s — %s*\n", parent.Key, parent.Summary)
		fmt.Fprintf(&b, "Status: *%s*\n\n", digest.OverallStatus)

		if len(digest.Progress) > 0 {
			b.WriteString("*Progress Updates*\n\n")
			for _, p := range digest.Progress {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, p.EpicKey, p.EpicKey)
				fmt.Fprintf(&b, "- %s [%s] %s\n", link, p.Status, p.Update)
			}
			b.WriteString("\n")
		}
		if len(digest.Blockers) > 0 {
			b.WriteString("*Blockers*\n\n")
			for _, bl := range digest.Blockers {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, bl.EpicKey, bl.EpicKey)
				fmt.Fprintf(&b, "- %s: %s", link, bl.Blocker)
				if bl.Impact != "" {
					fmt.Fprintf(&b, " (Impact: %s)", bl.Impact)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if len(digest.NotStarted) > 0 {
			b.WriteString("*Not Started*\n\n")
			for _, n := range digest.NotStarted {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, n.EpicKey, n.EpicKey)
				fmt.Fprintf(&b, "- %s: %s — %s\n", link, n.EpicSummary, n.Reason)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "*Summary*\n%s\n", digest.Summary)

	case "text":
		fmt.Fprintf(&b, "Digest: %s — %s\n", parent.Key, parent.Summary)
		fmt.Fprintf(&b, "Overall Status: %s\n\n", digest.OverallStatus)

		if len(digest.Progress) > 0 {
			b.WriteString("Progress Updates\n")
			for _, p := range digest.Progress {
				fmt.Fprintf(&b, "  %s [%s] %s\n", p.EpicKey, p.Status, p.Update)
			}
			b.WriteString("\n")
		}
		if len(digest.Blockers) > 0 {
			b.WriteString("Blockers\n")
			for _, bl := range digest.Blockers {
				fmt.Fprintf(&b, "  %s: %s", bl.EpicKey, bl.Blocker)
				if bl.Impact != "" {
					fmt.Fprintf(&b, " (Impact: %s)", bl.Impact)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if len(digest.NotStarted) > 0 {
			b.WriteString("Not Started\n")
			for _, n := range digest.NotStarted {
				fmt.Fprintf(&b, "  %s: %s — %s\n", n.EpicKey, n.EpicSummary, n.Reason)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Summary\n  %s\n", digest.Summary)

	default: // "markdown"
		fmt.Fprintf(&b, "# Digest: %s — %s\n\n", parent.Key, parent.Summary)
		fmt.Fprintf(&b, "**Overall Status:** %s\n\n", digest.OverallStatus)

		if len(digest.Progress) > 0 {
			b.WriteString("## Progress Updates\n\n")
			b.WriteString("| Epic | Status | Update |\n")
			b.WriteString("|------|--------|--------|\n")
			for _, p := range digest.Progress {
				link := fmt.Sprintf("[%s](%s/browse/%s)", p.EpicKey, jiraServer, p.EpicKey)
				fmt.Fprintf(&b, "| %s | %s | %s |\n", link, p.Status, p.Update)
			}
			b.WriteString("\n")
		}
		if len(digest.Blockers) > 0 {
			b.WriteString("## Blockers\n\n")
			for _, bl := range digest.Blockers {
				link := fmt.Sprintf("[%s](%s/browse/%s)", bl.EpicKey, jiraServer, bl.EpicKey)
				fmt.Fprintf(&b, "- **%s**: %s", link, bl.Blocker)
				if bl.Impact != "" {
					fmt.Fprintf(&b, " _(Impact: %s)_", bl.Impact)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if len(digest.NotStarted) > 0 {
			b.WriteString("## Not Started\n\n")
			for _, n := range digest.NotStarted {
				link := fmt.Sprintf("[%s](%s/browse/%s)", n.EpicKey, jiraServer, n.EpicKey)
				fmt.Fprintf(&b, "- **%s**: %s — %s\n", link, n.EpicSummary, n.Reason)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## Summary\n\n%s\n", digest.Summary)
	}

	return b.String()
}

// WriteDigest renders a digest and writes it to a file.
func WriteDigest(parent *jira.IssueDetail, digest *DigestData,
	outfile, jiraServer, outputFormat string) error {

	content := RenderDigest(parent, digest, jiraServer, outputFormat)
	if err := os.WriteFile(outfile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write digest: %w", err)
	}
	slog.Info("digest written", "path", outfile)
	fmt.Printf("Digest written to %s\n", outfile)
	return nil
}
