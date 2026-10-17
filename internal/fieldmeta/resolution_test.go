package fieldmeta

import (
	"errors"
	"testing"
)

func TestConfiguredAliasDisambiguatesDuplicateNames(t *testing.T) {
	fields := []Field{{ID: "customfield_1", Name: "Effort"}, {ID: "customfield_2", Name: "Effort"}}
	r := New(fields, map[string]string{"Effort": "customfield_2"})
	got, err := r.Resolve("Effort", "PROJ", "")
	if err != nil || got.ID != "customfield_2" {
		t.Fatalf("alias resolution=%+v,%v", got, err)
	}
	for _, q := range []string{"cf[1]", "customfield_1"} {
		got, err = r.Resolve(q, "PROJ", "")
		if err != nil || got.ID != "customfield_1" {
			t.Fatalf("exact ID %s=%+v,%v", q, got, err)
		}
	}
	if _, err := New(fields, map[string]string{"Effort": "customfield_9"}).Resolve("Effort", "", ""); !errors.Is(err, ErrMissingMetadata) {
		t.Fatalf("invalid alias guessed another field: %v", err)
	}
	if _, err := New(fields, map[string]string{"effort": "customfield_1", "EFFORT": "customfield_2"}).Resolve("Effort", "", ""); !errors.Is(err, ErrAmbiguousField) {
		t.Fatalf("case-conflicting alias: %v", err)
	}
}
func TestScopedFuzzyRequiresKnownApplicabilityAndRejectsCloseMatches(t *testing.T) {
	fields := []Field{{ID: "customfield_1", Name: "Customer Segment"}}
	if _, err := New(fields, nil).Resolve("Customer Segmant", "PROJ", ""); !errors.Is(err, ErrMissingMetadata) {
		t.Fatalf("fuzzy accepted unknown applicability: %v", err)
	}
	fields[0].ApplicabilityKnown = true
	fields[0].Projects = []string{"PROJ"}
	if _, err := New(fields, nil).Resolve("Customer Segmant", "PROJ", ""); err != nil {
		t.Fatal(err)
	}
	fields = append(fields, Field{ID: "customfield_2", Name: "Customer Segmont", ApplicabilityKnown: true, Projects: []string{"PROJ"}})
	if _, err := New(fields, nil).Resolve("Customer Segmant", "PROJ", ""); !errors.Is(err, ErrAmbiguousField) {
		t.Fatalf("close fuzzy candidates not ambiguous: %v", err)
	}
	if _, err := New([]Field{{ID: "customfield_1", Name: "customfield_99"}}, nil).Resolve("customfield_999", "", ""); !errors.Is(err, ErrMissingMetadata) {
		t.Fatalf("repaired an explicit missing ID: %v", err)
	}
}
