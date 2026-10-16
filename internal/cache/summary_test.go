package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func summaryIssue(key, owner string) jira.Issue {
	return jira.Issue{Key: key, Project: "APP", Assignee: owner, Summary: key, Status: "Custom open", StatusCategory: "indeterminate", Updated: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
}

func summaryFetch(issues ...jira.Issue) func(context.Context, []string) ([]jira.BoardInfo, []jira.Issue, error) {
	return func(context.Context, []string) ([]jira.BoardInfo, []jira.Issue, error) {
		return []jira.BoardInfo{{Name: "Team", BoardType: "scrum", SprintName: "First", Issues: issues}, {Name: "Empty", BoardType: "kanban"}}, issues, nil
	}
}

func TestSummaryReconcilesRemovalsAndIsolatesMembership(t *testing.T) {
	c := openTestCache(t)
	alice := &config.Config{JiraProject: "APP", JiraUser: "alice@example.test"}
	bob := &config.Config{JiraProject: "APP", JiraUser: "bob@example.test"}
	a, b := summaryIssue("APP-1", alice.JiraUser), summaryIssue("APP-2", bob.JiraUser)
	if _, _, _, err := LoadSummary(t.Context(), c, alice, false, summaryFetch(a)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadSummary(t.Context(), c, bob, false, summaryFetch(b)); err != nil {
		t.Fatal(err)
	}
	// A general query warms canonical data without adding summary membership.
	if err := c.UpsertIssues("IGNORED", []jira.Issue{summaryIssue("APP-3", alice.JiraUser)}); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []*config.Config{alice, bob} {
		boards, rows, _, err := LoadSummary(t.Context(), c, cfg, true, nil)
		if err != nil || len(rows) != 1 || rows[0].Assignee != cfg.JiraUser || len(boards) != 2 {
			t.Fatalf("cached owner %s: boards=%+v rows=%+v err=%v", cfg.JiraUser, boards, rows, err)
		}
	}
	// The complete next result omits a closed/reassigned/moved/invisible issue.
	boards, rows, _, err := LoadSummary(t.Context(), c, alice, false, summaryFetch())
	if err != nil || len(rows) != 0 || len(boards) != 2 || len(boards[1].Issues) != 0 {
		t.Fatalf("removal failed: boards=%+v rows=%+v err=%v", boards, rows, err)
	}
	_, rows, _, err = LoadSummary(t.Context(), c, bob, true, nil)
	if err != nil || len(rows) != 1 || rows[0].Key != b.Key {
		t.Fatalf("another owner's reconciliation changed Bob: %+v, %v", rows, err)
	}
	var n int
	if err := c.db.QueryRow("SELECT count(*) FROM issues WHERE key=?", a.Key).Scan(&n); err != nil || n != 1 {
		t.Fatalf("membership removal deleted canonical issue: n=%d err=%v", n, err)
	}
	if _, _, _, err := LoadSummary(t.Context(), c, &config.Config{JiraProject: "ELSE", JiraUser: alice.JiraUser}, true, nil); !errors.Is(err, ErrNoSummary) {
		t.Fatalf("other project inherited membership: %v", err)
	}
}

func TestSummaryFailurePreservesCheckpointAndRows(t *testing.T) {
	for _, failure := range []string{"fetch", "storage", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			c := openTestCache(t)
			cfg := &config.Config{JiraProject: "APP"}
			i := summaryIssue("APP-1", "me")
			_, _, before, err := LoadSummary(t.Context(), c, cfg, false, summaryFetch(i))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "storage" {
				// Fail after canonical upserts and membership replacement have begun.
				if _, err := c.db.Exec(`CREATE TRIGGER fail_checkpoint BEFORE UPDATE ON summary_snapshots BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			fetch := func(context.Context, []string) ([]jira.BoardInfo, []jira.Issue, error) {
				if failure == "fetch" {
					return nil, nil, fmt.Errorf("upstream failed on later page")
				}
				if failure == "cancel" {
					cancel()
				}
				return nil, []jira.Issue{summaryIssue("APP-2", "me")}, nil
			}
			if _, _, _, err := LoadSummary(ctx, c, cfg, false, fetch); err == nil {
				t.Fatal("expected failed refresh")
			}
			_, rows, after, err := LoadSummary(t.Context(), c, cfg, true, nil)
			if err != nil || len(rows) != 1 || rows[0].Key != i.Key || !before.Equal(after) || !c.LastFetch("APP", cfg.Assignee()).Equal(before) {
				t.Fatalf("failed refresh changed snapshot: %+v %v %v err=%v", rows, before, after, err)
			}
			var count int
			if err := c.db.QueryRow("SELECT count(*) FROM issues WHERE key='APP-2'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial canonical fill survived failure: %d %v", count, err)
			}
		})
	}
}

func TestSummaryMutationDuringFetchCannotRefillOrAdvanceCheckpoint(t *testing.T) {
	c := openTestCache(t)
	cfg := &config.Config{JiraProject: "APP"}
	i := summaryIssue("APP-1", "me")
	if _, _, _, err := LoadSummary(t.Context(), c, cfg, false, summaryFetch(i)); err != nil {
		t.Fatal(err)
	}
	before := c.LastFetch("APP", cfg.Assignee())
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, _, err := LoadSummary(t.Context(), c, cfg, false, func(context.Context, []string) ([]jira.BoardInfo, []jira.Issue, error) {
			close(started)
			<-release
			return nil, []jira.Issue{i}, nil
		})
		result <- err
	}()
	<-started
	if err := c.InvalidateIssue(i.Key); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrCacheChanged) {
		t.Fatalf("stale fill err=%v", err)
	}
	if !before.Equal(c.LastFetch("APP", cfg.Assignee())) {
		t.Fatal("mutation-fenced refresh advanced checkpoint")
	}
	if _, _, _, err := LoadSummary(t.Context(), c, cfg, true, nil); !errors.Is(err, ErrNoSummary) {
		t.Fatalf("mutation left cache-only snapshot usable: %v", err)
	}
	// A fresh operation carries the invalidation ledger to direct revalidation.
	for n := 0; n < 2; n++ {
		_, rows, _, err := LoadSummary(t.Context(), c, cfg, false, func(_ context.Context, keys []string) ([]jira.BoardInfo, []jira.Issue, error) {
			if len(keys) != 1 || keys[0] != i.Key {
				t.Fatalf("missing lag protection on refresh %d: %v", n, keys)
			}
			return nil, nil, nil
		})
		if err != nil || len(rows) != 0 {
			t.Fatalf("revalidated refresh: %+v %v", rows, err)
		}
	}
}

func TestSummaryRejectsUnverifiedAndStaleCanonicalRows(t *testing.T) {
	c := openTestCache(t)
	cfg := &config.Config{JiraProject: "APP"}
	i := summaryIssue("APP-1", "me")
	newer := i
	newer.Updated = i.Updated.Add(time.Nanosecond)
	if err := c.UpsertIssues("IGNORED", []jira.Issue{newer}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadSummary(t.Context(), c, cfg, false, summaryFetch(i)); !errors.Is(err, ErrCacheChanged) {
		t.Fatalf("older canonical refresh accepted: %v", err)
	}
	for _, bad := range []jira.Issue{{Key: "ELSE-1", Project: "ELSE", StatusCategory: "new"}, {Key: "APP-1", Project: "APP"}, {Key: "APP-1", Project: "APP", StatusCategory: "done"}} {
		if _, _, _, err := LoadSummary(t.Context(), c, cfg, false, summaryFetch(bad)); err == nil {
			t.Fatalf("accepted unverified row: %+v", bad)
		}
	}
}

func TestNewCanonicalOwnershipInvalidatesPreviousSummary(t *testing.T) {
	c := openTestCache(t)
	cfg := &config.Config{JiraProject: "APP", JiraUser: "alice@example.test"}
	i := summaryIssue("APP-1", cfg.JiraUser)
	if _, _, _, err := LoadSummary(t.Context(), c, cfg, false, summaryFetch(i)); err != nil {
		t.Fatal(err)
	}
	i.Assignee, i.Updated = "bob@example.test", i.Updated.Add(time.Second)
	if err := c.UpsertIssues("APP", []jira.Issue{i}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadSummary(t.Context(), c, cfg, true, nil); !errors.Is(err, ErrNoSummary) {
		t.Fatalf("offline snapshot retained disproved ownership: %v", err)
	}
}
