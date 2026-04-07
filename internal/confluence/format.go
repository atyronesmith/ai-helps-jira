package confluence

import (
	"fmt"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

// WeeklyStatusToStorage converts a weekly status report to Confluence storage format (XHTML).
func WeeklyStatusToStorage(userName, jiraServer string, projects []llm.WeeklyProjectItem) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("<h2>%s</h2>\n", escapeHTML(userName)))

	for _, proj := range projects {
		issueURL := fmt.Sprintf("%s/browse/%s", jiraServer, proj.IssueKey)
		b.WriteString("<ul>\n")
		b.WriteString(fmt.Sprintf("  <li>%s (<a href=\"%s\">%s</a>)\n",
			escapeHTML(proj.ProjectName), issueURL, proj.IssueKey))
		b.WriteString("    <ul>\n")
		for _, bullet := range proj.Bullets {
			b.WriteString(fmt.Sprintf("      <li>%s</li>\n", escapeHTML(bullet)))
		}
		b.WriteString("    </ul>\n")
		b.WriteString("  </li>\n")
		b.WriteString("</ul>\n")
	}

	return b.String()
}

// IndexEntryStorage returns a Confluence storage format entry for the parent page index.
// The entry is a link to the child page with the date range.
func IndexEntryStorage(title, pageID string) string {
	return fmt.Sprintf(`<li><ac:link><ri:content-id ri:content-id="%s" /><ac:plain-text-link-body><![CDATA[%s]]></ac:plain-text-link-body></ac:link></li>`, pageID, escapeHTML(title))
}

// InsertIndexEntry adds a link entry at the top of a <ul> in the parent page body.
// If no <ul> exists, wraps the entry in a new one. If an entry with the same title
// already exists, it is replaced to avoid duplicates.
func InsertIndexEntry(body, entry, title string) string {
	escapedTitle := escapeHTML(title)

	// Remove any existing entry for the same title (overwrite case)
	if strings.Contains(body, escapedTitle) {
		lines := strings.Split(body, "\n")
		var filtered []string
		for _, line := range lines {
			if strings.Contains(line, escapedTitle) && strings.Contains(line, "<li>") {
				continue
			}
			filtered = append(filtered, line)
		}
		body = strings.Join(filtered, "\n")
	}

	// Insert at top of existing <ul>, or create a new one
	if idx := strings.Index(body, "<ul>"); idx >= 0 {
		insertPoint := idx + len("<ul>")
		return body[:insertPoint] + "\n" + entry + body[insertPoint:]
	}

	// No list found — prepend a new one
	return "<ul>\n" + entry + "\n</ul>\n" + body
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
