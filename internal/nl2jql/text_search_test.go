package nl2jql

import (
	"errors"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

func TestTextNormalizationProtectsLiteralMeaning(t *testing.T) {
	input := "lsit my opne items that contain the words Were, opne or origianl and order by priorty"
	want := "list my open items that contain the words Were, opne or origianl and order by priority"
	if got := NewNormalizer().Normalize(input, map[string]string{"Origianl": "PROJ"}); got != want {
		t.Fatalf("normalized=%q, want %q", got, want)
	}
	if err := checkAmbiguity(input); err != nil {
		t.Fatalf("literal tense word triggered syntax ambiguity: %v", err)
	}
}

func TestTextLiteralCannotInjectJQLConstraints(t *testing.T) {
	predicate, err := textOrPredicate(&TextSearch{Terms: []*TextSearchTerm{{Value: `gpu" OR project = OTHER`}, {Value: "NOT"}}})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := (&QueryPlan{Filter: predicate}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jql.Parse(tree.Render())
	if err != nil || !parsed.HasField("text") || parsed.HasField("project") {
		t.Fatalf("rendered=%s parsed=%+v err=%v; text must remain a single operand", tree.Render(), parsed, err)
	}
}

func TestTextWordListsHaveBoundedComplexity(t *testing.T) {
	for _, search := range []*TextSearch{
		nil,
		{},
		{Terms: []*TextSearchTerm{nil}},
		{Terms: []*TextSearchTerm{{Value: strings.Repeat("x", 129)}}},
		{Terms: make([]*TextSearchTerm, 33)},
	} {
		if _, err := textOrPredicate(search); !errors.Is(err, ErrAmbiguousIntent) {
			t.Fatalf("unbounded/empty search accepted: %+v err=%v", search, err)
		}
	}
}
