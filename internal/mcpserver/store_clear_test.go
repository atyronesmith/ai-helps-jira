package mcpserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestResultStoreClearRemovesEveryStoredRowAndPersists(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, "clear-owner", 0, 0)
	shared := openTestStore(t, dir, "clear-owner", 0, 0)
	other := openTestStore(t, dir, "clear-other", 0, 0)

	// Raw rows include every known type plus unknown and malformed data. Clear
	// operates on storage rows and must not decode historical payloads.
	known := []ResultType{ResultSummary, ResultQuery, ResultDigest, ResultEnrich, ResultCreateEpic, ResultWeeklyStatus, ResultFindSimilar, ResultConfluenceAnalytics, ResultAttempt}
	for i, typ := range known {
		if _, err := store.db.Exec("INSERT INTO results(id,type,title,data,created_at) VALUES(?,?,?,?,?)", string(rune('a'+i)), string(typ), "row", "{}", time.Now().UTC().Format(resultTimestampFormat)); err != nil {
			t.Fatal(err)
		}
	}
	for i, typ := range []string{"unknown-type", string(ResultQuery)} {
		payload := "{}"
		if i == 1 {
			payload = "not-json"
		}
		if _, err := store.db.Exec("INSERT INTO results(id,type,title,data,created_at) VALUES(?,?,?,?,?)", string(rune('x'+i)), typ, "bad", payload, "invalid-date"); err != nil {
			t.Fatal(err)
		}
	}
	otherID := other.Save(ResultQuery, "other owner", &QueryResultData{})

	if n, err := store.Clear(context.Background()); err != nil || n != len(known)+2 {
		t.Fatalf("Clear() = (%d, %v), want (%d, nil)", n, err, len(known)+2)
	}
	if n, err := store.Clear(context.Background()); err != nil || n != 0 {
		t.Fatalf("second Clear() = (%d, %v), want (0, nil)", n, err)
	}
	if got := len(store.List()); got != 0 {
		t.Fatalf("rows after clear = %d, want 0", got)
	}
	if got := len(shared.List()); got != 0 {
		t.Fatalf("second handle saw %d rows after clear, want 0", got)
	}
	if _, ok := other.Get(otherID); !ok {
		t.Fatal("clear affected another store instance")
	}
	afterID := store.Save(ResultAttempt, "after clear", &AttemptResultData{})
	if _, ok := store.Get(afterID); !ok {
		t.Fatal("save after clear was unavailable")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, dir, "clear-owner", 0, 0)
	if got := len(reopened.List()); got != 1 {
		t.Fatalf("rows after reopen = %d, want only the post-clear save", got)
	}
}

func TestResultStoreClearMemoryAndCancellation(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	first := store.Save(ResultQuery, "before", &QueryResultData{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := store.Clear(ctx); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Clear() = (%d, %v), want (0, context.Canceled)", n, err)
	}
	if _, ok := store.Get(first); !ok {
		t.Fatal("cancelled clear removed in-memory data")
	}
	store.mu.Lock()
	waitCtx, waitCancel := context.WithCancel(context.Background())
	clearDone := make(chan error, 1)
	go func() {
		_, err := store.Clear(waitCtx)
		clearDone <- err
	}()
	waitCancel()
	store.mu.Unlock()
	if err := <-clearDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("clear cancelled while waiting for lock = %v, want context.Canceled", err)
	}
	if n, err := store.Clear(context.Background()); n != 1 || err != nil {
		t.Fatalf("Clear() = (%d, %v), want (1, nil)", n, err)
	}
	if store.results == nil || store.order == nil || len(store.results) != 0 || len(store.order) != 0 {
		t.Fatal("in-memory store was not reset to empty non-nil collections")
	}
	second := store.Save(ResultQuery, "after", &QueryResultData{})
	if _, ok := store.Get(second); !ok {
		t.Fatal("save after memory clear was unavailable")
	}
	if err := store.PersistenceError(); !errors.Is(err, errResultStoreNotDurable) {
		t.Fatalf("memory store durability = %v, want not durable", err)
	}
}

func TestResultStoreClearFailureAndClosedStore(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir, "clear-failure", 0, 0)
	id := store.Save(ResultQuery, "retain", &QueryResultData{})
	if _, err := store.db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Clear(context.Background()); n != 0 || err == nil {
		t.Fatalf("failed Clear() = (%d, %v), want an error", n, err)
	}
	if err := store.PersistenceError(); err == nil {
		t.Fatal("failed clear reported healthy persistence")
	}
	if got, ok := store.Get(id); !ok || got.Title != "retain" {
		t.Fatal("failed clear lost the existing result")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Clear(context.Background()); n != 0 || !errors.Is(err, errResultStoreClosed) {
		t.Fatalf("closed Clear() = (%d, %v), want closed error", n, err)
	}
	reopened := openTestStore(t, dir, "clear-failure", 0, 0)
	if _, ok := reopened.Get(id); !ok {
		t.Fatal("failed or closed clear did not preserve the durable row")
	}
}

func TestResultStoreClearWithConcurrentOperations(t *testing.T) {
	store := openTestStore(t, t.TempDir(), "clear-race", 0, 0)
	store.Save(ResultQuery, "seed", &QueryResultData{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < 20; j++ {
				switch i {
				case 0:
					store.Save(ResultQuery, "concurrent", &QueryResultData{})
				case 1:
					_, _ = store.Clear(context.Background())
				case 2:
					_ = store.List()
				case 3:
					_, _ = store.Get("missing")
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
}
