package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// mockProvider implements Provider for testing without real LLM calls.
type mockProvider struct {
	response string
	err      error
	calls    int
}

func (m *mockProvider) Complete(_ context.Context, _, _ string, _ int) (string, error) {
	m.calls++
	return m.response, m.err
}

// --- PrepareCandidates ---

func TestPrepareCandidates_ExcludesTarget(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "target issue"},
		{Key: "PROJ-2", Summary: "other issue"},
		{Key: "PROJ-3", Summary: "another issue"},
	}

	got := PrepareCandidates("PROJ-1", issues)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	for _, c := range got {
		if c.Key == "PROJ-1" {
			t.Error("target issue should be excluded from candidates")
		}
	}
}

func TestPrepareCandidates_TargetNotInList(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "issue 2"},
		{Key: "PROJ-3", Summary: "issue 3"},
	}

	got := PrepareCandidates("PROJ-999", issues)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates when target not in list, got %d", len(got))
	}
}

func TestPrepareCandidates_EmptyInput(t *testing.T) {
	got := PrepareCandidates("PROJ-1", nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates for nil input, got %d", len(got))
	}
}

func TestPrepareCandidates_AllSameKey(t *testing.T) {
	issues := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "dup 1"},
		{Key: "PROJ-1", Summary: "dup 2"},
	}

	got := PrepareCandidates("PROJ-1", issues)
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates when all match target, got %d", len(got))
	}
}

// --- FilterByThreshold ---

func TestFilterByThreshold_FiltersBelow(t *testing.T) {
	matches := []SimilarIssue{
		{Key: "PROJ-1", Confidence: 0.9},
		{Key: "PROJ-2", Confidence: 0.5},
		{Key: "PROJ-3", Confidence: 0.3},
	}

	got := FilterByThreshold(matches, 0.5)
	if len(got) != 2 {
		t.Fatalf("expected 2 matches >= 0.5, got %d", len(got))
	}
	for _, m := range got {
		if m.Confidence < 0.5 {
			t.Errorf("match %s has confidence %f below threshold", m.Key, m.Confidence)
		}
	}
}

func TestFilterByThreshold_ZeroThreshold(t *testing.T) {
	matches := []SimilarIssue{
		{Key: "PROJ-1", Confidence: 0.1},
		{Key: "PROJ-2", Confidence: 0.0},
	}

	got := FilterByThreshold(matches, 0.0)
	if len(got) != 2 {
		t.Fatalf("expected all matches with threshold 0.0, got %d", len(got))
	}
}

func TestFilterByThreshold_EmptyInput(t *testing.T) {
	got := FilterByThreshold(nil, 0.5)
	if len(got) != 0 {
		t.Fatalf("expected 0 matches for nil input, got %d", len(got))
	}
}

// --- SortByConfidence ---

func TestSortByConfidence_DescendingOrder(t *testing.T) {
	matches := []SimilarIssue{
		{Key: "PROJ-1", Confidence: 0.3},
		{Key: "PROJ-2", Confidence: 0.9},
		{Key: "PROJ-3", Confidence: 0.6},
	}

	SortByConfidence(matches)

	if matches[0].Key != "PROJ-2" || matches[1].Key != "PROJ-3" || matches[2].Key != "PROJ-1" {
		t.Errorf("expected descending order [PROJ-2, PROJ-3, PROJ-1], got [%s, %s, %s]",
			matches[0].Key, matches[1].Key, matches[2].Key)
	}
}

func TestSortByConfidence_AlreadySorted(t *testing.T) {
	matches := []SimilarIssue{
		{Key: "PROJ-1", Confidence: 0.9},
		{Key: "PROJ-2", Confidence: 0.5},
	}

	SortByConfidence(matches)

	if matches[0].Key != "PROJ-1" {
		t.Errorf("already-sorted input should remain stable, got %s first", matches[0].Key)
	}
}

func TestSortByConfidence_EmptySlice(t *testing.T) {
	// Should not panic on empty/nil
	SortByConfidence(nil)
	SortByConfidence([]SimilarIssue{})
}

// --- BuildSimilarityPrompt ---

func TestBuildSimilarityPrompt_ContainsTarget(t *testing.T) {
	target := &jira.IssueDetail{
		Key:         "PROJ-1",
		Summary:     "Fix login timeout",
		Description: "Users see a timeout when logging in",
	}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "Login page crashes", Description: "Login crashes on submit"},
	}

	prompt := BuildSimilarityPrompt(target, candidates)

	if !strings.Contains(prompt, "PROJ-1") {
		t.Error("prompt should contain target key")
	}
	if !strings.Contains(prompt, "Fix login timeout") {
		t.Error("prompt should contain target summary")
	}
	if !strings.Contains(prompt, "Users see a timeout") {
		t.Error("prompt should contain target description")
	}
}

