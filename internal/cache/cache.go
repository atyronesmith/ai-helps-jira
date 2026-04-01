package cache

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

type Cache struct {
	db *sql.DB
}

func Open() (*Cache, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".jira-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	dbPath := filepath.Join(dir, "cache.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	slog.Info("cache opened", "path", dbPath)
	return &Cache{db: db}, nil
}

func (c *Cache) Close() error {
	return c.db.Close()
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
			key        TEXT PRIMARY KEY,
			project    TEXT NOT NULL,
			status     TEXT NOT NULL,
			priority   TEXT NOT NULL,
			summary    TEXT NOT NULL,
			updated    DATETIME NOT NULL,
			fetched_at DATETIME NOT NULL
		);
		CREATE TABLE IF NOT EXISTS issue_boards (
			issue_key  TEXT NOT NULL,
			board      TEXT NOT NULL,
			board_type TEXT NOT NULL,
			sprint     TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (issue_key, board)
		);
		CREATE TABLE IF NOT EXISTS fetch_log (
			project    TEXT NOT NULL,
			assignee   TEXT NOT NULL,
			fetched_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_issues_project ON issues(project);
		CREATE INDEX IF NOT EXISTS idx_fetch_log_project ON fetch_log(project, assignee);
	`)
	if err != nil {
		return fmt.Errorf("migrate cache: %w", err)
	}
	return nil
}

// LastFetch returns the most recent fetch time for a project+assignee, or zero time if none.
func (c *Cache) LastFetch(project, assignee string) time.Time {
	var t string
	err := c.db.QueryRow(
		"SELECT fetched_at FROM fetch_log WHERE project = ? AND assignee = ? ORDER BY fetched_at DESC LIMIT 1",
		project, assignee,
	).Scan(&t)
	if err != nil {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339, t)
	return parsed
}

// UpsertIssues inserts or updates issues and their board memberships.
func (c *Cache) UpsertIssues(project string, issues []jira.Issue) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	issueStmt, err := tx.Prepare(`
		INSERT INTO issues (key, project, status, priority, summary, updated, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			status = excluded.status,
			priority = excluded.priority,
			summary = excluded.summary,
			updated = excluded.updated,
			fetched_at = excluded.fetched_at
	`)
	if err != nil {
		return err
	}
	defer issueStmt.Close()

	boardStmt, err := tx.Prepare(`
		INSERT INTO issue_boards (issue_key, board, board_type, sprint)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(issue_key, board) DO UPDATE SET
			board_type = excluded.board_type,
			sprint = excluded.sprint
	`)
	if err != nil {
		return err
	}
	defer boardStmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, issue := range issues {
		_, err := issueStmt.Exec(
			issue.Key, project, issue.Status, issue.Priority,
			issue.Summary, issue.Updated.Format(time.RFC3339), now,
		)
		if err != nil {
			return fmt.Errorf("upsert issue %s: %w", issue.Key, err)
		}

		if issue.Board != "" {
			boardType := "kanban"
			if issue.Sprint != "" {
				boardType = "scrum"
			}
			_, err := boardStmt.Exec(issue.Key, issue.Board, boardType, issue.Sprint)
			if err != nil {
				return fmt.Errorf("upsert board %s/%s: %w", issue.Key, issue.Board, err)
			}
		}
	}

	slog.Info("cache upserted", "count", len(issues), "project", project)
	return tx.Commit()
}

// RemoveDone removes done issues and their board mappings from cache.
func (c *Cache) RemoveDone(project string) (int64, error) {
	// Remove board mappings for done issues
	_, _ = c.db.Exec(
		"DELETE FROM issue_boards WHERE issue_key IN "+
			"(SELECT key FROM issues WHERE project = ? AND status IN ('Done', 'Closed', 'Resolved'))",
		project,
	)
	result, err := c.db.Exec(
		"DELETE FROM issues WHERE project = ? AND status IN ('Done', 'Closed', 'Resolved')",
		project,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		slog.Info("cache removed done issues", "count", n, "project", project)
	}
	return n, nil
}

// LogFetch records a fetch timestamp in local time (JIRA interprets JQL dates in user's timezone).
func (c *Cache) LogFetch(project, assignee string) error {
	_, err := c.db.Exec(
		"INSERT INTO fetch_log (project, assignee, fetched_at) VALUES (?, ?, ?)",
		project, assignee, time.Now().Format(time.RFC3339),
	)
	return err
}

// GetIssues returns all cached open issues for a project.
func (c *Cache) GetIssues(project string) ([]jira.Issue, error) {
	rows, err := c.db.Query(
		"SELECT key, status, priority, summary, updated FROM issues WHERE project = ? "+
			"AND status NOT IN ('Done', 'Closed', 'Resolved') "+
			"ORDER BY priority ASC, status ASC",
		project,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []jira.Issue
	for rows.Next() {
		var i jira.Issue
		var updated string
		if err := rows.Scan(&i.Key, &i.Status, &i.Priority, &i.Summary, &updated); err != nil {
			return nil, err
		}
		i.Updated, _ = time.Parse(time.RFC3339, updated)
		issues = append(issues, i)
	}
	slog.Info("cache loaded", "count", len(issues), "project", project)
	return issues, rows.Err()
}

// GetBoards reconstructs BoardInfo groupings from cached issues + board mappings.
func (c *Cache) GetBoards(project string) ([]jira.BoardInfo, error) {
	rows, err := c.db.Query(`
		SELECT ib.board, ib.board_type, ib.sprint,
		       i.key, i.status, i.priority, i.summary, i.updated
		FROM issue_boards ib
		JOIN issues i ON i.key = ib.issue_key
		WHERE i.project = ? AND i.status NOT IN ('Done', 'Closed', 'Resolved')
		ORDER BY ib.board, i.priority ASC, i.status ASC
	`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	boardMap := make(map[string]*jira.BoardInfo)
	var order []string
	for rows.Next() {
		var boardName, boardType, sprint string
		var i jira.Issue
		var updated string
		if err := rows.Scan(&boardName, &boardType, &sprint,
			&i.Key, &i.Status, &i.Priority, &i.Summary, &updated); err != nil {
			return nil, err
		}
		i.Updated, _ = time.Parse(time.RFC3339, updated)
		i.Board = boardName
		i.Sprint = sprint

		bi, exists := boardMap[boardName]
		if !exists {
			bi = &jira.BoardInfo{
				Name:       boardName,
				BoardType:  boardType,
				SprintName: sprint,
			}
			boardMap[boardName] = bi
			order = append(order, boardName)
		}
		bi.Issues = append(bi.Issues, i)
	}

	var boards []jira.BoardInfo
	for _, name := range order {
		boards = append(boards, *boardMap[name])
	}
	slog.Info("cache boards loaded", "count", len(boards))
	return boards, rows.Err()
}

// Clear removes all cached data for a project.
func (c *Cache) Clear(project string) error {
	_, _ = c.db.Exec(
		"DELETE FROM issue_boards WHERE issue_key IN (SELECT key FROM issues WHERE project = ?)",
		project,
	)
	_, err := c.db.Exec("DELETE FROM issues WHERE project = ?", project)
	if err != nil {
		return err
	}
	_, err = c.db.Exec("DELETE FROM fetch_log WHERE project = ?", project)
	slog.Info("cache cleared", "project", project)
	return err
}
