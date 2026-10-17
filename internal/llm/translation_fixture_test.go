package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

type fixtureResponseProvider struct {
	response       string
	system, prompt string
}

type failingCompleteProvider struct{ calls atomic.Int32 }

func (p *failingCompleteProvider) Complete(context.Context, string, string, int) (string, error) {
	p.calls.Add(1)
	return "not json", nil
}

func (p *fixtureResponseProvider) Generate(_ context.Context, system, prompt string) (string, error) {
	p.system, p.prompt = system, prompt
	return p.response, nil
}

func TestGenerateJQLWithFixtureCountsCallsAndKeepsFailureDiagnostics(t *testing.T) {
	cfg := &config.Config{JiraProject: "PROJ"}
	fixture := TranslationFixture{Now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ProjectMap: map[string]string{"PROJ": "PROJ"}}
	fast, err := GenerateJQLWithFixture(context.Background(), cfg, "my open bugs in PROJ", fixture, &fakeJQLProvider{response: modelStatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if fast.ProviderCalls != 0 || fast.Route != RouteFastPath || fast.DurationNanos <= 0 {
		t.Fatalf("fast result diagnostics: %+v", fast)
	}

	bad := &fakeJQLProvider{response: "not json"}
	failed, err := GenerateJQLWithFixture(context.Background(), cfg, "external bugs", fixture, bad)
	if err == nil {
		t.Fatal("expected invalid fallback output")
	}
	if failed == nil || failed.ProviderCalls != 1 || failed.Route != RouteFallback || failed.Input != "external bugs" || failed.NormalizedInput == "" || failed.ParseError == "" {
		t.Fatalf("failed result diagnostics: %+v", failed)
	}
}

func TestGenerateJQLWithFixtureFreezesClockMetadataAndEnvironment(t *testing.T) {
	t.Setenv("JIRA_FIELD_ALIASES", `{"priority":"environment-field"}`)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	fetched := now.Add(-48 * time.Hour)
	fixture := TranslationFixture{
		Now:          now,
		Metadata:     TranslationMetadata{FetchedAt: fetched, Fields: []fieldmeta.Field{{ID: "customfield_123", Name: "Effort", Type: "number", Projects: []string{"SUP"}, ApplicabilityKnown: true}}},
		ProjectMap:   map[string]string{"Support": "SUP"},
		FieldAliases: map[string]string{"points": "customfield_123"},
	}
	// An already cancelled request must still return frozen metadata diagnostics.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := GenerateJQLWithFixture(ctx, &config.Config{JiraProject: "SUP"}, "my open bugs", fixture, &fakeJQLProvider{response: modelStatusOpen})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if res == nil || !res.MetadataStale || res.MetadataFetchedAt != fetched || res.MetadataApplicabilityKnown != true || res.ProviderCalls != 0 {
		t.Fatalf("fixture metadata/cancellation diagnostics: %+v", res)
	}

	weekdayFixture := fixture
	weekdayFixture.Now = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC) // Tuesday
	weekday, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "SUP"}, "bugs created last Tuesday", weekdayFixture, &fakeJQLProvider{response: modelStatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(weekday.NormalizedInput, `created >= startOfDay("-7") AND created < startOfDay("-6")`) {
		t.Fatalf("weekday normalization did not use fixture clock: %+v", weekday)
	}

	// The supplied fixture aliases and project map govern the translation even
	// when ambient alias configuration changes.
	provider := &fixtureResponseProvider{response: `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"points","operator":">","value":{"kind":"number","text":"3"}},"sort":[]}`}
	result, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "SUP"}, "find work with points greater than 3 in Support", fixture, provider)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.system, "field alias points -> customfield_123") || strings.Contains(provider.system, "environment-field") || !strings.Contains(provider.prompt, "in SUP") {
		t.Fatalf("fallback schema did not use fixture aliases: %s", provider.prompt)
	}
	if !strings.Contains(result.JQL, "cf[123] > 3") || !strings.Contains(result.JQL, "project = SUP") {
		t.Fatalf("fixture field/project aliases were not resolved: %s", result.JQL)
	}
}

func TestStandardTranslationUsesFixtureContextAndNilFixtureProviderIsRejected(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	fixture := TranslationFixture{Now: now, ProjectMap: map[string]string{"Work Help": "SUP"}}
	res, err := GenerateJQLWithProjects(WithTranslationFixture(context.Background(), fixture), &config.Config{JiraProject: "SUP"}, "bugs created last Tuesday in Work Help", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.NormalizedInput, `created >= startOfDay("-7") AND created < startOfDay("-6")`) || !strings.Contains(res.NormalizedInput, "in SUP") {
		t.Fatalf("standard translator ignored frozen context: %+v", res)
	}
	failed, err := GenerateJQLWithFixture(context.Background(), &config.Config{JiraProject: "SUP"}, "external work", fixture, nil)
	if err == nil || failed == nil || failed.Input != "external work" || failed.ProviderCalls != 0 {
		t.Fatalf("nil fixture provider result = %+v, err %v", failed, err)
	}
}

func TestTranslatorCacheProviderCallsArePerRequestWithSharedEngine(t *testing.T) {
	provider := &failingCompleteProvider{}
	cache := NewTranslatorCache()
	cache.build = func(*config.Config) (*nl2jql.FallbackEngine, error) {
		return nl2jql.NewFallbackEngine(jqlProviderAdapter{provider: provider})
	}
	const requests = 8
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cache.GenerateJQL(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs", nil)
			if err == nil {
				errs <- errors.New("expected invalid fallback response")
				return
			}
			if res == nil || res.ProviderCalls != 1 || res.Route != RouteFallback {
				errs <- fmt.Errorf("incomplete request diagnostics: %+v", res)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := provider.calls.Load(); got != requests {
		t.Errorf("completion calls = %d, want %d", got, requests)
	}
}
