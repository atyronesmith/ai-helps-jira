package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

type metadataTestProvider struct {
	calls    atomic.Int32
	response string
}

func (p *metadataTestProvider) Generate(context.Context, string, string) (string, error) {
	p.calls.Add(1)
	return p.response, nil
}

func metadataTranslatorCache(t *testing.T, response string) *TranslatorCache {
	t.Helper()
	p := &metadataTestProvider{response: response}
	c := NewTranslatorCache()
	c.build = func(*config.Config) (*nl2jql.FallbackEngine, error) { return nl2jql.NewFallbackEngine(p) }
	t.Cleanup(func() {
		if p.calls.Load() != 0 {
			t.Errorf("fallback provider called %d times", p.calls.Load())
		}
	})
	return c
}

func metadataConfig(site, email string) *config.Config {
	return &config.Config{JiraServer: site, JiraEmail: email, JiraProject: "TEST", JiraUser: "self@example.test"}
}

func scopedFieldCache(t *testing.T, cfg *config.Config, fields []fieldmeta.Field, at time.Time) *cache.Cache {
	t.Helper()
	c, err := cache.OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if at.IsZero() && len(fields) == 0 {
		return c
	}
	if err := c.ReplaceFields(context.Background(), fields, at); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCachedStoryPointsTranslationUsesMetadataAndConfiguredSelfOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected Jira request", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := metadataConfig(srv.URL, "account@example.test")
	refreshed := time.Now().UTC().Add(-48 * time.Hour)
	db := scopedFieldCache(t, cfg, []fieldmeta.Field{{ID: "customfield_10001", Name: "Story Points", Type: "number"}}, refreshed)
	tc := metadataTranslatorCache(t, `{"version":1,"outcome":"unsupported"}`)
	ctx := WithTranslationMetadata(context.Background(), db)
	res, err := tc.GenerateJQL(ctx, cfg, "my work items with story points > 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.JQL, "cf[10001] > 3") {
		t.Fatalf("JQL=%q", res.JQL)
	}
	if !strings.Contains(res.JQL, `assignee = "self@example.test"`) {
		t.Fatalf("configured self scope missing: %q", res.JQL)
	}
	if !res.MetadataStale || !res.MetadataFetchedAt.Equal(refreshed) {
		t.Fatalf("metadata freshness = %v/%v", res.MetadataStale, res.MetadataFetchedAt)
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d Jira HTTP requests", requests.Load())
	}
}

func TestCachedMetadataIsScopedBySiteAndAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected Jira request", 500)
	}))
	defer srv.Close()
	owner := metadataConfig(srv.URL, "owner@example.test")
	otherSite := metadataConfig("https://other.invalid", "owner@example.test")
	otherAccount := metadataConfig(srv.URL, "other@example.test")
	db := scopedFieldCache(t, owner, []fieldmeta.Field{{ID: "customfield_7", Name: "Story Points", Type: "number"}}, time.Now().UTC())
	tc := metadataTranslatorCache(t, `{"version":1,"outcome":"unsupported"}`)
	good, err := tc.GenerateJQL(WithTranslationMetadata(context.Background(), db), owner, "my work items with story points > 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(good.JQL, "cf[7] > 3") {
		t.Fatalf("owner JQL=%q", good.JQL)
	}
	for name, cfg := range map[string]*config.Config{"other site": otherSite, "other account": otherAccount} {
		t.Run(name, func(t *testing.T) {
			other := scopedFieldCache(t, cfg, nil, time.Time{})
			_, err := tc.GenerateJQL(WithTranslationMetadata(context.Background(), other), cfg, "my work items with story points > 3", nil)
			if err == nil || !strings.Contains(err.Error(), "field metadata is missing") {
				t.Fatalf("translation error=%v, want missing scoped metadata", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d Jira HTTP requests", requests.Load())
	}
}

func TestCachedTranslatorReadsUpdatedCatalogForEachCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected Jira request", 500)
	}))
	defer srv.Close()
	cfg := metadataConfig(srv.URL, "account@example.test")
	db := scopedFieldCache(t, cfg, []fieldmeta.Field{{ID: "customfield_11", Name: "Story Points", Type: "number"}}, time.Now().UTC())
	var builds atomic.Int32
	tc := NewTranslatorCache()
	tc.build = func(*config.Config) (*nl2jql.FallbackEngine, error) {
		builds.Add(1)
		return nl2jql.NewFallbackEngine(&metadataTestProvider{response: `{"version":1,"outcome":"unsupported"}`})
	}
	translate := func() string {
		res, err := tc.GenerateJQL(WithTranslationMetadata(context.Background(), db), cfg, "my work items with story points > 3", nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.JQL
	}
	if got := translate(); !strings.Contains(got, "cf[11] > 3") {
		t.Fatalf("first JQL=%q", got)
	}
	if err := db.ReplaceFields(context.Background(), []fieldmeta.Field{{ID: "customfield_22", Name: "Story Points", Type: "number"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := translate(); !strings.Contains(got, "cf[22] > 3") {
		t.Fatalf("second JQL reused stale translator metadata: %q", got)
	}
	if builds.Load() != 1 {
		t.Fatalf("fallback engine built %d times, want cached once", builds.Load())
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d Jira HTTP requests", requests.Load())
	}
}

func TestConfiguredFieldAliasResolvesInTypedFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("JIRA_FIELD_ALIASES", `{"effort":"customfield_42"}`)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected Jira request", 500)
	}))
	defer srv.Close()
	cfg := metadataConfig(srv.URL, "account@example.test")
	db := scopedFieldCache(t, cfg, []fieldmeta.Field{{ID: "customfield_42", Name: "Effort Estimate", Type: "number"}}, time.Now().UTC())
	provider := &metadataTestProvider{response: `{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"effort","operator":">","value":{"kind":"number","text":"3"}}}`}
	tc := NewTranslatorCache()
	tc.build = func(*config.Config) (*nl2jql.FallbackEngine, error) { return nl2jql.NewFallbackEngine(provider) }
	res, err := tc.GenerateJQL(WithTranslationMetadata(context.Background(), db), cfg, "find work with effort greater than three", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.JQL, "cf[42] > 3") {
		t.Fatalf("JQL=%q", res.JQL)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls=%d want one typed fallback", provider.calls.Load())
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d Jira HTTP requests", requests.Load())
	}
}

func TestInvalidFieldAliasConfigurationStopsBeforeProviderSetup(t *testing.T) {
	t.Setenv("JIRA_FIELD_ALIASES", "{invalid")
	var builds atomic.Int32
	tc := NewTranslatorCache()
	tc.build = func(*config.Config) (*nl2jql.FallbackEngine, error) {
		builds.Add(1)
		return nl2jql.NewFallbackEngine(&metadataTestProvider{})
	}
	_, err := tc.GenerateJQL(context.Background(), &config.Config{JiraProject: "PROJ"}, "my issues", nil)
	if translationerror.KindOf(err) != translationerror.Unsupported {
		t.Fatalf("alias config classification=%v", err)
	}
	if builds.Load() != 0 {
		t.Fatal("invalid aliases initialized the provider")
	}
}
