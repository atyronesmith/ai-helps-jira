package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// fakeProjectFetcher is a ProjectFetcher that returns canned data and records how
// often ListProjects was called, so the cache's cold/warm/refetch behaviour can
// be asserted without a real JIRA connection.
type fakeProjectFetcher struct {
	projects []jira.Project
	err      error
	calls    int
}

func (f *fakeProjectFetcher) ListProjects() ([]jira.Project, error) {
	f.calls++
	return f.projects, f.err
}

// countingBuilder wraps f in the lazy builder FetchProjects now expects and counts
// how often the builder (the client-construction seam) is invoked, so a test can
// assert that a cache hit never constructs a fetcher.
func countingBuilder(f ProjectFetcher, built *int) func() (ProjectFetcher, error) {
	return func() (ProjectFetcher, error) {
		*built++
		return f, nil
	}
}

// seedStaleProject inserts a project row directly with a fixed, ancient
// fetched_at so a FetchProjects call sees a stale cache without having to wait
// out a TTL. It bypasses UpsertProjects because that stamps fetched_at with now.
func seedStaleProject(t *testing.T, c *Cache, key, name, when string) {
	t.Helper()
	if _, err := c.db.Exec(
		"INSERT INTO projects (key, name, project_type, fetched_at) VALUES (?, ?, ?, ?)",
		key, name, "software", when); err != nil {
		t.Fatalf("seed stale project: %v", err)
	}
}

// --- Projects (cache layer) ---

// GetProjects on a fresh cache reports no rows; after an upsert it returns them
// ordered by key, with a populated fetched_at.
func TestGetProjects(t *testing.T) {
	c := openTestCache(t)

	if _, _, ok := c.GetProjects(); ok {
		t.Fatal("expected GetProjects to report no rows on a fresh cache")
	}

	projects := []jira.Project{
		{Key: "SUP", Name: "Support", ProjectType: "serviceDesks"},
		{Key: "ENG", Name: "Engineering", ProjectType: "software"},
	}
	if err := c.UpsertProjects(projects); err != nil {
		t.Fatalf("UpsertProjects: %v", err)
	}

	got, fetchedAt, ok := c.GetProjects()
	if !ok {
		t.Fatal("expected GetProjects to report rows after upsert")
	}
	if len(got) != 2 {
		t.Fatalf("GetProjects returned %d projects, want 2", len(got))
	}
	// Rows are ordered by key.
	if got[0].Key != "ENG" || got[1].Key != "SUP" {
		t.Errorf("GetProjects order = [%s, %s], want [ENG, SUP]", got[0].Key, got[1].Key)
	}
	byKey := map[string]jira.Project{}
	for _, p := range got {
		byKey[p.Key] = p
	}
	if byKey["SUP"].Name != "Support" || byKey["ENG"].Name != "Engineering" {
		t.Errorf("GetProjects names wrong: %v", byKey)
	}
	if fetchedAt.IsZero() {
		t.Error("fetchedAt should be set after upsert")
	}
}

