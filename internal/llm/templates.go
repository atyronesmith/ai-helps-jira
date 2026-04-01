package llm

import (
	"fmt"
	"strings"
)

type EpicContent struct {
	Summary            string   `json:"summary"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Priority           string   `json:"priority"`
	Labels             []string `json:"labels"`
}

const EpicSystemPrompt = `You are a senior product manager helping create JIRA EPICs.
Given a brief description, generate a complete EPIC with:

1. **Summary**: A clear, concise EPIC title (under 80 chars)
2. **Description**: 2-3 paragraphs covering:
   - What this EPIC delivers and why it matters
   - High-level approach
   - Key technical considerations
3. **Acceptance Criteria**: 4-6 testable criteria
4. **Priority**: One of "Highest", "High", "Medium", "Low", "Lowest"
5. **Labels**: 2-4 relevant labels (lowercase-hyphenated)

Respond ONLY with valid JSON (no markdown fences) with these keys:
  summary, description, acceptance_criteria (array of strings),
  priority, labels (array of strings)`

func BuildDescription(epic *EpicContent) string {
	var b strings.Builder
	b.WriteString(epic.Description)
	b.WriteString("\n\nh3. Acceptance Criteria\n")
	for _, c := range epic.AcceptanceCriteria {
		fmt.Fprintf(&b, "* %s\n", c)
	}
	return b.String()
}
