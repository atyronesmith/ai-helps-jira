package cache

import (
	"fmt"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// mockFetcher records API calls and returns canned data.
type mockFetcher struct {
	issues   map[string]*jira.IssueDetail
	links    map[string][]jira.IssueLink
	comments map[string][]jira.Comment

	// Call counters
	GetIssueCalls          map[string]int
	GetCommentsCalls       map[string]int
	GetIssueWithLinksCalls map[string]int
}

func newMockFetcher() *mockFetcher {
	return &mockFetcher{
		issues:                 make(map[string]*jira.IssueDetail),
		links:                  make(map[string][]jira.IssueLink),
		comments:               make(map[string][]jira.Comment),
		GetIssueCalls:          make(map[string]int),
		GetCommentsCalls:       make(map[string]int),
		GetIssueWithLinksCalls: make(map[string]int),
	}
}

func (m *mockFetcher) GetIssue(key string) (*jira.IssueDetail, error) {
	m.GetIssueCalls[key]++
	if d, ok := m.issues[key]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("issue %s not found", key)
}

func (m *mockFetcher) GetComments(key string) ([]jira.Comment, error) {
	m.GetCommentsCalls[key]++
	return m.comments[key], nil
}

func (m *mockFetcher) GetIssueWithLinks(key string) (*jira.IssueDetail, []jira.IssueLink, error) {
	m.GetIssueWithLinksCalls[key]++
	d, ok := m.issues[key]
	if !ok {
		return nil, nil, fmt.Errorf("issue %s not found", key)
	}
	return d, m.links[key], nil
}

func (m *mockFetcher) totalAPICalls() int {
	n := 0
	for _, c := range m.GetIssueCalls {
		n += c
	}
	for _, c := range m.GetCommentsCalls {
		n += c
	}
	for _, c := range m.GetIssueWithLinksCalls {
		n += c
	}
	return n
}

// --- Integration Tests ---

func TestFetchIssue_ColdCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	now := time.Now().UTC().Truncate(time.Second)
	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "test issue", Status: "To Do",
		IssueType: "Task", Updated: now,
	}

	// First call — cache miss, hits API
	detail, cached, err := c.FetchIssue(f, "PROJ-1", time.Time{})
	if err != nil {
		t.Fatalf("FetchIssue: %v", err)
	}
	if cached {
		t.Error("expected cache miss on first call")
	}
	if detail.Key != "PROJ-1" {
		t.Errorf("got key %q", detail.Key)
	}
	if f.GetIssueCalls["PROJ-1"] != 1 {
		t.Errorf("expected 1 API call, got %d", f.GetIssueCalls["PROJ-1"])
	}
}

func TestFetchIssue_WarmCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	now := time.Now().UTC().Truncate(time.Second)
	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "test issue", Status: "To Do",
		IssueType: "Task", Updated: now,
	}

	// First call populates cache
	c.FetchIssue(f, "PROJ-1", time.Time{})

	// Second call — cache hit, no API call
	detail, cached, err := c.FetchIssue(f, "PROJ-1", time.Time{})
	if err != nil {
		t.Fatalf("FetchIssue: %v", err)
	}
	if !cached {
		t.Error("expected cache hit on second call")
	}
	if detail.Key != "PROJ-1" {
		t.Errorf("got key %q", detail.Key)
	}
	if f.GetIssueCalls["PROJ-1"] != 1 {
		t.Errorf("expected still 1 API call, got %d", f.GetIssueCalls["PROJ-1"])
	}
}

func TestFetchIssue_StaleCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	oldTime := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)

	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "original", Status: "To Do",
		IssueType: "Task", Updated: oldTime,
	}

	// Populate cache with old data
	c.FetchIssue(f, "PROJ-1", time.Time{})

	// Update mock to return newer data
	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "updated", Status: "In Progress",
		IssueType: "Task", Updated: newTime,
	}

	// Fetch with knownUpdated = newTime → cache stale → re-fetch
	detail, cached, err := c.FetchIssue(f, "PROJ-1", newTime)
	if err != nil {
		t.Fatalf("FetchIssue: %v", err)
	}
	if cached {
		t.Error("expected cache miss when issue is stale")
	}
	if detail.Summary != "updated" {
		t.Errorf("expected updated summary, got %q", detail.Summary)
	}
	if f.GetIssueCalls["PROJ-1"] != 2 {
		t.Errorf("expected 2 API calls (cold + stale re-fetch), got %d", f.GetIssueCalls["PROJ-1"])
	}
}

func TestFetchIssue_FreshCacheWithKnownUpdated(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	cachedTime := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "test", Status: "To Do",
		IssueType: "Task", Updated: cachedTime,
	}

	// Populate cache
	c.FetchIssue(f, "PROJ-1", time.Time{})

	// Fetch with same knownUpdated — still fresh
	_, cached, _ := c.FetchIssue(f, "PROJ-1", cachedTime)
	if !cached {
		t.Error("expected cache hit when knownUpdated == cachedUpdated")
	}
	if f.GetIssueCalls["PROJ-1"] != 1 {
		t.Error("should not have made another API call")
	}
}

