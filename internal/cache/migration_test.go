package cache

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

func dbAtVersion(t *testing.T, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`CREATE TABLE issues (key TEXT PRIMARY KEY, project TEXT NOT NULL,status TEXT NOT NULL,priority TEXT NOT NULL,summary TEXT NOT NULL,updated DATETIME NOT NULL,fetched_at DATETIME NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`CREATE TABLE issue_boards(issue_key TEXT NOT NULL,board TEXT NOT NULL,board_type TEXT NOT NULL,sprint TEXT NOT NULL DEFAULT '',PRIMARY KEY(issue_key,board)); CREATE TABLE fetch_log(project TEXT NOT NULL,assignee TEXT NOT NULL,fetched_at DATETIME NOT NULL); CREATE INDEX idx_issues_project ON issues(project); CREATE INDEX idx_fetch_log_project ON fetch_log(project,assignee)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for v := 2; v <= version; v++ {
		tx, err = db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = migrateVersion(tx, v); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
			t.Fatal(err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEveryLegacyVersionMigratesToV8(t *testing.T) {
	for version := 1; version <= 7; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db := dbAtVersion(t, version)
			if err := migrate(db); err != nil {
				t.Fatal(err)
			}
			var got int
			if err := db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != 8 {
				t.Fatalf("version=%d err=%v", got, err)
			}
			if err := migrate(db); err != nil {
				t.Fatalf("reopening migrated cache: %v", err)
			}
		})
	}
}

func TestUnexpectedMigrationFailureRollsBackEarlierSteps(t *testing.T) {
	db := dbAtVersion(t, 1)
	if _, err := db.Exec("CREATE TABLE comments (unexpected TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("unexpected v3 schema conflict was swallowed")
	}
	var version, columns int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("failed upgrade advanced version: %d %v", version, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('issues') WHERE name='assignee'").Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("earlier upgrade step was not rolled back: %d %v", columns, err)
	}
}

func TestFutureSchemaIsRejectedWithoutModification(t *testing.T) {
	db := dbAtVersion(t, 7)
	if _, err := db.Exec("PRAGMA user_version=9"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("future schema accepted")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 9 {
		t.Fatalf("future schema modified: %d %v", version, err)
	}
}

func TestMigrateV8FailureRollsBackSchemaAndVersion(t *testing.T) {
	db := dbAtVersion(t, 7)
	// Simulate a malformed partial v8 migration; the first ALTER must fail.
	if _, err := db.Exec(`ALTER TABLE issues ADD COLUMN status_category TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("expected v8 migration to fail")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("user_version advanced to %d", version)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('issue_details') WHERE name IN ('status_category','project','assignee_email')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial v8 columns remain: %d", count)
	}
}

func TestMigrateV2InspectsDuplicateAssignee(t *testing.T) {
	db := dbAtVersion(t, 1)
	if _, err := db.Exec(`ALTER TABLE issues ADD COLUMN assignee TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate legacy v2 duplicate column: %v", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 8 {
		t.Fatalf("version=%d, want 8", version)
	}
}
