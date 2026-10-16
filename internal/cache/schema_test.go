package cache

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"

	_ "modernc.org/sqlite"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

func TestInitializeCurrentSchemaAndReopenWithoutDataLoss(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := initializeSchema(db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("schema version=%d, want 1", version)
	}
	if _, err := db.Exec(`INSERT INTO issues(key,project,status,priority,summary,updated,fetched_at) VALUES('APP-1','APP','Open','P1','persist','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(db); err != nil {
		t.Fatalf("reopen current schema: %v", err)
	}
	var summary string
	if err := db.QueryRow("SELECT summary FROM issues WHERE key='APP-1'").Scan(&summary); err != nil || summary != "persist" {
		t.Fatalf("row=%q err=%v", summary, err)
	}
}

func TestInitializationFailureRollsBackSchemaAndVersion(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE comments (unexpected TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := initializeSchema(db); err == nil {
		t.Fatal("expected conflicting schema initialization to fail")
	}
	var version, count int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed initialization advanced version: %d", version)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='issues'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partial schema remains after rollback")
	}
}

func TestUnsupportedSchemaRejectedWithoutModification(t *testing.T) {
	for _, invalid := range []int{-1, 2, 8} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d; CREATE TABLE marker(value TEXT)", invalid)); err != nil {
				t.Fatal(err)
			}
			if err := initializeSchema(db); err == nil {
				t.Fatal("unsupported schema accepted")
			}
			var version, markers, locks int
			if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='marker'").Scan(&markers); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='cache_schema_lock'").Scan(&locks); err != nil {
				t.Fatal(err)
			}
			if version != invalid || markers != 1 || locks != 0 {
				t.Fatalf("unsupported database modified: version=%d marker=%d locks=%d", version, markers, locks)
			}
		})
	}
}

func TestConcurrentFreshCacheInitialization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "user@example.test"}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := OpenForConfig(cfg)
			if err != nil {
				errs <- err
				return
			}
			errs <- c.Close()
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

func TestOpenForConfigPersistsAcrossClose(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "user@example.test"}
	first, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := first.Path()
	if err := first.UpsertIssueDetail(&jira.IssueDetail{Key: "APP-1", Summary: "persisted", Updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Path() != path {
		t.Fatalf("path changed: %s != %s", second.Path(), path)
	}
	if _, ok := second.GetIssueDetail("APP-1", time.Time{}); !ok {
		t.Fatal("cached detail missing after reopen")
	}
}

func TestManagerPathIsEmpty(t *testing.T) {
	manager, err := OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if manager.Path() != "" {
		t.Fatalf("in-memory manager path=%q", manager.Path())
	}
}

func TestNewScopeUsesCacheV1Namespace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, err := OpenForConfig(&config.Config{JiraServer: "https://jira.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if filepath.Base(filepath.Dir(c.Path())) != "cache-v1" {
		t.Fatalf("scope path=%s", c.Path())
	}
	if _, err := os.Stat(c.Path()); err != nil {
		t.Fatal(err)
	}
}
