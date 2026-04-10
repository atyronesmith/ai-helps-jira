package cache

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func openTestCache(t *testing.T) *Cache {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Cache{db: db}
}

// --- Issue CRUD ---

func TestUpsertAndGetIssues(t *testing.T) {
	c := openTestCache(t)

	issues := []jira.Issue{
		{Key: "PROJ-1", Status: "In Progress", Priority: "High", Summary: "task one", Updated: time.Now(), Assignee: "alice"},
		{Key: "PROJ-2", Status: "To Do", Priority: "Medium", Summary: "task two", Updated: time.Now(), Assignee: "bob"},
	}

	if err := c.UpsertIssues("PROJ", issues); err != nil {
		t.Fatalf("UpsertIssues: %v", err)
	}

	got, err := c.GetIssues("PROJ")
	if err != nil {
		t.Fatalf("GetIssues: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(got))
	}
}

func TestUpsertIssues_Update(t *testing.T) {
	c := openTestCache(t)

	issues := []jira.Issue{
		{Key: "PROJ-1", Status: "To Do", Priority: "Low", Summary: "original", Updated: time.Now(), Assignee: "alice"},
	}
	c.UpsertIssues("PROJ", issues)

	// Update same key
	issues[0].Status = "In Progress"
	issues[0].Summary = "updated"
	c.UpsertIssues("PROJ", issues)

	got, _ := c.GetIssues("PROJ")
	if len(got) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(got))
	}
	if got[0].Status != "In Progress" {
		t.Errorf("expected status 'In Progress', got %q", got[0].Status)
	}
	if got[0].Summary != "updated" {
		t.Errorf("expected summary 'updated', got %q", got[0].Summary)
	}
}

func TestRemoveDone(t *testing.T) {
	c := openTestCache(t)

	issues := []jira.Issue{
		{Key: "PROJ-1", Status: "Done", Priority: "Low", Summary: "done task", Updated: time.Now()},
		{Key: "PROJ-2", Status: "In Progress", Priority: "High", Summary: "active task", Updated: time.Now()},
	}
	c.UpsertIssues("PROJ", issues)

	n, err := c.RemoveDone("PROJ")
	if err != nil {
		t.Fatalf("RemoveDone: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 removed, got %d", n)
	}

	got, _ := c.GetIssues("PROJ")
	if len(got) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(got))
	}
	if got[0].Key != "PROJ-2" {
		t.Errorf("expected PROJ-2, got %s", got[0].Key)
	}
}

// --- Issue Detail ---

func TestUpsertAndGetIssueDetail(t *testing.T) {
	c := openTestCache(t)

	detail := &jira.IssueDetail{
		Key:           "PROJ-1",
		Summary:       "test issue",
		Description:   "a description",
		Status:        "In Progress",
		Priority:      "High",
		IssueType:     "Task",
		Labels:        []string{"backend", "urgent"},
		Assignee:      "alice",
		AssigneeID:    "abc123",
		ParentKey:     "PROJ-100",
		ParentSummary: "parent epic",
		Updated:       time.Now().UTC().Truncate(time.Second),
	}

	if err := c.UpsertIssueDetail(detail); err != nil {
		t.Fatalf("UpsertIssueDetail: %v", err)
	}

	// Get with zero time — return regardless of age
	got, ok := c.GetIssueDetail("PROJ-1", time.Time{})
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Key != "PROJ-1" {
		t.Errorf("key: got %q, want PROJ-1", got.Key)
	}
	if got.Summary != "test issue" {
		t.Errorf("summary: got %q", got.Summary)
	}
	if got.Description != "a description" {
		t.Errorf("description: got %q", got.Description)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "backend" {
		t.Errorf("labels: got %v", got.Labels)
	}
	if got.Assignee != "alice" {
		t.Errorf("assignee: got %q", got.Assignee)
	}
	if got.ParentKey != "PROJ-100" {
		t.Errorf("parent: got %q", got.ParentKey)
	}
}

func TestGetIssueDetail_Freshness(t *testing.T) {
	c := openTestCache(t)

	cachedTime := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	detail := &jira.IssueDetail{
		Key:       "PROJ-1",
		Summary:   "test",
		Status:    "To Do",
		IssueType: "Task",
		Updated:   cachedTime,
	}
	c.UpsertIssueDetail(detail)

	// Same time — fresh
	got, ok := c.GetIssueDetail("PROJ-1", cachedTime)
	if !ok {
		t.Error("expected cache hit when knownUpdated == cachedUpdated")
	}
	if got == nil {
		t.Fatal("got nil detail")
	}

	// Older known time — still fresh
	_, ok = c.GetIssueDetail("PROJ-1", cachedTime.Add(-time.Hour))
	if !ok {
		t.Error("expected cache hit when knownUpdated < cachedUpdated")
	}

	// Newer known time — stale
	_, ok = c.GetIssueDetail("PROJ-1", cachedTime.Add(time.Hour))
	if ok {
		t.Error("expected cache miss when knownUpdated > cachedUpdated")
	}
}

func TestGetIssueDetail_NotFound(t *testing.T) {
	c := openTestCache(t)

	_, ok := c.GetIssueDetail("NOPE-1", time.Time{})
	if ok {
		t.Error("expected cache miss for nonexistent key")
	}
}

func TestUpsertIssueDetails_Batch(t *testing.T) {
	c := openTestCache(t)

	details := []*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "one", Status: "To Do", IssueType: "Task", Updated: time.Now()},
		{Key: "PROJ-2", Summary: "two", Status: "To Do", IssueType: "Bug", Updated: time.Now()},
		{Key: "PROJ-3", Summary: "three", Status: "To Do", IssueType: "Story", Updated: time.Now()},
	}

	if err := c.UpsertIssueDetails(details); err != nil {
		t.Fatalf("UpsertIssueDetails: %v", err)
	}

	for _, d := range details {
		got, ok := c.GetIssueDetail(d.Key, time.Time{})
		if !ok {
			t.Errorf("expected cache hit for %s", d.Key)
			continue
		}
		if got.Summary != d.Summary {
			t.Errorf("%s: summary got %q, want %q", d.Key, got.Summary, d.Summary)
		}
	}
}

