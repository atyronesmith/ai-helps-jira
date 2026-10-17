package nl2jql

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

const supportedStatusPlan = `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Open"}}}`

func TestFallbackPromptDefinesTypedContractAndOpenWork(t *testing.T) {
	prompt := buildSystemPrompt(nil)
	for _, required := range []string{"version\":1", "typed", "statusCategory != Done", "Never substitute an issuetype !=", "Fields must be core fields or fields in the supplied catalog", "No raw JQL"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
}

func fallbackOutput(t *testing.T, raw string, fields []fieldmeta.Field) (string, error) {
	t.Helper()
	p := &recordingProvider{jql: raw}
	fb, err := NewFallbackEngine(p)
	if err != nil {
		t.Fatal(err)
	}
	return fb.generate(context.Background(), "request", nil, fieldmeta.New(fields, nil), "OPS", "")
}

func TestTypedFallbackPreservesBooleanGroupsAndQuotesValues(t *testing.T) {
	raw := `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"and","children":[{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Open"}},{"kind":"or","children":[{"kind":"comparison","field":"summary","operator":"~","value":{"kind":"string","text":"x\" OR project = OTHER"}},{"kind":"comparison","field":"summary","operator":"~","value":{"kind":"string","text":"safe"}}]}]}}`
	got, err := fallbackOutput(t, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `status = Open AND (summary ~ "x\" OR project = OTHER" OR summary ~ safe)`) {
		t.Fatalf("compiled plan lost grouping or escaping: %s", got)
	}
	if strings.Contains(got, `" OR project = OTHER" OR project = OTHER`) {
		t.Fatalf("hostile literal escaped its quoted operand: %s", got)
	}
}

func TestTypedFallbackRequiresMetadataAndAppliesScope(t *testing.T) {
	unknown := `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"Budget Owner","operator":"=","value":{"kind":"string","text":"Sam"}}}`
	if _, err := fallbackOutput(t, unknown, nil); !errors.Is(err, fieldmeta.ErrMissingMetadata) {
		t.Fatalf("unknown field error=%v", err)
	}
	fields := []fieldmeta.Field{{ID: "customfield_9", Name: "Budget Owner", Type: "string", Projects: []string{"OTHER"}, ApplicabilityKnown: true}}
	if _, err := fallbackOutput(t, unknown, fields); !errors.Is(err, fieldmeta.ErrInapplicableField) {
		t.Fatalf("inapplicable field error=%v", err)
	}
	fields = append(fields, fieldmeta.Field{ID: "customfield_10", Name: "Budget Owner", Type: "string"})
	if _, err := fallbackOutput(t, unknown, fields); !errors.Is(err, fieldmeta.ErrAmbiguousField) {
		t.Fatalf("ambiguous field error=%v", err)
	}
}

func TestTypedFallbackValidatesOperandTypes(t *testing.T) {
	cases := []string{
		`{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"status","operator":">","value":{"kind":"number","text":"3"}}}`,
		`{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"created","operator":"=","value":{"kind":"string","text":"yesterday"}}}`,
	}
	for _, raw := range cases {
		if _, err := fallbackOutput(t, raw, nil); err == nil {
			t.Errorf("accepted operand type mismatch: %s", raw)
		}
	}
}

func TestTypedFallbackRejectsMalformedContracts(t *testing.T) {
	cases := map[string]string{
		"wrong version":    strings.Replace(supportedStatusPlan, `"version":1`, `"version":2`, 1),
		"unknown operator": strings.Replace(supportedStatusPlan, `"operator":"="`, `"operator":"EXEC"`, 1),
		"raw jql":          strings.Replace(supportedStatusPlan, `"filter":{`, `"rawJQL":"project = OPS","filter":{`, 1),
		"unknown property": strings.Replace(supportedStatusPlan, `"mode":"list"`, `"mode":"list","surprise":true`, 1),
		"trailing json":    supportedStatusPlan + ` {"version":1}`,
		"non-json raw jql": "status = Open",
	}
	deep := `{"kind":"comparison","field":"status","operator":"=","value":{"kind":"string","text":"Open"}}`
	for i := 0; i < 18; i++ {
		deep = `{"kind":"not","children":[` + deep + `]}`
	}
	cases["complexity limit"] = `{"version":1,"outcome":"supported","mode":"list","filter":` + deep + `}`
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fallbackOutput(t, raw, nil); err == nil {
				t.Fatalf("accepted invalid model output %s", raw)
			}
		})
	}
}

type controlledProvider struct {
	calls    int32
	response string
	err      error
	cancel   bool
}

func (p *controlledProvider) Generate(ctx context.Context, _, _ string) (string, error) {
	atomic.AddInt32(&p.calls, 1)
	if p.cancel {
		return "", context.Canceled
	}
	return p.response, p.err
}

func TestFallbackCallsProviderOnceAndPropagatesErrors(t *testing.T) {
	failure := errors.New("provider failed")
	for _, tc := range []struct {
		name     string
		provider *controlledProvider
		ctx      context.Context
		want     error
	}{{"provider error", &controlledProvider{err: failure}, context.Background(), failure}, {"canceled before call", &controlledProvider{response: supportedStatusPlan}, makeCanceledContext(), context.Canceled}, {"cancel returned by provider", &controlledProvider{cancel: true}, context.Background(), context.Canceled}} {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := NewFallbackEngine(tc.provider)
			_, err := fb.generate(tc.ctx, "query", nil, fieldmeta.New(nil, nil), "", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want %v", err, tc.want)
			}
			wantCalls := int32(1)
			if tc.name == "canceled before call" {
				wantCalls = 0
			}
			if got := atomic.LoadInt32(&tc.provider.calls); got != wantCalls {
				t.Fatalf("calls=%d want %d", got, wantCalls)
			}
		})
	}
}

func makeCanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
