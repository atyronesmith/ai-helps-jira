package cache

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestMutationFencesEveryStaleWriteAcrossHandles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "auth@example.test", JiraProject: "APP"}
	stale, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	mutator, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mutator.Close()
	if err := mutator.InvalidateIssue("APP-1"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		write func() error
	}{
		{"issues", func() error { return stale.UpsertIssues("APP", []jira.Issue{summaryIssue("APP-1", "me")}) }},
		{"details", func() error { return stale.UpsertIssueDetail(&jira.IssueDetail{Key: "APP-1"}) }},
		{"comments", func() error { return stale.UpsertComments([]jira.Comment{{ID: "1", IssueKey: "APP-1"}}) }},
		{"links", func() error {
			return stale.UpsertIssueLinks([]jira.IssueLink{{SourceKey: "APP-1", TargetKey: "APP-2"}})
		}},
		{"weekly", func() error { return stale.SetWeeklyCache("report", "{}") }},
		{"digest checkpoint", func() error { return stale.LogDigestRun("query") }},
		{"summary checkpoint", func() error { return stale.LogFetch("APP", "me") }},
		{"projects", func() error { return stale.UpsertProjects([]jira.Project{{Key: "APP"}}) }},
		{"clear", func() error { return stale.Clear("APP") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); !errors.Is(err, ErrCacheChanged) {
				t.Fatalf("stale operation escaped mutation fence: %v", err)
			}
		})
	}
	fresh, err := stale.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.UpsertIssues("APP", []jira.Issue{summaryIssue("APP-1", "me")}); err != nil {
		t.Fatal(err)
	}
	if rows, err := fresh.GetIssues("APP"); err != nil || len(rows) != 0 {
		t.Fatalf("a new operation trusted a lagging post-mutation search row: %+v %v", rows, err)
	}
	if err := fresh.UpsertIssueDetail(&jira.IssueDetail{Key: "APP-1", Summary: "post mutation", Updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, ok := mutator.GetIssueDetail("APP-1", time.Time{}); !ok || got.Summary != "post mutation" {
		t.Fatalf("fresh operation couldn't refill: %+v %v", got, ok)
	}
}

type blockingFetcher struct {
	started chan struct{}
	release chan struct{}
}

func (f *blockingFetcher) GetIssue(key string) (*jira.IssueDetail, error) {
	close(f.started)
	<-f.release
	return &jira.IssueDetail{Key: key, Summary: "pre-mutation", Updated: time.Now()}, nil
}
func (f *blockingFetcher) GetComments(string) ([]jira.Comment, error) { return nil, nil }
func (f *blockingFetcher) GetIssueWithLinks(string) (*jira.IssueDetail, []jira.IssueLink, error) {
	panic("unused")
}

func TestFetchHelperCapturesFenceBeforeNetwork(t *testing.T) {
	c := openTestCache(t)
	f := &blockingFetcher{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, _, err := c.FetchIssue(f, "APP-1", time.Time{})
		done <- err
	}()
	<-f.started
	if err := c.InvalidateIssue("APP-1"); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, ok := c.GetIssueDetail("APP-1", time.Time{}); ok {
		t.Fatalf("in-flight helper refilled stale data: %+v", got)
	}
}

func TestCanonicalFreshnessUsesInstantsAndActualProjects(t *testing.T) {
	c := openTestCache(t)
	i := summaryIssue("OLD-1", "me")
	i.Project = "ACTUAL"
	i.Updated = i.Updated.Add(500 * time.Nanosecond)
	if err := c.UpsertIssues("DEFAULT", []jira.Issue{i, {Key: "OTHER-2", StatusCategory: "new"}}); err != nil {
		t.Fatal(err)
	}
	older := i
	older.Updated = i.Updated.Add(-500 * time.Nanosecond)
	older.Project, older.Summary = "WRONG", "stale"
	if err := c.UpsertIssues("DEFAULT", []jira.Issue{older}); err != nil {
		t.Fatal(err)
	}
	rows, err := c.GetIssues("ACTUAL")
	if err != nil || len(rows) != 1 || rows[0].Summary != i.Summary || !rows[0].Updated.Equal(i.Updated) || rows[0].Project != "ACTUAL" {
		t.Fatalf("stale canonical overwrite: %+v %v", rows, err)
	}
	rows, err = c.GetIssues("OTHER")
	if err != nil || len(rows) != 1 || rows[0].Project != "OTHER" {
		t.Fatalf("missing actual-project fallback: %+v %v", rows, err)
	}
	d := &jira.IssueDetail{Key: i.Key, Summary: "newer direct", Updated: i.Updated.Add(time.Nanosecond), StatusCategory: "done", Project: "ACTUAL"}
	if err := c.UpsertIssueDetail(d); err != nil {
		t.Fatal(err)
	}
	staleDetail := *d
	staleDetail.Summary, staleDetail.StatusCategory, staleDetail.Updated = "stale detail", "new", older.Updated.In(time.FixedZone("offset", 3600))
	if err := c.UpsertIssueDetail(&staleDetail); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.GetIssueDetail(d.Key, time.Time{}); !ok || got.Summary != d.Summary {
		t.Fatalf("stale detail overwrite: %+v %v", got, ok)
	}
	rows, err = c.GetIssues("ACTUAL")
	if err != nil || len(rows) != 0 {
		t.Fatalf("direct closed category didn't update canonical work: %+v %v", rows, err)
	}
}

func TestConcurrentFirstScopeOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "auth@example.test"}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := OpenForConfig(cfg)
			if err == nil {
				err = c.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
