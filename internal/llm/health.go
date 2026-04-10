package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// HealthFinding represents a single issue flagged during a backlog health check.
type HealthFinding struct {
	Category string // stale, missing_description, orphaned, unassigned_active, missing_labels
	Key      string
	Summary  string
	Detail   string
}

// HealthReport holds the full backlog health check results.
type HealthReport struct {
	TotalIssues int
	Findings    []HealthFinding
	// LLM-generated
	ExecutiveSummary string   `json:"executive_summary"`
	Recommendations  []string `json:"recommendations"`
}

const healthSummaryPrompt = `You are a product management expert analyzing a JIRA backlog health check.
Given the project's open issue statistics and a list of problems found, produce:

1. **Executive Summary**: A 2-3 sentence assessment of overall backlog health.
   Mention the most critical problems first.
2. **Recommendations**: 3-6 actionable recommendations to improve backlog health,
   ordered by impact. Be specific (e.g. "Assign PROJ-123 which has been in progress
   for 21 days" rather than "assign stale tickets").

Respond ONLY with valid JSON (no markdown fences) with these keys:
  executive_summary (string), recommendations (array of strings)`

// CheckBacklogHealth runs rule-based checks on open issues and returns findings.
func CheckBacklogHealth(issues []*jira.IssueDetail, staleDays int) []HealthFinding {
	if staleDays <= 0 {
		staleDays = 14
	}
	staleThreshold := time.Now().AddDate(0, 0, -staleDays)

	var findings []HealthFinding

	activeStatuses := map[string]bool{
		"In Progress": true,
		"In Review":   true,
		"In Dev":      true,
		"In QA":       true,
		"In Test":     true,
		"Reviewing":   true,
	}

	for _, issue := range issues {
		// Stale: in active status but not updated recently
		if activeStatuses[issue.Status] && issue.Updated.Before(staleThreshold) {
			days := int(time.Since(issue.Updated).Hours() / 24)
			findings = append(findings, HealthFinding{
				Category: "stale",
				Key:      issue.Key,
				Summary:  issue.Summary,
				Detail:   fmt.Sprintf("%s for %d days (last updated %s)", issue.Status, days, issue.Updated.Format("2006-01-02")),
			})
		}

		// Missing description
		if strings.TrimSpace(issue.Description) == "" {
			findings = append(findings, HealthFinding{
				Category: "missing_description",
				Key:      issue.Key,
				Summary:  issue.Summary,
				Detail:   fmt.Sprintf("%s with no description", issue.IssueType),
			})
		}

		// Orphaned: no parent epic (skip epics/initiatives themselves)
		lowerType := strings.ToLower(issue.IssueType)
		if issue.ParentKey == "" && lowerType != "epic" && lowerType != "initiative" && lowerType != "feature" {
			findings = append(findings, HealthFinding{
				Category: "orphaned",
				Key:      issue.Key,
				Summary:  issue.Summary,
				Detail:   fmt.Sprintf("%s with no parent epic", issue.IssueType),
			})
		}

		// Unassigned but in active status
		if activeStatuses[issue.Status] && issue.Assignee == "" {
			findings = append(findings, HealthFinding{
				Category: "unassigned_active",
				Key:      issue.Key,
				Summary:  issue.Summary,
				Detail:   fmt.Sprintf("%s but unassigned", issue.Status),
			})
		}

		// Missing labels
		if len(issue.Labels) == 0 {
			findings = append(findings, HealthFinding{
				Category: "missing_labels",
				Key:      issue.Key,
				Summary:  issue.Summary,
				Detail:   fmt.Sprintf("%s with no labels", issue.IssueType),
			})
		}
	}

	return findings
}

// GenerateHealthSummary uses an LLM to produce an executive summary and recommendations.
func GenerateHealthSummary(cfg *config.Config, totalIssues int, findings []HealthFinding) (string, []string, error) {
	ctx := context.Background()

	slog.Info("generating health summary", "total_issues", totalIssues, "findings", len(findings))

	provider, err := NewProvider(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("create LLM provider: %w", err)
	}

	// Build stats by category
	counts := make(map[string]int)
	for _, f := range findings {
		counts[f.Category]++
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("Project: %s", cfg.JiraProject))
	parts = append(parts, fmt.Sprintf("Total open issues: %d", totalIssues))
	parts = append(parts, fmt.Sprintf("Issues with problems: %d", len(findings)))
	parts = append(parts, "")
	parts = append(parts, "Problem counts:")
	for cat, count := range counts {
		parts = append(parts, fmt.Sprintf("  %s: %d", cat, count))
	}
	parts = append(parts, "")
	parts = append(parts, "Detailed findings:")
	for _, f := range findings {
		parts = append(parts, fmt.Sprintf("  [%s] %s: %s — %s", f.Category, f.Key, f.Summary, f.Detail))
	}

	userMessage := strings.Join(parts, "\n")

	text, err := provider.Complete(ctx, healthSummaryPrompt, userMessage, 4096)
	if err != nil {
		return "", nil, fmt.Errorf("LLM api: %w", err)
	}
	slog.Debug("raw LLM response", "text", text)

	var result struct {
		ExecutiveSummary string   `json:"executive_summary"`
		Recommendations  []string `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(cleanJSON(text)), &result); err != nil {
		return "", nil, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse:\n%s", err, text)
	}

	return result.ExecutiveSummary, result.Recommendations, nil
}