func TestGetFreshDetailKeys(t *testing.T) {
	c := openTestCache(t)

	t1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

	c.UpsertIssueDetails([]*jira.IssueDetail{
		{Key: "PROJ-1", Summary: "one", Status: "To Do", IssueType: "Task", Updated: t2},
		{Key: "PROJ-2", Summary: "two", Status: "To Do", IssueType: "Task", Updated: t1},
	})

	updatedByKey := map[string]time.Time{
		"PROJ-1": t2,         // same — fresh
		"PROJ-2": t2,         // newer than cached — stale
		"PROJ-3": time.Now(), // not in cache at all
	}

	fresh := c.GetFreshDetailKeys(updatedByKey)

	if !fresh["PROJ-1"] {
		t.Error("PROJ-1 should be fresh")
	}
	if fresh["PROJ-2"] {
		t.Error("PROJ-2 should be stale")
	}
	if fresh["PROJ-3"] {
		t.Error("PROJ-3 should not be fresh (not cached)")
	}
}

// --- Comments ---

func TestUpsertAndGetComments(t *testing.T) {
	c := openTestCache(t)

	now := time.Now().UTC().Truncate(time.Second)
	comments := []jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", AuthorEmail: "alice@co.com", Body: "first comment", Created: now},
		{ID: "c2", IssueKey: "PROJ-1", AuthorName: "bob", Body: "second comment", Created: now.Add(time.Hour)},
		{ID: "c3", IssueKey: "PROJ-2", AuthorName: "carol", Body: "other issue", Created: now},
	}

	if err := c.UpsertComments(comments); err != nil {
		t.Fatalf("UpsertComments: %v", err)
	}

	// Get comments for PROJ-1 since zero time
	got, err := c.GetCommentsByKeys([]string{"PROJ-1"}, time.Time{})
	if err != nil {
		t.Fatalf("GetCommentsByKeys: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 comments for PROJ-1, got %d", len(got))
	}

	// Get comments for both keys
	got, _ = c.GetCommentsByKeys([]string{"PROJ-1", "PROJ-2"}, time.Time{})
	if len(got) != 3 {
		t.Fatalf("expected 3 total comments, got %d", len(got))
	}
}

func TestGetCommentsByKeys_SinceFilter(t *testing.T) {
	c := openTestCache(t)

	t1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)

	comments := []jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", Body: "old", Created: t1},
		{ID: "c2", IssueKey: "PROJ-1", AuthorName: "bob", Body: "new", Created: t2},
	}
	c.UpsertComments(comments)

	since := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	got, _ := c.GetCommentsByKeys([]string{"PROJ-1"}, since)
	if len(got) != 1 {
		t.Fatalf("expected 1 comment since Apr 3, got %d", len(got))
	}
	if got[0].ID != "c2" {
		t.Errorf("expected c2, got %s", got[0].ID)
	}
}

func TestGetCommentsByKeys_Empty(t *testing.T) {
	c := openTestCache(t)

	got, err := c.GetCommentsByKeys(nil, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty keys, got %v", got)
	}
}

// --- Issue Links ---

