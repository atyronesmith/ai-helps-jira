package format

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"

	"github.com/atyronesmith/ai-helps-jira/internal/confluence"
)

// DisplayConfluenceAnalytics renders page view analytics to the terminal with pterm.
func DisplayConfluenceAnalytics(parentTitle string, pages []confluence.PageAnalytics, jiraServer string) {
	pterm.DefaultBox.WithTitle("Confluence Analytics").Println(parentTitle)

	if len(pages) == 0 {
		pterm.FgYellow.Println("No pages found.")
		fmt.Println()
		return
	}

	data := [][]string{{"Page", "Total Views", "Unique Viewers"}}
	for _, p := range pages {
		data = append(data, []string{
			truncate(p.Title, 50),
			fmt.Sprintf("%d", p.TotalViews),
			fmt.Sprintf("%d", p.UniqueViewers),
		})
	}

	table, _ := pterm.DefaultTable.
		WithHasHeader(true).
		WithData(data).
		Srender()

	pterm.DefaultSection.Printfln("Page Analytics (%d pages)", len(pages))
	fmt.Println(table)
	fmt.Println()
}

// RenderConfluenceAnalytics renders page view analytics as markdown, slack, or text.
func RenderConfluenceAnalytics(parentTitle string, pages []confluence.PageAnalytics,
	jiraServer, outputFormat string) string {

	var b strings.Builder

	switch outputFormat {
	case "slack":
		fmt.Fprintf(&b, "*Confluence Analytics: %s*\n\n", parentTitle)
		if len(pages) == 0 {
			b.WriteString("No pages found.\n")
		} else {
			for _, p := range pages {
				link := fmt.Sprintf("<%s/wiki/pages/viewpage.action?pageId=%s|%s>",
					jiraServer, p.PageID, p.Title)
				fmt.Fprintf(&b, "- %s — %d views, %d unique viewers\n",
					link, p.TotalViews, p.UniqueViewers)
			}
		}

	case "text":
		fmt.Fprintf(&b, "Confluence Analytics: %s\n\n", parentTitle)
		if len(pages) == 0 {
			b.WriteString("No pages found.\n")
		} else {
			for _, p := range pages {
				fmt.Fprintf(&b, "  %-50s  %5d views  %5d unique\n",
					truncate(p.Title, 50), p.TotalViews, p.UniqueViewers)
			}
		}

	default: // "markdown"
		fmt.Fprintf(&b, "# Confluence Analytics: %s\n\n", parentTitle)
		if len(pages) == 0 {
			b.WriteString("No pages found.\n")
		} else {
			b.WriteString("| Page | Total Views | Unique Viewers |\n")
			b.WriteString("|------|------------|----------------|\n")
			for _, p := range pages {
				link := fmt.Sprintf("[%s](%s/wiki/pages/viewpage.action?pageId=%s)",
					escapePipe(p.Title), jiraServer, p.PageID)
				fmt.Fprintf(&b, "| %s | %d | %d |\n",
					link, p.TotalViews, p.UniqueViewers)
			}
		}
	}

	return b.String()
}