func TestBuildSimilarityPrompt_ContainsCandidates(t *testing.T) {
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "target"}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "candidate one", Description: "desc one"},
		{Key: "PROJ-3", Summary: "candidate two", Description: "desc two"},
	}

	prompt := BuildSimilarityPrompt(target, candidates)

	if !strings.Contains(prompt, "PROJ-2") || !strings.Contains(prompt, "PROJ-3") {
		t.Error("prompt should contain all candidate keys")
	}
	if !strings.Contains(prompt, "candidate one") || !strings.Contains(prompt, "candidate two") {
		t.Error("prompt should contain all candidate summaries")
	}
}

func TestBuildSimilarityPrompt_NoCandidates(t *testing.T) {
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "target"}

	prompt := BuildSimilarityPrompt(target, nil)

	// Should still produce a valid prompt (LLM will return empty matches)
	if !strings.Contains(prompt, "PROJ-1") {
		t.Error("prompt should still contain target even with no candidates")
	}
}

func TestBuildSimilarityPrompt_EmptyDescription(t *testing.T) {
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "No desc issue", Description: ""}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "Other", Description: ""},
	}

	prompt := BuildSimilarityPrompt(target, candidates)

	if strings.Contains(prompt, "Description:") {
		t.Error("prompt should not contain Description label when description is empty")
	}
	if !strings.Contains(prompt, "PROJ-1") {
		t.Error("prompt should still contain target key")
	}
}

func TestBuildSimilarityPromptFromText_ContainsText(t *testing.T) {
	text := "Users cannot log in after password reset"
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "Login broken"},
	}

	prompt := BuildSimilarityPromptFromText(text, candidates)

	if !strings.Contains(prompt, text) {
		t.Error("prompt should contain the freeform text")
	}
	if !strings.Contains(prompt, "PROJ-2") {
		t.Error("prompt should contain candidate keys")
	}
}

// --- ParseSimilarityResponse ---

func TestParseSimilarityResponse_ValidJSON(t *testing.T) {
	resp := `{"matches": [{"key": "PROJ-2", "summary": "Login bug", "confidence": 0.85, "reason": "same area", "relation": "duplicate"}]}`

	matches, err := ParseSimilarityResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Key != "PROJ-2" {
		t.Errorf("expected PROJ-2, got %s", matches[0].Key)
	}
	if matches[0].Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", matches[0].Confidence)
	}
}

func TestParseSimilarityResponse_CodeFenced(t *testing.T) {
	resp := "```json\n{\"matches\": [{\"key\": \"PROJ-2\", \"summary\": \"test\", \"confidence\": 0.7, \"reason\": \"similar\", \"relation\": \"related\"}]}\n```"

	matches, err := ParseSimilarityResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
}

func TestParseSimilarityResponse_EmptyMatches(t *testing.T) {
	resp := `{"matches": []}`

	matches, err := ParseSimilarityResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

func TestParseSimilarityResponse_MalformedJSON(t *testing.T) {
	_, err := ParseSimilarityResponse("not json at all")
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// --- FindSimilar (integration with mock provider) ---

func TestFindSimilar_ReturnsFilteredSortedResults(t *testing.T) {
	llmResponse := mustJSON(t, map[string]any{
		"matches": []map[string]any{
			{"key": "PROJ-3", "summary": "low match", "confidence": 0.3, "reason": "vaguely related", "relation": "related"},
			{"key": "PROJ-2", "summary": "high match", "confidence": 0.9, "reason": "same bug", "relation": "duplicate"},
		},
	})

	provider := &mockProvider{response: llmResponse}
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "Fix login", Description: "Login broken"}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "high match", Description: "same login bug"},
		{Key: "PROJ-3", Summary: "low match", Description: "unrelated"},
	}

	result, err := FindSimilar(provider, target, candidates, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TargetKey != "PROJ-1" {
		t.Errorf("expected target key PROJ-1, got %s", result.TargetKey)
	}
	// Should filter out PROJ-3 (0.3 < 0.5 threshold) and keep PROJ-2
	if len(result.Matches) != 1 {
		t.Fatalf("expected 1 match above threshold, got %d", len(result.Matches))
	}
	if result.Matches[0].Key != "PROJ-2" {
		t.Errorf("expected PROJ-2 as top match, got %s", result.Matches[0].Key)
	}
}

func TestFindSimilar_NoCandidates(t *testing.T) {
	provider := &mockProvider{response: `{"matches": []}`}
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "test"}

	result, err := FindSimilar(provider, target, nil, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result even with no candidates")
	}
	if len(result.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(result.Matches))
	}
}

