package format

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"

	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

// DisplaySimilarIssues renders similarity results to the terminal with pterm.
func DisplaySimilarIssues(targetKey, targetText string, matches []llm.SimilarIssue, jiraServer string) {
	title := targetKey
	if title == "" {
		title = "Freeform text"
	}
	pterm.DefaultBox.WithTitle("Find Similar").Println(title)

	if len(matches) == 0 {
		pterm.FgGreen.Println("No similar issues found.")
		fmt.Println()
		return
	}

	data := [][]string{{"Key", "Confidence", "Relation", "Summary", "Reason"}}
	for _, m := range matches {
		data = append(data, []string{
			m.Key,
			fmt.Sprintf("%.0f%%", m.Confidence*100),
			m.Relation,
			truncate(m.Summary, 40),
			truncate(m.Reason, 50),
		})
	}

	table, _ := pterm.DefaultTable.
		WithHasHeader(true).
		WithData(data).
		Srender()

	pterm.DefaultSection.Printfln("Similar Issues (%d)", len(matches))
	fmt.Println(table)
	fmt.Println()
}

// RenderSimilarIssues renders similarity results as markdown, slack, or text.
func RenderSimilarIssues(targetKey, targetText string, matches []llm.SimilarIssue,
	jiraServer, outputFormat string) string {

	var b strings.Builder

	title := targetKey
	if title == "" {
		title = fmt.Sprintf("%q", truncate(targetText, 60))
	}

	switch outputFormat {
	case "slack":
		if targetKey != "" {
			link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, targetKey, targetKey)
			fmt.Fprintf(&b, "*Similar Issues: %s*\n\n", link)
		} else {
			fmt.Fprintf(&b, "*Similar Issues: %s*\n\n", title)
		}
		if len(matches) == 0 {
			b.WriteString("No similar issues found.\n")
		} else {
			for _, m := range matches {
				link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, m.Key, m.Key)
				fmt.Fprintf(&b, "- %s (%.0f%% %s) %s — _%s_\n",
					link, m.Confidence*100, m.Relation, m.Summary, m.Reason)
			}
		}

	case "text":
		fmt.Fprintf(&b, "Similar Issues: %s\n\n", title)
		if len(matches) == 0 {
			b.WriteString("No similar issues found.\n")
		} else {
			for _, m := range matches {
				fmt.Fprintf(&b, "  %s  %.0f%%  %-10s  %s\n    Reason: %s\n",
					m.Key, m.Confidence*100, m.Relation, m.Summary, m.Reason)
			}
		}

	default: // "markdown"
		if targetKey != "" {
			link := fmt.Sprintf("[%s](%s/browse/%s)", targetKey, jiraServer, targetKey)
			fmt.Fprintf(&b, "# Similar Issues: %s\n\n", link)
		} else {
			fmt.Fprintf(&b, "# Similar Issues: %s\n\n", title)
		}
		if len(matches) == 0 {
			b.WriteString("No similar issues found.\n")
		} else {
			b.WriteString("| Key | Confidence | Relation | Summary | Reason |\n")
			b.WriteString("|-----|-----------|----------|---------|--------|\n")
			for _, m := range matches {
				link := fmt.Sprintf("[%s](%s/browse/%s)", m.Key, jiraServer, m.Key)
				fmt.Fprintf(&b, "| %s | %.0f%% | %s | %s | %s |\n",
					link, m.Confidence*100, m.Relation,
					escapePipe(m.Summary), escapePipe(m.Reason))
			}
		}
	}

	return b.String()
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
