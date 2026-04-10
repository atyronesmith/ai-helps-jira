package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// SimilarIssue represents a single match found by duplicate detection.
type SimilarIssue struct {
	Key        string  `json:"key"`
	Summary    string  `json:"summary"`
	Confidence float64 `json:"confidence"` // 0.0 to 1.0
	Reason     string  `json:"reason"`
	Relation   string  `json:"relation"` // "duplicate", "related", "parent", "subtask"
}

// SimilarityResult holds the complete response from duplicate detection.
type SimilarityResult struct {
	TargetKey  string
	TargetText string
	Matches    []SimilarIssue
}

const similaritySystemPrompt = `You are a JIRA issue analyst. Given a target issue and a list of candidate issues, identify which candidates are semantically similar, duplicates, or related to the target.

For each match, provide:
- key: the candidate issue key
- summary: the candidate's summary
- confidence: a float between 0.0 and 1.0 indicating how similar/related
- reason: a brief explanation of why they are similar
- relation: one of "duplicate", "related", "parent", "subtask"

Only include candidates with meaningful similarity (confidence >= 0.3).

Respond ONLY with valid JSON (no markdown fences) with this structure:
  {"matches": [{"key": "...", "summary": "...", "confidence": 0.85, "reason": "...", "relation": "..."}]}`

// PrepareCandidates filters the target issue out of the candidate list.
func PrepareCandidates(targetKey string, issues []*jira.IssueDetail) []*jira.IssueDetail {
	var result []*jira.IssueDetail
	for _, issue := range issues {
		if issue.Key != targetKey {
			result = append(result, issue)
		}
	}
	return result
}

// FilterByThreshold removes matches below the given confidence threshold.
func FilterByThreshold(matches []SimilarIssue, threshold float64) []SimilarIssue {
	var result []SimilarIssue
	for _, m := range matches {
		if m.Confidence >= threshold {
			result = append(result, m)
		}
	}
	return result
}

// SortByConfidence sorts matches descending by confidence score.
func SortByConfidence(matches []SimilarIssue) {
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Confidence > matches[j].Confidence
	})
}

// BuildSimilarityPrompt constructs the user message for the LLM from a target and candidates.
func BuildSimilarityPrompt(target *jira.IssueDetail, candidates []*jira.IssueDetail) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Target issue:\n")
	fmt.Fprintf(&b, "  Key: %s\n", target.Key)
	fmt.Fprintf(&b, "  Summary: %s\n", target.Summary)
	if target.Description != "" {
		fmt.Fprintf(&b, "  Description: %s\n", target.Description)
	}

	b.WriteString("\nCandidate issues:\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "  - %s: %s", c.Key, c.Summary)
		if c.Description != "" {
			fmt.Fprintf(&b, " | %s", c.Description)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// BuildSimilarityPromptFromText constructs the user message when given freeform text instead of an issue.
func BuildSimilarityPromptFromText(text string, candidates []*jira.IssueDetail) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Target text:\n  %s\n", text)

	b.WriteString("\nCandidate issues:\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "  - %s: %s", c.Key, c.Summary)
		if c.Description != "" {
			fmt.Fprintf(&b, " | %s", c.Description)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// ParseSimilarityResponse parses the LLM JSON response into structured results.
func ParseSimilarityResponse(raw string) ([]SimilarIssue, error) {
	cleaned := cleanJSON(raw)

	var resp struct {
		Matches []SimilarIssue `json:"matches"`
	}
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil, fmt.Errorf("parse similarity response: %w", err)
	}

	// Clamp confidence to [0, 1]
	for i := range resp.Matches {
		if resp.Matches[i].Confidence > 1.0 {
			resp.Matches[i].Confidence = 1.0
		}
		if resp.Matches[i].Confidence < 0.0 {
			resp.Matches[i].Confidence = 0.0
		}
	}

	return resp.Matches, nil
}

// FindSimilar orchestrates the full duplicate detection flow:
// prepare candidates, call LLM, parse, filter, sort.
func FindSimilar(provider Provider, target *jira.IssueDetail, candidates []*jira.IssueDetail, threshold float64) (*SimilarityResult, error) {
	if len(candidates) == 0 {
		return &SimilarityResult{TargetKey: target.Key, Matches: nil}, nil
	}

	prompt := BuildSimilarityPrompt(target, candidates)

	text, err := provider.Complete(context.Background(), similaritySystemPrompt, prompt, 4096)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}

	matches, err := ParseSimilarityResponse(text)
	if err != nil {
		return nil, err
	}

	matches = FilterByThreshold(matches, threshold)
	SortByConfidence(matches)

	return &SimilarityResult{
		TargetKey: target.Key,
		Matches:   matches,
	}, nil
}

// FindSimilarByText finds issues similar to freeform text.
func FindSimilarByText(provider Provider, text string, candidates []*jira.IssueDetail, threshold float64) (*SimilarityResult, error) {
	if len(candidates) == 0 {
		return &SimilarityResult{TargetText: text, Matches: nil}, nil
	}

	prompt := BuildSimilarityPromptFromText(text, candidates)

	raw, err := provider.Complete(context.Background(), similaritySystemPrompt, prompt, 4096)
	if err != nil {
		return nil, fmt.Errorf("LLM api: %w", err)
	}

	matches, err := ParseSimilarityResponse(raw)
	if err != nil {
		return nil, err
	}

	matches = FilterByThreshold(matches, threshold)
	SortByConfidence(matches)

	return &SimilarityResult{
		TargetText: text,
		Matches:    matches,
	}, nil
}