func TestFindSimilar_LLMError(t *testing.T) {
	provider := &mockProvider{err: fmt.Errorf("LLM unavailable")}
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "test"}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "candidate"},
	}

	_, err := FindSimilar(provider, target, candidates, 0.5)
	if err == nil {
		t.Error("expected error when LLM fails")
	}
}

func TestFindSimilarByText_Works(t *testing.T) {
	llmResponse := mustJSON(t, map[string]any{
		"matches": []map[string]any{
			{"key": "PROJ-2", "summary": "Login issue", "confidence": 0.8, "reason": "same topic", "relation": "duplicate"},
		},
	})

	provider := &mockProvider{response: llmResponse}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "Login issue", Description: "login broken"},
	}

	result, err := FindSimilarByText(provider, "Users cannot log in", candidates, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TargetText != "Users cannot log in" {
		t.Errorf("expected target text preserved, got %q", result.TargetText)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result.Matches))
	}
}

func TestFindSimilarByText_NoCandidates(t *testing.T) {
	provider := &mockProvider{response: `{"matches": []}`}

	result, err := FindSimilarByText(provider, "some text", nil, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(result.Matches))
	}
	if result.TargetText != "some text" {
		t.Errorf("expected target text preserved, got %q", result.TargetText)
	}
	if provider.calls != 0 {
		t.Errorf("should not call LLM with no candidates, got %d calls", provider.calls)
	}
}

// --- Confidence boundary cases ---

func TestFilterByThreshold_ExactThreshold(t *testing.T) {
	matches := []SimilarIssue{
		{Key: "PROJ-1", Confidence: 0.75},
		{Key: "PROJ-2", Confidence: 0.74999},
	}

	got := FilterByThreshold(matches, 0.75)
	if len(got) != 1 {
		t.Fatalf("expected 1 match at exact threshold, got %d", len(got))
	}
	if got[0].Key != "PROJ-1" {
		t.Errorf("expected PROJ-1 at exact threshold, got %s", got[0].Key)
	}
}

func TestParseSimilarityResponse_ClampsConfidence(t *testing.T) {
	resp := mustJSON(t, map[string]any{
		"matches": []map[string]any{
			{"key": "PROJ-1", "summary": "over", "confidence": 1.5, "reason": "test", "relation": "duplicate"},
			{"key": "PROJ-2", "summary": "under", "confidence": -0.3, "reason": "test", "relation": "related"},
			{"key": "PROJ-3", "summary": "normal", "confidence": 0.7, "reason": "test", "relation": "related"},
		},
	})

	matches, err := ParseSimilarityResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range matches {
		if m.Confidence < 0.0 || m.Confidence > 1.0 {
			t.Errorf("confidence for %s should be clamped to [0,1], got %f", m.Key, m.Confidence)
		}
	}
}

// --- FindSimilar multiple matches ---

func TestFindSimilar_MultipleAboveThreshold(t *testing.T) {
	llmResponse := mustJSON(t, map[string]any{
		"matches": []map[string]any{
			{"key": "PROJ-2", "summary": "match A", "confidence": 0.7, "reason": "related", "relation": "related"},
			{"key": "PROJ-3", "summary": "match B", "confidence": 0.95, "reason": "duplicate", "relation": "duplicate"},
			{"key": "PROJ-4", "summary": "match C", "confidence": 0.8, "reason": "similar", "relation": "related"},
		},
	})

	provider := &mockProvider{response: llmResponse}
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "test"}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "match A"},
		{Key: "PROJ-3", Summary: "match B"},
		{Key: "PROJ-4", Summary: "match C"},
	}

	result, err := FindSimilar(provider, target, candidates, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Matches) != 3 {
		t.Fatalf("expected 3 matches above threshold, got %d", len(result.Matches))
	}
	// Verify sorted descending
	if result.Matches[0].Key != "PROJ-3" {
		t.Errorf("expected PROJ-3 (0.95) first, got %s", result.Matches[0].Key)
	}
	if result.Matches[1].Key != "PROJ-4" {
		t.Errorf("expected PROJ-4 (0.8) second, got %s", result.Matches[1].Key)
	}
	if result.Matches[2].Key != "PROJ-2" {
		t.Errorf("expected PROJ-2 (0.7) third, got %s", result.Matches[2].Key)
	}
}

func TestFindSimilar_LLMMalformedJSON(t *testing.T) {
	provider := &mockProvider{response: "I don't know how to help with that"}
	target := &jira.IssueDetail{Key: "PROJ-1", Summary: "test"}
	candidates := []*jira.IssueDetail{
		{Key: "PROJ-2", Summary: "candidate"},
	}

	_, err := FindSimilar(provider, target, candidates, 0.5)
	if err == nil {
		t.Error("expected error when LLM returns non-JSON")
	}
}

// mustJSON marshals v to a JSON string, failing the test on error.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal test data: %v", err)
	}
	return string(b)
}