func TestFetchComments_ColdCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	f.comments["PROJ-1"] = []jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", Body: "hello", Created: time.Now()},
	}

	comments, cached, err := c.FetchComments(f, "PROJ-1", time.Time{}, false)
	if err != nil {
		t.Fatalf("FetchComments: %v", err)
	}
	if cached {
		t.Error("expected cache miss")
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if f.GetCommentsCalls["PROJ-1"] != 1 {
		t.Error("expected 1 API call")
	}
}

func TestFetchComments_WarmCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	now := time.Now().UTC().Truncate(time.Second)
	f.comments["PROJ-1"] = []jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", Body: "hello", Created: now},
	}

	// Populate cache
	c.FetchComments(f, "PROJ-1", time.Time{}, false)

	// Second call with preferCache=true
	comments, cached, _ := c.FetchComments(f, "PROJ-1", time.Time{}, true)
	if !cached {
		t.Error("expected cache hit")
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment from cache, got %d", len(comments))
	}
	if f.GetCommentsCalls["PROJ-1"] != 1 {
		t.Error("should not have made another API call")
	}
}

func TestFetchComments_PreferCacheButEmpty(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	f.comments["PROJ-1"] = []jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", Body: "hello", Created: time.Now()},
	}

	// preferCache=true but nothing in cache → falls back to API
	comments, cached, _ := c.FetchComments(f, "PROJ-1", time.Time{}, true)
	if cached {
		t.Error("expected cache miss when cache is empty")
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment from API, got %d", len(comments))
	}
}

func TestFetchIssueWithLinks_ColdCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "parent", Status: "To Do",
		IssueType: "Epic", Updated: time.Now(),
	}
	f.links["PROJ-1"] = []jira.IssueLink{
		{SourceKey: "PROJ-1", TargetKey: "PROJ-2", LinkType: "is parent of", Direction: "outward", TargetSummary: "child", TargetStatus: "To Do"},
	}

	detail, links, cached, err := c.FetchIssueWithLinks(f, "PROJ-1")
	if err != nil {
		t.Fatalf("FetchIssueWithLinks: %v", err)
	}
	if cached {
		t.Error("expected cache miss")
	}
	if detail.Key != "PROJ-1" {
		t.Errorf("got key %q", detail.Key)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
}

func TestFetchIssueWithLinks_WarmCache(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "parent", Status: "To Do",
		IssueType: "Epic", Updated: time.Now(),
	}
	f.links["PROJ-1"] = []jira.IssueLink{
		{SourceKey: "PROJ-1", TargetKey: "PROJ-2", LinkType: "Blocks", Direction: "outward", TargetSummary: "child", TargetStatus: "To Do"},
	}

	// Populate cache
	c.FetchIssueWithLinks(f, "PROJ-1")

	// Second call — cache hit
	_, _, cached, _ := c.FetchIssueWithLinks(f, "PROJ-1")
	if !cached {
		t.Error("expected cache hit on second call")
	}
	if f.GetIssueWithLinksCalls["PROJ-1"] != 1 {
		t.Error("should not have made another API call")
	}
}

