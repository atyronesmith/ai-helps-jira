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

	// v2: add assignee column to issues
	var version int
	_ = db.QueryRow("PRAGMA user_version").Scan(&version)
	if version < 2 {
		_, err := db.Exec(`ALTER TABLE issues ADD COLUMN assignee TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			slog.Warn("migrate v2: assignee column may already exist", "error", err)
		}
		_, _ = db.Exec("PRAGMA user_version = 2")
		slog.Info("cache migrated to v2", "added", "assignee column")
	}

	// v3: add comments and issue_links tables
	if version < 3 {
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS comments (
				id          TEXT PRIMARY KEY,
				issue_key   TEXT NOT NULL,
				author      TEXT NOT NULL,
				author_email TEXT NOT NULL DEFAULT '',
				body        TEXT NOT NULL,
				created     DATETIME NOT NULL,
				fetched_at  DATETIME NOT NULL
			);
			CREATE TABLE IF NOT EXISTS issue_links (
				source_key     TEXT NOT NULL,
				target_key     TEXT NOT NULL,
				link_type      TEXT NOT NULL,
				direction      TEXT NOT NULL,
				target_summary TEXT NOT NULL DEFAULT '',
				target_status  TEXT NOT NULL DEFAULT '',
				target_type    TEXT NOT NULL DEFAULT '',
				fetched_at     DATETIME NOT NULL,
				PRIMARY KEY (source_key, target_key, link_type)
			);
			CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_key);
			CREATE INDEX IF NOT EXISTS idx_links_source ON issue_links(source_key);
		`)
		if err != nil {
			slog.Warn("migrate v3: tables may already exist", "error", err)
		}
		_, _ = db.Exec("PRAGMA user_version = 3")
		slog.Info("cache migrated to v3", "added", "comments + issue_links tables")
	}

	// v4: add digest_log table for tracking last digest run
	if version < 4 {
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS digest_log (
				query_key  TEXT PRIMARY KEY,
				last_run   DATETIME NOT NULL
			);
		`)
		if err != nil {
			slog.Warn("migrate v4: table may already exist", "error", err)
		}
		_, _ = db.Exec("PRAGMA user_version = 4")
		slog.Info("cache migrated to v4", "added", "digest_log table")
	}

	// v5: add weekly_cache table for caching full weekly status results
	if version < 5 {
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS weekly_cache (
				cache_key   TEXT PRIMARY KEY,
				result_json TEXT NOT NULL,
				cached_at   DATETIME NOT NULL
			);
		`)
		if err != nil {
			slog.Warn("migrate v5: table may already exist", "error", err)
		}
		_, _ = db.Exec("PRAGMA user_version = 5")
		slog.Info("cache migrated to v5", "added", "weekly_cache table")
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
		INSERT INTO issues (key, project, status, priority, summary, updated, fetched_at, assignee)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			status = excluded.status,
			priority = excluded.priority,
			summary = excluded.summary,
			updated = excluded.updated,
			fetched_at = excluded.fetched_at,
			assignee = excluded.assignee
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
			issue.Summary, issue.Updated.Format(time.RFC3339), now, issue.Assignee,
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
		"SELECT key, status, priority, summary, updated, assignee FROM issues WHERE project = ? "+
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
		if err := rows.Scan(&i.Key, &i.Status, &i.Priority, &i.Summary, &updated, &i.Assignee); err != nil {
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
		       i.key, i.status, i.priority, i.summary, i.updated, i.assignee
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
			&i.Key, &i.Status, &i.Priority, &i.Summary, &updated, &i.Assignee); err != nil {
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

// UpsertComments inserts or updates comments in the cache.
func (c *Cache) UpsertComments(comments []jira.Comment) error {
	if len(comments) == 0 {
		return nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO comments (id, issue_key, author, author_email, body, created, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			body = excluded.body,
			fetched_at = excluded.fetched_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, c := range comments {
		_, err := stmt.Exec(c.ID, c.IssueKey, c.AuthorName, c.AuthorEmail,
			c.Body, c.Created.Format(time.RFC3339), now)
		if err != nil {
			return fmt.Errorf("upsert comment %s: %w", c.ID, err)
		}
	}
	slog.Info("cache upserted comments", "count", len(comments))
	return tx.Commit()
}

// GetCommentsByKeys returns cached comments for multiple issue keys, filtered by created date.
func (c *Cache) GetCommentsByKeys(issueKeys []string, since time.Time) ([]jira.Comment, error) {
	if len(issueKeys) == 0 {
		return nil, nil
	}
	placeholders := ""
	args := make([]any, 0, len(issueKeys)+1)
	for i, key := range issueKeys {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, key)
	}
	args = append(args, since.Format(time.RFC3339))

	query := fmt.Sprintf(
		"SELECT id, issue_key, author, author_email, body, created FROM comments "+
			"WHERE issue_key IN (%s) AND created >= ? ORDER BY created DESC",
		placeholders,
	)
	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []jira.Comment
	for rows.Next() {
		var cm jira.Comment
		var created string
		if err := rows.Scan(&cm.ID, &cm.IssueKey, &cm.AuthorName, &cm.AuthorEmail,
			&cm.Body, &created); err != nil {
			return nil, err
		}
		cm.Created, _ = time.Parse(time.RFC3339, created)
		comments = append(comments, cm)
	}
	slog.Info("cache loaded comments", "count", len(comments), "keys", len(issueKeys))
	return comments, rows.Err()
}

// UpsertIssueLinks inserts or updates issue links in the cache.
func (c *Cache) UpsertIssueLinks(links []jira.IssueLink) error {
	if len(links) == 0 {
		return nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO issue_links (source_key, target_key, link_type, direction,
			target_summary, target_status, target_type, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_key, target_key, link_type) DO UPDATE SET
			direction = excluded.direction,
			target_summary = excluded.target_summary,
			target_status = excluded.target_status,
			target_type = excluded.target_type,
			fetched_at = excluded.fetched_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, l := range links {
		_, err := stmt.Exec(l.SourceKey, l.TargetKey, l.LinkType, l.Direction,
			l.TargetSummary, l.TargetStatus, l.TargetType, now)
		if err != nil {
			return fmt.Errorf("upsert link %s->%s: %w", l.SourceKey, l.TargetKey, err)
		}
	}
	slog.Info("cache upserted links", "count", len(links))
	return tx.Commit()
}

// GetIssueLinks returns cached links for a source issue key.
func (c *Cache) GetIssueLinks(sourceKey string) ([]jira.IssueLink, error) {
	rows, err := c.db.Query(
		"SELECT source_key, target_key, link_type, direction, "+
			"target_summary, target_status, target_type "+
			"FROM issue_links WHERE source_key = ?",
		sourceKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []jira.IssueLink
	for rows.Next() {
		var l jira.IssueLink
		if err := rows.Scan(&l.SourceKey, &l.TargetKey, &l.LinkType, &l.Direction,
			&l.TargetSummary, &l.TargetStatus, &l.TargetType); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	slog.Info("cache loaded links", "count", len(links), "source", sourceKey)
	return links, rows.Err()
}

// LastDigestRun returns the last time a digest was run for the given query key,
// or zero time if never run.
func (c *Cache) LastDigestRun(queryKey string) time.Time {
	var t string
	err := c.db.QueryRow(
		"SELECT last_run FROM digest_log WHERE query_key = ?", queryKey,
	).Scan(&t)
	if err != nil {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339, t)
	return parsed
}

// LogDigestRun records the current time as the last digest run for the given query key.
func (c *Cache) LogDigestRun(queryKey string) error {
	_, err := c.db.Exec(
		"INSERT INTO digest_log (query_key, last_run) VALUES (?, ?) "+
			"ON CONFLICT(query_key) DO UPDATE SET last_run = excluded.last_run",
		queryKey, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// GetWeeklyCache returns cached weekly status JSON and its cached_at time, if it exists.
func (c *Cache) GetWeeklyCache(cacheKey string) (string, time.Time, bool) {
	var resultJSON, cachedAt string
	err := c.db.QueryRow(
		"SELECT result_json, cached_at FROM weekly_cache WHERE cache_key = ?", cacheKey,
	).Scan(&resultJSON, &cachedAt)
	if err != nil {
		return "", time.Time{}, false
	}
	t, _ := time.Parse(time.RFC3339, cachedAt)
	return resultJSON, t, true
}

// SetWeeklyCache stores the weekly status JSON result.
func (c *Cache) SetWeeklyCache(cacheKey, resultJSON string) error {
	_, err := c.db.Exec(
		"INSERT INTO weekly_cache (cache_key, result_json, cached_at) VALUES (?, ?, ?) "+
			"ON CONFLICT(cache_key) DO UPDATE SET result_json = excluded.result_json, cached_at = excluded.cached_at",
		cacheKey, resultJSON, time.Now().UTC().Format(time.RFC3339),
	)
	return err
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