// An empty upsert is a no-op: it neither poisons nor clears a good cache.
func TestUpsertProjects_EmptyIsNoop(t *testing.T) {
	c := openTestCache(t)
	if err := c.UpsertProjects([]jira.Project{{Key: "SUP", Name: "Support"}}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	if err := c.UpsertProjects(nil); err != nil {
		t.Fatalf("UpsertProjects(nil): %v", err)
	}
	if err := c.UpsertProjects([]jira.Project{}); err != nil {
		t.Fatalf("UpsertProjects([]): %v", err)
	}
	got, _, ok := c.GetProjects()
	if !ok || len(got) != 1 || got[0].Key != "SUP" {
		t.Fatalf("empty upsert changed the cache: got %v ok=%v", got, ok)
	}
}

// A second upsert of the same key refreshes its name and type (ON CONFLICT).
func TestUpsertProjects_RefreshesRow(t *testing.T) {
	c := openTestCache(t)
	c.UpsertProjects([]jira.Project{{Key: "SUP", Name: "Support", ProjectType: "serviceDesks"}})
	c.UpsertProjects([]jira.Project{{Key: "SUP", Name: "Customer Support", ProjectType: "serviceDesks"}})

	got, _, _ := c.GetProjects()
	if len(got) != 1 {
		t.Fatalf("expected 1 project after refresh, got %d", len(got))
	}
	if got[0].Name != "Customer Support" {
		t.Errorf("name not refreshed: got %q, want %q", got[0].Name, "Customer Support")
	}
}

// Cold fetch touches the network and caches; a warm fetch is served from cache
// with no second network call.
func TestFetchProjects_ColdThenWarm(t *testing.T) {
	c := openTestCache(t)
	fetcher := &fakeProjectFetcher{projects: []jira.Project{
		{Key: "SUP", Name: "Support"},
		{Key: "ENG", Name: "Engineering"},
	}}
	var built int
	got, fromCache, err := c.FetchProjects(countingBuilder(fetcher, &built), time.Hour)
	if err != nil {
		t.Fatalf("FetchProjects (cold): %v", err)
	}
	if fromCache {
		t.Error("cold fetch should report fromCache=false")
	}
	if len(got) != 2 {
		t.Fatalf("cold fetch returned %d projects, want 2", len(got))
	}

	got, fromCache, err = c.FetchProjects(countingBuilder(fetcher, &built), time.Hour)
	if err != nil {
		t.Fatalf("FetchProjects (warm): %v", err)
	}
	if !fromCache {
		t.Error("warm fetch should report fromCache=true")
	}
	if len(got) != 2 {
		t.Fatalf("warm fetch returned %d projects, want 2", len(got))
	}
	if fetcher.calls != 1 {
		t.Errorf("ListProjects called %d times, want 1 (the warm call must hit cache)", fetcher.calls)
	}

	if built != 1 {
		t.Errorf("fetcher built %d times, want 1 (the warm call must not rebuild the client)", built)
	}
}

// A ttl <= 0 disables expiry: a stale row is still served from cache and the
// network is never touched.
func TestFetchProjects_TTLZeroNeverExpires(t *testing.T) {
	c := openTestCache(t)
	seedStaleProject(t, c, "SUP", "Support", "2000-01-01T00:00:00Z")
	fetcher := &fakeProjectFetcher{projects: []jira.Project{{Key: "ENG", Name: "Engineering"}}}
	var built int
	got, fromCache, err := c.FetchProjects(countingBuilder(fetcher, &built), 0)
	if err != nil {
		t.Fatalf("FetchProjects(ttl=0): %v", err)
	}
	if !fromCache {
		t.Error("ttl<=0 should treat the cache as fresh and not re-fetch")
	}
	if len(got) != 1 || got[0].Key != "SUP" {
		t.Errorf("ttl<=0 should serve the stale row, got %v", got)
	}
	if fetcher.calls != 0 {
		t.Errorf("ListProjects called %d times, want 0 (ttl<=0 must not touch the network)", fetcher.calls)
	}

	if built != 0 {
		t.Errorf("fetcher built %d times, want 0 (a cache hit must not construct a client)", built)
	}
}

// A cache older than ttl triggers a re-fetch, replacing the stale rows.
func TestFetchProjects_StaleTriggersRefetch(t *testing.T) {
	c := openTestCache(t)
	seedStaleProject(t, c, "OLD", "Old Name", "2000-01-01T00:00:00Z")
	fetcher := &fakeProjectFetcher{projects: []jira.Project{{Key: "SUP", Name: "Support"}}}
	var built int
	got, fromCache, err := c.FetchProjects(countingBuilder(fetcher, &built), time.Hour)
	if err != nil {
		t.Fatalf("FetchProjects (stale): %v", err)
	}
	if fromCache {
		t.Error("a cache older than ttl must re-fetch, not serve cache")
	}
	if len(got) != 1 || got[0].Key != "SUP" {
		t.Errorf("refetch returned %v, want [SUP]", got)
	}
	if fetcher.calls != 1 {
		t.Errorf("ListProjects called %d times, want 1", fetcher.calls)
	}

	if built != 1 {
		t.Errorf("fetcher built %d times, want 1 (a stale cache must reach the fetch path)", built)
	}
}

// A failed re-fetch on a warm cache still serves the stale copy alongside the
// error, so human names keep resolving even when JIRA is unreachable.
func TestFetchProjects_StaleFetchFailureServesStale(t *testing.T) {
	c := openTestCache(t)
	seedStaleProject(t, c, "SUP", "Support", "2000-01-01T00:00:00Z")
	fetcher := &fakeProjectFetcher{projects: []jira.Project{{Key: "ENG", Name: "Engineering"}}}
	fetcher.err = errors.New("network down")
	var built int
	got, fromCache, err := c.FetchProjects(countingBuilder(fetcher, &built), time.Hour)
	if err == nil {
		t.Fatal("expected an error when a stale re-fetch fails")
	}
	if fromCache {
		t.Error("a failed re-fetch should report fromCache=false")
	}
	if len(got) != 1 || got[0].Key != "SUP" {
		t.Fatalf("stale copy not served on failed refetch: got %v", got)
	}

	if built != 1 {
		t.Errorf("fetcher built %d times, want 1 (the stale path must reach the fetcher)", built)
	}
}

// A failed fetch on a cold cache returns nil and the error (nothing to serve).
func TestFetchProjects_ColdFetchFailure(t *testing.T) {
	c := openTestCache(t)
	fetcher := &fakeProjectFetcher{err: errors.New("network down")}
	var built int
	got, fromCache, err := c.FetchProjects(countingBuilder(fetcher, &built), time.Hour)
	if err == nil {
		t.Fatal("expected an error on a cold fetch failure")
	}
	if fromCache {
		t.Error("a cold failure should report fromCache=false")
	}
	if got != nil {
		t.Errorf("cold failure should return nil projects, got %v", got)
	}

	if built != 1 {
		t.Errorf("fetcher built %d times, want 1 (a cold cache must reach the fetch path)", built)
	}
}
