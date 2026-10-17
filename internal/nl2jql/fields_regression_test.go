package nl2jql

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

func TestCachedNumericFieldPreservesBooleanIntentWithoutProvider(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("deterministic custom field must not call provider")}
	fallback, _ := NewFallbackEngine(provider)
	tr, err := NewTranslator(nil, fallback, Options{Project: "PROJ", Fields: []fieldmeta.Field{{ID: "customfield_12", Name: "Story Points", Type: "number", ApplicabilityKnown: true, Projects: []string{"PROJ"}}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tr.Translate(context.Background(), "my (story points > 3 or story points < 1)")
	if err != nil {
		t.Fatal(err)
	}
	if result.JQL != `assignee = currentUser() AND (cf[12] > 3 OR cf[12] < 1)` {
		t.Fatalf("Boolean custom field JQL=%s", result.JQL)
	}
	if atomic.LoadInt32(&provider.calls) != 0 {
		t.Fatal("provider called")
	}
}
func TestDeterministicNumericFieldRequiresNumericMetadata(t *testing.T) {
	provider := &recordingProvider{fail: errors.New("must not call provider")}
	fallback, _ := NewFallbackEngine(provider)
	tr, _ := NewTranslator(nil, fallback, Options{Fields: []fieldmeta.Field{{ID: "customfield_1", Name: "Story Points", Type: "string"}}})
	if _, err := tr.Translate(context.Background(), "my work items with story points > 3"); !errors.Is(err, fieldmeta.ErrMissingMetadata) {
		t.Fatalf("wrong field type accepted: %v", err)
	}
	if atomic.LoadInt32(&provider.calls) != 0 {
		t.Fatal("provider called")
	}
}
