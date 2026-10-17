package nl2jql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

func TestTypedContractRejectsDuplicateKeysAndExcessiveDepth(t *testing.T) {
	cases := []string{strings.Replace(supportedStatusPlan, `"version":1`, `"version":2,"version":1`, 1), strings.Replace(supportedStatusPlan, `"text":"Open"`, `"text":"Closed","text":"Open"`, 1)}
	deep := `{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Open"}}`
	for range 18 {
		deep = `{"kind":"not","children":[` + deep + `]}`
	}
	cases = append(cases, `{"version":1,"outcome":"supported","mode":"list","filter":`+deep+`}`)
	for _, raw := range cases {
		_, err := fallbackOutput(t, raw, nil)
		if translationerror.KindOf(err) != translationerror.InvalidOutput {
			t.Fatalf("invalid contract error=%v", err)
		}
	}
}
func TestTypedCustomFieldApplicabilityChecksEveryRequestedProject(t *testing.T) {
	raw := `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"and","children":[{"kind":"comparison","field":"project","operator":"IN","values":[{"kind":"string","text":"OPS"},{"kind":"string","text":"OTHER"}]},{"kind":"comparison","field":"Budget","operator":">","value":{"kind":"number","text":"3"}}]}}`
	_, err := fallbackOutput(t, raw, []fieldmeta.Field{{ID: "customfield_1", Name: "Budget", Type: "number", ApplicabilityKnown: true, Projects: []string{"OPS"}}})
	if !errors.Is(err, fieldmeta.ErrInapplicableField) {
		t.Fatalf("cross-project applicability=%v", err)
	}
}
func TestTypedDateSelfAndOrderingCompileLocally(t *testing.T) {
	raw := `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"and","children":[{"kind":"owner","value":{"kind":"self"}},{"kind":"comparison","field":"created","operator":">=","value":{"kind":"calendar_function","text":"startOfMonth","offset":-1}},{"kind":"comparison","field":"created","operator":"<","value":{"kind":"calendar_function","text":"startOfMonth"}}]},"sort":[{"field":"priority","direction":"DESC"},{"field":"created","direction":"ASC"}]}`
	got, err := fallbackOutput(t, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `assignee = currentUser() AND created >= startOfMonth("-1") AND created < startOfMonth() ORDER BY priority DESC, created ASC`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestFallbackRejectsOversizedInputBeforeProviderCall(t *testing.T) {
	p := &recordingProvider{jql: supportedStatusPlan}
	fb, _ := NewFallbackEngine(p)
	_, err := fb.Generate(context.Background(), strings.Repeat("x", maxFallbackInputBytes+1), nil)
	if translationerror.KindOf(err) != translationerror.Unsupported {
		t.Fatalf("input budget error=%v", err)
	}
	if p.calls != 0 {
		t.Fatalf("oversized input caused %d provider calls", p.calls)
	}
}

func TestTypedPlanRejectsConflictingStatusAndOwnerConjunctions(t *testing.T) {
	for _, filter := range []string{
		`{"kind":"and","children":[{"kind":"owner","value":{"kind":"self"}},{"kind":"owner","value":{"kind":"string","text":"other-user"}}]}`,
		`{"kind":"and","children":[{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"In Review"}},{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Open"}}]}`,
	} {
		raw := `{"version":1,"outcome":"supported","mode":"list","filter":` + filter + `}`
		if _, err := fallbackOutput(t, raw, nil); !errors.Is(err, ErrAmbiguousIntent) {
			t.Fatalf("contradictory intent accepted: %v", err)
		}
		raw = strings.Replace(raw, `"kind":"and"`, `"kind":"or"`, 1)
		if _, err := fallbackOutput(t, raw, nil); err != nil {
			t.Fatalf("valid alternatives rejected: %v", err)
		}
	}
}
