package format

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"
)

// WeeklyStatusData holds weekly status results for display.
type WeeklyStatusData struct {
	UserName string
	Projects []WeeklyProject
}

// WeeklyProject groups work items under a project/epic heading.
type WeeklyProject struct {
	ProjectName string
	IssueKey    string
	Bullets     []string
}

// RenderWeeklyStatus formats a weekly status report as a string.
func RenderWeeklyStatus(data *WeeklyStatusData, jiraServer, outputFormat string) string {
	var b strings.Builder

	switch outputFormat {
	case "slack":
		fmt.Fprintf(&b, "*%s*\n\n", data.UserName)
		for _, proj := range data.Projects {
			link := fmt.Sprintf("<%s/browse/%s|%s>", jiraServer, proj.IssueKey, proj.IssueKey)
			fmt.Fprintf(&b, "- %s (%s)\n", proj.ProjectName, link)
			for _, bullet := range proj.Bullets {
				fmt.Fprintf(&b, "  - %s\n", bullet)
			}
		}

	case "text":
		fmt.Fprintf(&b, "%s\n", data.UserName)
		for _, proj := range data.Projects {
			fmt.Fprintf(&b, "* %s (%s — %s/browse/%s)\n", proj.ProjectName, proj.IssueKey, jiraServer, proj.IssueKey)
			for _, bullet := range proj.Bullets {
				fmt.Fprintf(&b, "  * %s\n", bullet)
			}
		}

	default: // "markdown"
		fmt.Fprintf(&b, "# %s\n\n", data.UserName)
		for _, proj := range data.Projects {
			link := fmt.Sprintf("[%s](%s/browse/%s)", proj.IssueKey, jiraServer, proj.IssueKey)
			fmt.Fprintf(&b, "* **%s** (%s)\n", proj.ProjectName, link)
			for _, bullet := range proj.Bullets {
				fmt.Fprintf(&b, "  * %s\n", bullet)
			}
		}
	}

	return b.String()
}

// DisplayWeeklyStatus renders a weekly status report using pterm.
func DisplayWeeklyStatus(data *WeeklyStatusData, jiraServer string) {
	fmt.Println()
	pterm.DefaultBox.WithTitle("Weekly Status").Println(data.UserName)

	for _, proj := range data.Projects {
		items := make([]pterm.BulletListItem, 0, len(proj.Bullets)+1)
		items = append(items, pterm.BulletListItem{
			Level: 0,
			Text:  fmt.Sprintf("%s (%s)", proj.ProjectName, proj.IssueKey),
		})
		for _, bullet := range proj.Bullets {
			items = append(items, pterm.BulletListItem{
				Level: 1,
				Text:  bullet,
			})
		}
		pterm.DefaultBulletList.WithItems(items).Render()
	}
	fmt.Println()
}