// TestFullGetIssueFlow simulates the get-issue command flow:
// fetch detail → fetch links → fetch comments, all cache-aware.
func TestFullGetIssueFlow(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	now := time.Now().UTC().Truncate(time.Second)
	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "full flow test", Status: "In Progress",
		IssueType: "Task", Updated: now, Assignee: "alice",
		ParentKey: "PROJ-100", ParentSummary: "epic",
	}
	f.links["PROJ-1"] = []jira.IssueLink{
		{SourceKey: "PROJ-1", TargetKey: "PROJ-2", LinkType: "Blocks", Direction: "outward",
			TargetSummary: "blocked task", TargetStatus: "To Do"},
	}
	f.comments["PROJ-1"] = []jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", Body: "working on it", Created: now},
		{ID: "c2", IssueKey: "PROJ-1", AuthorName: "bob", Body: "review needed", Created: now.Add(time.Hour)},
	}

	// === First run: everything from API ===
	detail, detailCached, _ := c.FetchIssue(f, "PROJ-1", time.Time{})
	if detailCached {
		t.Error("run 1: detail should be from API")
	}

	_, links, linksCached, _ := c.FetchIssueWithLinks(f, "PROJ-1")
	// Detail was just cached by FetchIssue, and links were fetched
	if !linksCached {
		// Links were not in cache before FetchIssueWithLinks, but detail was.
		// FetchIssueWithLinks requires BOTH detail and links in cache for a hit.
		// Since we didn't have links cached, it fetched from API.
		// That's expected on first run.
	}
	if len(links) != 1 {
		t.Fatalf("run 1: expected 1 link, got %d", len(links))
	}

	comments, commentsCached, _ := c.FetchComments(f, "PROJ-1", time.Time{}, false)
	if commentsCached {
		t.Error("run 1: comments should be from API")
	}
	if len(comments) != 2 {
		t.Fatalf("run 1: expected 2 comments, got %d", len(comments))
	}

	apiCallsRun1 := f.totalAPICalls()
	t.Logf("run 1: %d API calls", apiCallsRun1)

	// === Second run: everything from cache ===
	detail, detailCached, _ = c.FetchIssue(f, "PROJ-1", time.Time{})
	if !detailCached {
		t.Error("run 2: detail should be from cache")
	}
	if detail.Assignee != "alice" {
		t.Errorf("run 2: detail should preserve all fields, assignee=%q", detail.Assignee)
	}

	_, _, linksCached, _ = c.FetchIssueWithLinks(f, "PROJ-1")
	if !linksCached {
		t.Error("run 2: links should be from cache")
	}

	comments, commentsCached, _ = c.FetchComments(f, "PROJ-1", time.Time{}, true)
	if !commentsCached {
		t.Error("run 2: comments should be from cache")
	}

	apiCallsRun2 := f.totalAPICalls() - apiCallsRun1
	if apiCallsRun2 != 0 {
		t.Errorf("run 2: expected 0 API calls, got %d", apiCallsRun2)
	}

	// === Third run: issue updated in JIRA, stale detail triggers re-fetch ===
	newerTime := now.Add(2 * time.Hour)
	f.issues["PROJ-1"] = &jira.IssueDetail{
		Key: "PROJ-1", Summary: "updated summary", Status: "In Review",
		IssueType: "Task", Updated: newerTime, Assignee: "alice",
	}

	detail, detailCached, _ = c.FetchIssue(f, "PROJ-1", newerTime)
	if detailCached {
		t.Error("run 3: detail should be from API (stale)")
	}
	if detail.Summary != "updated summary" {
		t.Errorf("run 3: expected updated summary, got %q", detail.Summary)
	}

	apiCallsRun3 := f.totalAPICalls() - apiCallsRun1 - apiCallsRun2
	if apiCallsRun3 != 1 {
		t.Errorf("run 3: expected 1 API call (re-fetch detail), got %d", apiCallsRun3)
	}
}

// TestBatchFetchPattern simulates the weekly-status batch pattern:
// search returns N issues, check freshness in batch, only fetch stale ones.
func TestBatchFetchPattern(t *testing.T) {
	c := openTestCache(t)
	f := newMockFetcher()

	t1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)

	// Pre-populate cache with 3 issues at t1
	for i := 1; i <= 3; i++ {
		key := fmt.Sprintf("PROJ-%d", i)
		detail := &jira.IssueDetail{
			Key: key, Summary: fmt.Sprintf("issue %d", i),
			Status: "To Do", IssueType: "Task", Updated: t1,
		}
		c.UpsertIssueDetail(detail)
		// Also put in mock for potential re-fetch
		f.issues[key] = &jira.IssueDetail{
			Key: key, Summary: fmt.Sprintf("issue %d updated", i),
			Status: "In Progress", IssueType: "Task", Updated: t2,
		}
	}

	// Simulate search results: PROJ-1 unchanged, PROJ-2 and PROJ-3 updated
	searchResults := map[string]time.Time{
		"PROJ-1": t1, // same as cached — fresh
		"PROJ-2": t2, // newer — stale
		"PROJ-3": t2, // newer — stale
	}

	freshKeys := c.GetFreshDetailKeys(searchResults)

	if !freshKeys["PROJ-1"] {
		t.Error("PROJ-1 should be fresh")
	}
	if freshKeys["PROJ-2"] {
		t.Error("PROJ-2 should be stale")
	}
	if freshKeys["PROJ-3"] {
		t.Error("PROJ-3 should be stale")
	}

	// Only fetch stale issues
	var fetchCount int
	for key, updated := range searchResults {
		if freshKeys[key] {
			// Use cache
			_, ok := c.GetIssueDetail(key, updated)
			if !ok {
				t.Errorf("expected cache hit for fresh key %s", key)
			}
		} else {
			// Fetch from API
			detail, cached, err := c.FetchIssue(f, key, updated)
			if err != nil {
				t.Fatalf("FetchIssue %s: %v", key, err)
			}
			if cached {
				t.Errorf("expected API fetch for stale key %s", key)
			}
			if detail.Status != "In Progress" {
				t.Errorf("%s: expected updated status, got %q", key, detail.Status)
			}
			fetchCount++
		}
	}

	if fetchCount != 2 {
		t.Errorf("expected 2 API fetches, got %d", fetchCount)
	}
	if f.GetIssueCalls["PROJ-1"] != 0 {
		t.Error("PROJ-1 should not have been fetched from API")
	}
}
