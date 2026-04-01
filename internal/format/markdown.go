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
	b.WriteString("|-----|--------|----------|---------||\n")
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
