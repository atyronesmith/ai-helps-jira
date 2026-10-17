package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T, dir, instance string, age time.Duration, count int) *ResultStore {
	t.Helper()
	s, err := OpenResultStore(ResultStoreOptions{Directory: dir, InstanceID: instance, MaxAge: age, MaxResults: count})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestResultStoreDurableRestart(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir, "owner", 0, 0)
	id := s.Save(ResultQuery, "persisted", &QueryResultData{Query: "hello"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, dir, "owner", 0, 0)
	got, ok := reopened.Get(id)
	if !ok {
		t.Fatal("result missing after restart")
	}
	if got.Title != "persisted" || got.Data.(*QueryResultData).Query != "hello" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestResultStoreConcurrentHandlesSeeSharedRows(t *testing.T) {
	dir := t.TempDir()
	a := openTestStore(t, dir, "same", 0, 0)
	b := openTestStore(t, dir, "same", 0, 0)
	id := a.Save(ResultQuery, "shared", &QueryResultData{Query: "q"})
	if _, ok := b.Get(id); !ok {
		t.Fatal("second handle did not see insert")
	}
	if len(b.List()) != 1 {
		t.Fatal("second handle list did not see insert")
	}
	if !b.Delete(id) {
		t.Fatal("second handle failed to delete shared row")
	}
	if _, ok := a.Get(id); ok {
		t.Fatal("first handle still sees deleted row")
	}
}

func TestResultStoreRetentionIsScopedToDatabase(t *testing.T) {
	dir := t.TempDir()
	bounded := openTestStore(t, dir, "bounded", time.Hour, 2)
	other := openTestStore(t, dir, "other", 0, 0)
	otherID := other.Save(ResultQuery, "other", &QueryResultData{})
	oldID := bounded.Save(ResultQuery, "old", &QueryResultData{})
	// Create an old row directly to exercise age pruning deterministically.
	if _, err := bounded.db.Exec("UPDATE results SET created_at=? WHERE id=?", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano), oldID); err != nil {
		t.Fatal(err)
	}
	firstNew := bounded.Save(ResultQuery, "new one", &QueryResultData{})
	bounded.Save(ResultQuery, "new two", &QueryResultData{})
	bounded.Save(ResultQuery, "new three", &QueryResultData{})
	if _, ok := bounded.Get(oldID); ok {
		t.Fatal("expired result retained")
	}
	if got := len(bounded.List()); got != 2 {
		t.Fatalf("count retention got %d rows, want 2", got)
	}
	if _, ok := bounded.Get(firstNew); ok {
		t.Fatal("oldest live result exceeded count retention")
	}
	if _, ok := other.Get(otherID); !ok {
		t.Fatal("pruning affected another instance database")
	}
}

func TestResultStorePrivateFilesAndNoPIDCleanup(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "results-123456.db")
	if err := os.WriteFile(legacy, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, dir, "private", 0, 0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var dbPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") && e.Name() != "results-123456.db" {
			dbPath = filepath.Join(dir, e.Name())
		}
	}
	if dbPath == "" {
		t.Fatal("database file missing")
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, dbPath: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", path, got, want)
		}
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy/PID file was removed: %v", err)
	}
	_ = s.Close()
}

func TestOpenResultStoreRejectsInvalidOptionsAndSymlinks(t *testing.T) {
	for _, opts := range []ResultStoreOptions{{Directory: t.TempDir(), MaxAge: -time.Second}, {Directory: t.TempDir(), MaxResults: -1}} {
		if _, err := OpenResultStore(opts); err == nil {
			t.Fatal("expected invalid retention error")
		}
	}
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenResultStore(ResultStoreOptions{Directory: link}); err == nil {
		t.Fatal("expected symlink directory rejection")
	}
}

func TestResultStoreConcurrentFirstOpenAndCloseObservation(t *testing.T) {
	dir := t.TempDir()
	stores := make([]*ResultStore, 8)
	start := make(chan struct{})
	errCh := make(chan error, len(stores))
	for i := range stores {
		go func(i int) {
			<-start
			s, err := OpenResultStore(ResultStoreOptions{Directory: dir, InstanceID: "racing", MaxAge: time.Hour, MaxResults: 100})
			if err == nil {
				stores[i] = s
			}
			errCh <- err
		}(i)
	}
	close(start)
	for range stores {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	var writers sync.WaitGroup
	for _, store := range stores {
		writers.Add(1)
		go func(s *ResultStore) {
			defer writers.Done()
			for range 20 {
				s.Save(ResultQuery, "parallel", &QueryResultData{Query: "parallel"})
				if err := s.PersistenceError(); err != nil {
					t.Error(err)
				}
			}
		}(store)
	}
	writers.Wait()
	if got := len(stores[1].List()); got != 100 {
		t.Fatalf("parallel writes visible as %d rows, want retention cap of 100", got)
	}
	for _, s := range stores {
		_ = s.Close()
	}

	store := openTestStore(t, dir, "close-race", 0, 0)
	id := store.Save(ResultQuery, "close", &QueryResultData{})
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-begin; _, _ = store.Get(id); _, _ = store.PersistenceObservation() }()
	}
	close(begin)
	_ = store.Close()
	wg.Wait()
}

func TestResultStoreCorruptHistoryIsNotHealthy(t *testing.T) {
	store := openTestStore(t, t.TempDir(), "corrupt", 0, 0)
	bad := store.Save(ResultQuery, "bad", &QueryResultData{})
	store.Save(ResultQuery, "valid", &QueryResultData{})
	if _, err := store.db.Exec("UPDATE results SET data='not-json' WHERE id=?", bad); err != nil {
		t.Fatal(err)
	}
	if got := len(store.List()); got != 1 {
		t.Fatalf("returned %d rows, want one valid result", got)
	}
	if err := store.PersistenceError(); err == nil {
		t.Fatal("corrupt history reported a healthy read")
	}
}

func TestResultStorePersistenceObservationTracksLastOperation(t *testing.T) {
	store := openTestStore(t, t.TempDir(), "observed", 0, 0)
	if err, checked := store.PersistenceObservation(); err != nil || checked.IsZero() {
		t.Fatalf("open observation = (%v, %v)", err, checked)
	}
	store.Save(ResultQuery, "one", &QueryResultData{})
	if err, checked := store.PersistenceObservation(); err != nil || checked.IsZero() {
		t.Fatalf("save observation = (%v, %v)", err, checked)
	}
	memory := &ResultStore{results: make(map[string]*StoredResult)}
	if err, checked := memory.PersistenceObservation(); err == nil || !checked.IsZero() {
		t.Fatalf("memory observation = (%v, %v), want non-durable error and no timestamp", err, checked)
	}
}