func TestUpsertAndGetIssueLinks(t *testing.T) {
	c := openTestCache(t)

	links := []jira.IssueLink{
		{SourceKey: "PROJ-1", TargetKey: "PROJ-2", LinkType: "Blocks", Direction: "outward", TargetSummary: "blocked task", TargetStatus: "To Do", TargetType: "Task"},
		{SourceKey: "PROJ-1", TargetKey: "PROJ-3", LinkType: "Relates", Direction: "outward", TargetSummary: "related task", TargetStatus: "In Progress", TargetType: "Story"},
	}

	if err := c.UpsertIssueLinks(links); err != nil {
		t.Fatalf("UpsertIssueLinks: %v", err)
	}

	got, err := c.GetIssueLinks("PROJ-1")
	if err != nil {
		t.Fatalf("GetIssueLinks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 links, got %d", len(got))
	}

	// No links for different source
	got, _ = c.GetIssueLinks("PROJ-99")
	if len(got) != 0 {
		t.Errorf("expected 0 links for PROJ-99, got %d", len(got))
	}
}

// --- Weekly Cache ---

func TestWeeklyCache(t *testing.T) {
	c := openTestCache(t)

	key := "weekly:PROJ:2026-04-01:2026-04-07"
	jsonData := `{"user_name":"alice","projects":[]}`

	// Miss
	_, _, ok := c.GetWeeklyCache(key)
	if ok {
		t.Error("expected cache miss")
	}

	// Set
	if err := c.SetWeeklyCache(key, jsonData); err != nil {
		t.Fatalf("SetWeeklyCache: %v", err)
	}

	// Hit
	got, cachedAt, ok := c.GetWeeklyCache(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != jsonData {
		t.Errorf("data mismatch: got %q", got)
	}
	if cachedAt.IsZero() {
		t.Error("cachedAt should not be zero")
	}

	// Overwrite
	newJSON := `{"user_name":"bob","projects":[]}`
	c.SetWeeklyCache(key, newJSON)
	got, _, _ = c.GetWeeklyCache(key)
	if got != newJSON {
		t.Errorf("expected overwritten data, got %q", got)
	}
}

// --- Digest Log ---

func TestDigestLog(t *testing.T) {
	c := openTestCache(t)

	key := "keys:PROJ-1"

	// No prior run
	last := c.LastDigestRun(key)
	if !last.IsZero() {
		t.Error("expected zero time for first run")
	}

	// Log a run
	if err := c.LogDigestRun(key); err != nil {
		t.Fatalf("LogDigestRun: %v", err)
	}

	last = c.LastDigestRun(key)
	if last.IsZero() {
		t.Error("expected non-zero time after logging")
	}
	if time.Since(last) > 5*time.Second {
		t.Error("last run should be recent")
	}
}

// --- Fetch Log ---

func TestFetchLog(t *testing.T) {
	c := openTestCache(t)

	last := c.LastFetch("PROJ", "currentUser()")
	if !last.IsZero() {
		t.Error("expected zero time for no prior fetch")
	}

	c.LogFetch("PROJ", "currentUser()")

	last = c.LastFetch("PROJ", "currentUser()")
	if last.IsZero() {
		t.Error("expected non-zero time after fetch log")
	}
}

// --- Stats ---

func TestStats(t *testing.T) {
	c := openTestCache(t)

	// Empty stats
	s := c.Stats()
	if s.Issues != 0 || s.Comments != 0 || s.IssueDetails != 0 {
		t.Error("expected all zeros for empty cache")
	}

	// Add some data
	c.UpsertIssues("PROJ", []jira.Issue{
		{Key: "PROJ-1", Status: "To Do", Priority: "Low", Summary: "one", Updated: time.Now()},
		{Key: "PROJ-2", Status: "To Do", Priority: "Low", Summary: "two", Updated: time.Now()},
	})
	c.UpsertIssueDetail(&jira.IssueDetail{Key: "PROJ-1", Summary: "one", Status: "To Do", IssueType: "Task", Updated: time.Now()})
	c.UpsertComments([]jira.Comment{
		{ID: "c1", IssueKey: "PROJ-1", AuthorName: "alice", Body: "hi", Created: time.Now()},
	})

	s = c.Stats()
	if s.Issues != 2 {
		t.Errorf("issues: got %d, want 2", s.Issues)
	}
	if s.IssueDetails != 1 {
		t.Errorf("issue_details: got %d, want 1", s.IssueDetails)
	}
	if s.Comments != 1 {
		t.Errorf("comments: got %d, want 1", s.Comments)
	}
}

// --- Clear ---

func TestClear(t *testing.T) {
	c := openTestCache(t)

	c.UpsertIssues("PROJ", []jira.Issue{
		{Key: "PROJ-1", Status: "To Do", Priority: "Low", Summary: "one", Updated: time.Now()},
	})
	c.LogFetch("PROJ", "currentUser()")

	if err := c.Clear("PROJ"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	issues, _ := c.GetIssues("PROJ")
	if len(issues) != 0 {
		t.Errorf("expected 0 issues after clear, got %d", len(issues))
	}
}
