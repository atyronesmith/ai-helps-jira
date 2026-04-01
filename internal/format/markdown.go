package format

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func WriteSummaryMarkdown(boards []jira.BoardInfo, openIssues []jira.Issue,
	outfile, jiraServer string, slack bool) error {

	var b strings.Builder

	for _, board := range boards {
		if slack {
			if board.BoardType == "scrum" && board.SprintName != "" {
				fmt.Fprintf(&b, "*Sprint: %s* (%s)\n\n", board.SprintName, board.Name)
			} else {
				fmt.Fprintf(&b, "*Kanban: %s*\n\n", board.Name)
			}
			appendIssueListSlack(&b, board.Issues, jiraServer)
		} else {
			if board.BoardType == "scrum" && board.SprintName != "" {
				fmt.Fprintf(&b, "## Sprint: %s (%s)\n\n", board.SprintName, board.Name)
			} else {
				fmt.Fprintf(&b, "## Kanban: %s\n\n", board.Name)
			}
			appendIssueTableMD(&b, board.Issues, jiraServer)
		}
	}

	if len(boards) == 0 {
		if slack {
			b.WriteString("_No board issues found._\n\n")
		} else {
			b.WriteString("*No board issues found.*\n\n")
		}
	}

	if len(openIssues) > 0 {
		if slack {
			b.WriteString("*All Open Issues*\n\n")
			appendIssueListSlack(&b, openIssues, jiraServer)
		} else {
			b.WriteString("## All Open Issues\n\n")
			appendIssueTableMD(&b, openIssues, jiraServer)
		}
	} else {
		b.WriteString("No open issues assigned!\n\n")
	}

	if err := os.WriteFile(outfile, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write markdown: %w", err)
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

// WriteDigestMarkdown writes a digest report as markdown or Slack mrkdwn.
func WriteDigestMarkdown(parent *jira.IssueDetail, digest *DigestData,
	outfile, jiraServer string, slack bool) error {

	var b strings.Builder

	if slack {
		fmt.Fprintf(&b, "*Digest: %s — %s*\n", parent.Key, parent.Summary)
		fmt.Fprintf(&b, "Status: *%s*\n\n", digest.OverallStatus)
	} else {
		fmt.Fprintf(&b, "# Digest: %s — %s\n\n", parent.Key, parent.Summary)
		fmt.Fprintf(&b, "**Overall Status:** %s\n\n", digest.OverallStatus)
	}

	if len(digest.Progress) > 0 {
		if slack {
			b.WriteString("*Progress Updates*\n\n")
			for _, p := range digest.Progress {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, p.EpicKey, p.EpicKey)
				fmt.Fprintf(&b, "- %s [%s] %s\n", link, p.Status, p.Update)
			}
		} else {
			b.WriteString("## Progress Updates\n\n")
			b.WriteString("| Epic | Status | Update |\n")
			b.WriteString("|------|--------|--------|\n")
			for _, p := range digest.Progress {
				link := fmt.Sprintf("[%s](%s/browse/%s)", p.EpicKey, jiraServer, p.EpicKey)
				fmt.Fprintf(&b, "| %s | %s | %s |\n", link, p.Status, p.Update)
			}
		}
		b.WriteString("\n")
	}

	if len(digest.Blockers) > 0 {
		if slack {
			b.WriteString("*Blockers*\n\n")
			for _, bl := range digest.Blockers {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, bl.EpicKey, bl.EpicKey)
				fmt.Fprintf(&b, "- %s: %s", link, bl.Blocker)
				if bl.Impact != "" {
					fmt.Fprintf(&b, " (Impact: %s)", bl.Impact)
				}
				b.WriteString("\n")
			}
		} else {
			b.WriteString("## Blockers\n\n")
			for _, bl := range digest.Blockers {
				link := fmt.Sprintf("[%s](%s/browse/%s)", bl.EpicKey, jiraServer, bl.EpicKey)
				fmt.Fprintf(&b, "- **%s**: %s", link, bl.Blocker)
				if bl.Impact != "" {
					fmt.Fprintf(&b, " _(Impact: %s)_", bl.Impact)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	if len(digest.NotStarted) > 0 {
		if slack {
			b.WriteString("*Not Started*\n\n")
			for _, n := range digest.NotStarted {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, n.EpicKey, n.EpicKey)
				fmt.Fprintf(&b, "- %s: %s — %s\n", link, n.EpicSummary, n.Reason)
			}
		} else {
			b.WriteString("## Not Started\n\n")
			for _, n := range digest.NotStarted {
				link := fmt.Sprintf("[%s](%s/browse/%s)", n.EpicKey, jiraServer, n.EpicKey)
				fmt.Fprintf(&b, "- **%s**: %s — %s\n", link, n.EpicSummary, n.Reason)
			}
		}
		b.WriteString("\n")
	}

	if slack {
		fmt.Fprintf(&b, "*Summary*\n%s\n", digest.Summary)
	} else {
		fmt.Fprintf(&b, "## Summary\n\n%s\n", digest.Summary)
	}

	if err := os.WriteFile(outfile, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write digest markdown: %w", err)
	}
	slog.Info("digest written", "path", outfile)
	fmt.Printf("Digest written to %s\n", outfile)
	return nil
}

func appendIssueListSlack(b *strings.Builder, issues []jira.Issue, server string) {
	for _, issue := range issues {
		link := fmt.Sprintf("<%s/browse/%s|%s>", server, issue.Key, issue.Key)
		fmt.Fprintf(b, "- %s [%s] (%s) %s\n",
			link, issue.Status, issue.Priority, issue.Summary)
	}
	b.WriteString("\n")
}
