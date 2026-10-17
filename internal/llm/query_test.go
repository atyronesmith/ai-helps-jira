package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

type fixedIntentProvider string

func (p fixedIntentProvider) Generate(context.Context, string, string) (string, error) {
	return string(p), nil
}

func runFallbackIntent(t *testing.T, raw string) (string, error) {
	t.Helper()
	engine, err := nl2jql.NewFallbackEngine(fixedIntentProvider(raw))
	if err != nil {
		t.Fatal(err)
	}
	return engine.Generate(context.Background(), "find something", nil)
}

func TestFallbackIntentQuotesExplicitEmail(t *testing.T) {
	got, err := runFallbackIntent(t, `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"owner","value":{"kind":"string","text":"jsmith@example.com"}},"sort":[]}`)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != `assignee = "jsmith@example.com"` {
		t.Fatalf("JQL = %q", got)
	}
}

func TestFallbackIntentAcceptsTypedCalendarDate(t *testing.T) {
	got, err := runFallbackIntent(t, `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"created","operator":">=","value":{"kind":"calendar_date","text":"2026-10-01"}},"sort":[]}`)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != `created >= "2026-10-01"` {
		t.Fatalf("JQL = %q", got)
	}
}

func TestFallbackIntentRejectsRawDateFunctionsAndMonthUnits(t *testing.T) {
	tests := map[string]string{
		"arbitrary function":             `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"created","operator":">=","value":{"kind":"string","text":"evilFunction()"}},"sort":[]}`,
		"month unit":                     `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"created","operator":">=","value":{"kind":"string","text":"-1m"}},"sort":[]}`,
		"unrecognized calendar function": `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"created","operator":">=","value":{"kind":"calendar_function","text":"thisMonth"}},"sort":[]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := runFallbackIntent(t, raw)
			if err == nil || translationerror.KindOf(err) != translationerror.InvalidOutput {
				t.Fatalf("error = %v, want invalid-output classification", err)
			}
		})
	}
}

func TestUnsupportedIntentIsTerminalUnsupportedError(t *testing.T) {
	got, err := runFallbackIntent(t, `{"version":1,"outcome":"unsupported"}`)
	if got != "" || !errors.Is(err, nl2jql.ErrUnmappable) || translationerror.KindOf(err) != translationerror.Unsupported {
		t.Fatalf("Generate = %q, %v; want terminal unsupported error", got, err)
	}
}

func TestFallbackIntentRequiresMetadataForCustomFields(t *testing.T) {
	_, err := runFallbackIntent(t, `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"Story Points","operator":">","value":{"kind":"number","text":"3"}},"sort":[]}`)
	if !errors.Is(err, fieldmeta.ErrMissingMetadata) {
		t.Fatalf("error = %v, want missing field metadata", err)
	}
}
