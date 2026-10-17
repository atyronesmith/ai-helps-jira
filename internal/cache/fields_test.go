package cache

import (
	"context"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

func TestFieldCatalogEmptyTimestampAndAtomicReplace(t *testing.T) {
	c := openTestCache(t)
	ctx := context.Background()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, at, err := c.GetFields(ctx); err != nil || len(got) != 0 || !at.IsZero() {
		t.Fatalf("initial=(%v,%v,%v)", got, at, err)
	}
	seed := []fieldmeta.Field{{ID: "customfield_1", Name: "Budget", Aliases: []string{"cost"}}}
	if err := c.ReplaceFields(ctx, seed, stamp); err != nil {
		t.Fatal(err)
	}
	if got, at, err := c.GetFields(ctx); err != nil || len(got) != 1 || got[0].Name != "Budget" || !at.Equal(stamp) {
		t.Fatalf("stored=(%v,%v,%v)", got, at, err)
	}
	err := c.ReplaceFields(ctx, []fieldmeta.Field{{ID: "customfield_2", Name: "New"}, {Name: "invalid"}}, stamp.Add(time.Hour))
	if err == nil {
		t.Fatal("expected invalid replace failure")
	}
	got, at, err := c.GetFields(ctx)
	if err != nil || len(got) != 1 || got[0].ID != "customfield_1" || !at.Equal(stamp) {
		t.Fatalf("failed replace changed catalog: %v %v %v", got, at, err)
	}
	if err := c.ReplaceFields(ctx, nil, stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, at, err = c.GetFields(ctx)
	if err != nil || len(got) != 0 || !at.Equal(stamp.Add(time.Hour)) {
		t.Fatalf("empty successful catalog lost timestamp: %v %v %v", got, at, err)
	}
}

func TestFieldCatalogIsScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	manager, err := OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	cfg1 := &config.Config{JiraServer: "https://one.example", JiraEmail: "a@example"}
	cfg2 := &config.Config{JiraServer: "https://two.example", JiraEmail: "a@example"}
	a, err := manager.ForConfig(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.ForConfig(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ReplaceFields(context.Background(), []fieldmeta.Field{{ID: "customfield_1", Name: "A"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _, err := b.GetFields(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("scope leaked fields: %v, %v", got, err)
	}
}

func TestFieldCatalogConcurrentSnapshotsKeepMatchingTimestamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "account@example.test"}
	writer, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := func(n int) error {
		at := base.Add(time.Duration(n) * time.Second)
		return writer.ReplaceFields(context.Background(), []fieldmeta.Field{{ID: "customfield_1", Name: at.Format(time.RFC3339Nano)}}, at)
	}
	if err := store(0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for n := 1; n < 40; n++ {
			if err := store(n); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 40 {
		fields, at, err := reader.GetFields(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(fields) != 1 || fields[0].Name != at.Format(time.RFC3339Nano) {
			t.Fatalf("mixed metadata snapshot: %+v at %v", fields, at)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
