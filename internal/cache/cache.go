package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

type Cache struct {
	db               *sql.DB
	base             *Cache
	expectedRevision *int64
	ownedBase        bool
	scopeID          string
	scopeMu          sync.Mutex
	children         map[string]*Cache
	metricsParent    *Cache

	lookupMu      sync.Mutex
	detailLookups []bool
	lookupNext    int
}

func (c *Cache) Close() error {
	_, err := c.closeScoped()
	return err
}

// initializeSchema creates the current cache schema in one transaction. The
// writer lock is acquired before reading user_version so concurrent first opens
// cannot both inspect an empty database and race to initialize it.
func initializeSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS cache_schema_lock (id INTEGER PRIMARY KEY);
 INSERT OR IGNORE INTO cache_schema_lock(id) VALUES(1)`); err != nil {
		return fmt.Errorf("lock cache initialization: %w", err)
	}
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read cache schema version: %w", err)
	}
	if version != 0 && version != 1 {
		return fmt.Errorf("unsupported cache schema version %d; expected current format 1", version)
	}
	if version == 0 {
		for _, statement := range currentSchemaStatements {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("initialize cache schema: %w", err)
			}
		}
		if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
			return fmt.Errorf("set cache schema version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cache initialization: %w", err)
	}
	return nil
}

var currentSchemaStatements = []string{
	`CREATE TABLE issues (key TEXT PRIMARY KEY, project TEXT NOT NULL, status TEXT NOT NULL, priority TEXT NOT NULL, summary TEXT NOT NULL, updated DATETIME NOT NULL, fetched_at DATETIME NOT NULL, assignee TEXT NOT NULL DEFAULT '', status_category TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE issue_boards (issue_key TEXT NOT NULL, board TEXT NOT NULL, board_type TEXT NOT NULL, sprint TEXT NOT NULL DEFAULT '', PRIMARY KEY(issue_key,board))`,
	`CREATE TABLE fetch_log (project TEXT NOT NULL, assignee TEXT NOT NULL, fetched_at DATETIME NOT NULL)`,
	`CREATE INDEX idx_issues_project ON issues(project)`,
	`CREATE INDEX idx_fetch_log_project ON fetch_log(project,assignee)`,
	`CREATE TABLE comments (id TEXT PRIMARY KEY,issue_key TEXT NOT NULL,author TEXT NOT NULL,author_email TEXT NOT NULL DEFAULT '',body TEXT NOT NULL,created DATETIME NOT NULL,fetched_at DATETIME NOT NULL)`,
	`CREATE TABLE issue_links (source_key TEXT NOT NULL,target_key TEXT NOT NULL,link_type TEXT NOT NULL,direction TEXT NOT NULL,target_summary TEXT NOT NULL DEFAULT '',target_status TEXT NOT NULL DEFAULT '',target_type TEXT NOT NULL DEFAULT '',fetched_at DATETIME NOT NULL,PRIMARY KEY(source_key,target_key,link_type))`,
	`CREATE INDEX idx_comments_issue ON comments(issue_key)`,
	`CREATE INDEX idx_links_source ON issue_links(source_key)`,
	`CREATE TABLE digest_log (query_key TEXT PRIMARY KEY,last_run DATETIME NOT NULL)`,
	`CREATE TABLE weekly_cache (cache_key TEXT PRIMARY KEY,result_json TEXT NOT NULL,cached_at DATETIME NOT NULL)`,
	`CREATE TABLE issue_details (key TEXT PRIMARY KEY,summary TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,priority TEXT NOT NULL DEFAULT '',issue_type TEXT NOT NULL DEFAULT '',labels TEXT NOT NULL DEFAULT '[]',assignee_name TEXT NOT NULL DEFAULT '',assignee_id TEXT NOT NULL DEFAULT '',parent_key TEXT NOT NULL DEFAULT '',parent_summary TEXT NOT NULL DEFAULT '',updated DATETIME NOT NULL,fetched_at DATETIME NOT NULL,status_category TEXT NOT NULL DEFAULT '',project TEXT NOT NULL DEFAULT '',assignee_email TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE projects (key TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',project_type TEXT NOT NULL DEFAULT '',fetched_at DATETIME NOT NULL)`,
	`CREATE TABLE cache_epoch (id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL)`,
	`INSERT INTO cache_epoch(id,revision) VALUES(1,0)`,
	`CREATE TABLE issue_invalidations (key TEXT PRIMARY KEY,revision INTEGER NOT NULL)`,
	`CREATE TABLE summary_snapshots (owner TEXT NOT NULL,project TEXT NOT NULL,fetched_at TEXT NOT NULL,PRIMARY KEY(owner,project))`,
	`CREATE TABLE summary_members (owner TEXT NOT NULL,project TEXT NOT NULL,issue_key TEXT NOT NULL,kind TEXT NOT NULL,board TEXT NOT NULL DEFAULT '',board_type TEXT NOT NULL DEFAULT '',sprint TEXT NOT NULL DEFAULT '',PRIMARY KEY(owner,project,issue_key,kind,board,sprint))`,
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
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Search-index rows are not authoritative after a known mutation. Even a
	// new operation can see the old index version; only a direct detail read or
	// the summary provider's direct rechecks may refill these canonical rows.
	var eligible []jira.Issue
	for _, issue := range issues {
		var invalidated int
		if err := tx.QueryRow("SELECT count(*) FROM issue_invalidations WHERE key=?", issue.Key).Scan(&invalidated); err != nil {
			return err
		}
		if invalidated == 0 {
			eligible = append(eligible, issue)
		}
	}
	if err := upsertIssuesTx(tx, eligible); err != nil {
		return err
	}
	boardStmt, err := tx.Prepare(`INSERT INTO issue_boards(issue_key,board,board_type,sprint) VALUES(?,?,?,?) ON CONFLICT(issue_key,board) DO UPDATE SET board_type=excluded.board_type,sprint=excluded.sprint`)
	if err != nil {
		return err
	}
	defer boardStmt.Close()
	for _, i := range eligible {
		if i.Board != "" {
			bt := "kanban"
			if i.Sprint != "" {
				bt = "scrum"
			}
			if _, err = boardStmt.Exec(i.Key, i.Board, bt, i.Sprint); err != nil {
				return fmt.Errorf("upsert board %s/%s: %w", i.Key, i.Board, err)
			}
		}
	}
	return tx.Commit()
}

// Compare parsed instants rather than RFC3339 text: offsets and optional
// fractional seconds do not have chronological lexical order.
func newerIssueRow(tx *sql.Tx, key string, incoming time.Time) (bool, error) {
	var stamp string
	err := tx.QueryRow("SELECT updated FROM issues WHERE key=?", key).Scan(&stamp)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stored, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return false, fmt.Errorf("invalid cached issue timestamp: %w", err)
	}
	return stored.After(incoming), nil
}

func upsertIssuesTx(tx *sql.Tx, issues []jira.Issue) error {
	stmt, err := tx.Prepare(`INSERT INTO issues(key,project,status,status_category,priority,summary,updated,fetched_at,assignee) VALUES(?,?,?,?,?,?,?,?,?)
 ON CONFLICT(key) DO UPDATE SET project=excluded.project,status=excluded.status,status_category=excluded.status_category,priority=excluded.priority,summary=excluded.summary,updated=excluded.updated,fetched_at=excluded.fetched_at,assignee=excluded.assignee`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, i := range issues {
		newer, err := newerIssueRow(tx, i.Key, i.Updated)
		if err != nil {
			return err
		}
		if newer {
			continue
		}
		p := firstNonEmpty(i.Project, projectPrefix(i.Key))
		// A newer canonical observation can disprove another owner's cached
		// membership even before that owner refreshes. Require reconciliation
		// rather than serving the row under its previous identity/project.
		if _, err := tx.Exec(`DELETE FROM summary_snapshots WHERE (owner,project) IN
 (SELECT owner,project FROM summary_members WHERE issue_key=?) AND EXISTS
 (SELECT 1 FROM issues WHERE key=? AND (project!=? OR status_category!=? OR assignee!=?))`, i.Key, i.Key, p, i.StatusCategory, i.Assignee); err != nil {
			return err
		}
		if _, err := stmt.Exec(i.Key, p, i.Status, i.StatusCategory, i.Priority, i.Summary, i.Updated.UTC().Format(time.RFC3339Nano), now, i.Assignee); err != nil {
			return fmt.Errorf("upsert issue %s: %w", i.Key, err)
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func projectPrefix(key string) string {
	if i := strings.IndexByte(key, '-'); i > 0 {
		return key[:i]
	}
	return ""
}

// RemoveDone removes done issues and their board mappings from cache.
func (c *Cache) RemoveDone(project string) (int64, error) {
	tx, err := c.beginWrite()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM issue_boards WHERE issue_key IN (SELECT key FROM issues WHERE project=? AND lower(status_category)='done')`, project); err != nil {
		return 0, err
	}
	var n int64
	if err = tx.QueryRow(`SELECT COUNT(*) FROM issues WHERE project=? AND lower(status_category)='done'`, project).Scan(&n); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// LogFetch records a fetch timestamp in local time (JIRA interprets JQL dates in user's timezone).
func (c *Cache) LogFetch(project, assignee string) error {
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		"INSERT INTO fetch_log (project, assignee, fetched_at) VALUES (?, ?, ?)",
		project, assignee, time.Now().Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GetIssues returns all cached open issues for a project.
func (c *Cache) GetIssues(project string) ([]jira.Issue, error) {
	rows, err := c.db.Query(
		"SELECT key, project, status, status_category, priority, summary, updated, assignee FROM issues WHERE project = ? "+
			"AND lower(status_category) != 'done' AND status_category != '' "+
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
		if err := rows.Scan(&i.Key, &i.Project, &i.Status, &i.StatusCategory, &i.Priority, &i.Summary, &updated, &i.Assignee); err != nil {
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
		       i.key, i.project, i.status, i.status_category, i.priority, i.summary, i.updated, i.assignee
		FROM issue_boards ib
		JOIN issues i ON i.key = ib.issue_key
		WHERE i.project = ? AND lower(i.status_category) != 'done' AND status_category != ''
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
			&i.Key, &i.Project, &i.Status, &i.StatusCategory, &i.Priority, &i.Summary, &updated, &i.Assignee); err != nil {
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
	tx, err := c.beginWrite()
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
	tx, err := c.beginWrite()
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

// InvalidateIssue removes an issue from summary/board caches along with its
// cached detail, comments, and links involving key.
// All deletions run in one transaction so callers never observe a partially
// invalidated issue view.
func (c *Cache) InvalidateIssue(key string) error {
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("begin issue cache invalidation: %w", err)
	}
	defer tx.Rollback()

	var revision int64
	if _, err := tx.Exec("UPDATE cache_epoch SET revision=revision+1 WHERE id=1"); err != nil {
		return fmt.Errorf("advance cache revision: %w", err)
	}
	if err := tx.QueryRow("SELECT revision FROM cache_epoch WHERE id=1").Scan(&revision); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO issue_invalidations(key,revision) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET revision=excluded.revision", key, revision); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM summary_snapshots"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM summary_members WHERE issue_key=?", key); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM weekly_cache"); err != nil {
		return err
	}
	deletes := []struct {
		query string
		args  []any
	}{
		{"DELETE FROM issue_boards WHERE issue_key = ?", []any{key}},
		{"DELETE FROM issues WHERE key = ?", []any{key}},
		{"DELETE FROM issue_details WHERE key = ?", []any{key}},
		{"DELETE FROM comments WHERE issue_key = ?", []any{key}},
		{"DELETE FROM issue_links WHERE source_key = ? OR target_key = ?", []any{key, key}},
	}
	for _, deletion := range deletes {
		result, err := tx.Exec(deletion.query, deletion.args...)
		if err != nil {
			return fmt.Errorf("invalidate issue cache %s: %w", key, err)
		}
		if _, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("count invalidated issue cache rows for %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit issue cache invalidation for %s: %w", key, err)
	}
	return nil
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
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		"INSERT INTO digest_log (query_key, last_run) VALUES (?, ?) "+
			"ON CONFLICT(query_key) DO UPDATE SET last_run = excluded.last_run",
		queryKey, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
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
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		"INSERT INTO weekly_cache (cache_key, result_json, cached_at) VALUES (?, ?, ?) "+
			"ON CONFLICT(cache_key) DO UPDATE SET result_json = excluded.result_json, cached_at = excluded.cached_at",
		cacheKey, resultJSON, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertIssueDetail caches a full IssueDetail.
func (c *Cache) UpsertIssueDetail(detail *jira.IssueDetail) error {
	return c.UpsertIssueDetails([]*jira.IssueDetail{detail})
}

// GetIssueDetail returns a cached IssueDetail if it exists and is fresh
// (i.e. the issue hasn't been updated since we cached it).
// knownUpdated is the issue's current updated timestamp from a search result.
// If knownUpdated is zero, the cached detail is returned regardless of age.
func (c *Cache) GetIssueDetail(key string, knownUpdated time.Time) (*jira.IssueDetail, bool) {
	var summary, description, status, statusCategory, project, assigneeEmail, priority, issueType string
	var labelsJSON, assigneeName, assigneeID, parentKey, parentSummary string
	var updatedStr string

	err := c.db.QueryRow(`
		SELECT summary, description, status, status_category, project, assignee_email, priority, issue_type, labels,
			assignee_name, assignee_id, parent_key, parent_summary, updated
		FROM issue_details WHERE key = ?`, key,
	).Scan(&summary, &description, &status, &statusCategory, &project, &assigneeEmail, &priority, &issueType, &labelsJSON,
		&assigneeName, &assigneeID, &parentKey, &parentSummary, &updatedStr)
	if err != nil {
		c.RecordIssueDetailLookup(false)
		return nil, false
	}

	cachedUpdated, _ := time.Parse(time.RFC3339, updatedStr)
	var canonicalStamp string
	if err := c.db.QueryRow("SELECT updated FROM issues WHERE key=?", key).Scan(&canonicalStamp); err == nil {
		if canonicalUpdated, err := time.Parse(time.RFC3339Nano, canonicalStamp); err == nil && canonicalUpdated.After(knownUpdated) {
			knownUpdated = canonicalUpdated
		}
	}

	// If we know the current updated time, check freshness
	if !knownUpdated.IsZero() && knownUpdated.After(cachedUpdated) {
		c.RecordIssueDetailLookup(false)
		return nil, false
	}
	c.RecordIssueDetailLookup(true)

	var labels []string
	_ = json.Unmarshal([]byte(labelsJSON), &labels)

	return &jira.IssueDetail{
		Key:            key,
		Summary:        summary,
		Description:    description,
		Status:         status,
		StatusCategory: statusCategory,
		Project:        project,
		AssigneeEmail:  assigneeEmail,
		Priority:       priority,
		Labels:         labels,
		IssueType:      issueType,
		Assignee:       assigneeName,
		AssigneeID:     assigneeID,
		ParentKey:      parentKey,
		ParentSummary:  parentSummary,
		Updated:        cachedUpdated,
	}, true
}

// UpsertIssueDetails caches multiple IssueDetails in a single transaction.
func (c *Cache) UpsertIssueDetails(details []*jira.IssueDetail) error {
	if len(details) == 0 {
		return nil
	}
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO issue_details (key, summary, description, status, status_category, project, assignee_email, priority, issue_type, labels, assignee_name, assignee_id, parent_key, parent_summary, updated, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			summary = excluded.summary,
			description = excluded.description,
			status = excluded.status,
			status_category = excluded.status_category, project = excluded.project, assignee_email = excluded.assignee_email,
			priority = excluded.priority,
			issue_type = excluded.issue_type,
			labels = excluded.labels,
			assignee_name = excluded.assignee_name,
			assignee_id = excluded.assignee_id,
			parent_key = excluded.parent_key,
			parent_summary = excluded.parent_summary,
			updated = excluded.updated,
			fetched_at = excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, d := range details {
		if d == nil {
			return fmt.Errorf("cannot cache a nil issue detail")
		}
		var prior string
		err := tx.QueryRow("SELECT updated FROM issue_details WHERE key=?", d.Key).Scan(&prior)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			stored, err := time.Parse(time.RFC3339Nano, prior)
			if err != nil {
				return err
			}
			if stored.After(d.Updated) {
				continue
			}
		}
		labelsJSON, _ := json.Marshal(d.Labels)
		_, err = stmt.Exec(d.Key, d.Summary, d.Description, d.Status, d.StatusCategory, firstNonEmpty(d.Project, projectPrefix(d.Key)), d.AssigneeEmail, d.Priority,
			d.IssueType, string(labelsJSON), d.Assignee, d.AssigneeID, d.ParentKey, d.ParentSummary, d.Updated.UTC().Format(time.RFC3339Nano), now)
		if err != nil {
			return fmt.Errorf("upsert issue detail %s: %w", d.Key, err)
		}
		if d.StatusCategory != "" {
			if err := upsertIssuesTx(tx, []jira.Issue{{Key: d.Key, Project: firstNonEmpty(d.Project, projectPrefix(d.Key)), Status: d.Status, StatusCategory: d.StatusCategory, Priority: d.Priority, Summary: d.Summary, Updated: d.Updated, Assignee: d.AssigneeEmail, AssigneeID: d.AssigneeID}}); err != nil {
				return err
			}
		}
	}

	slog.Info("cache upserted issue details", "count", len(details))
	return tx.Commit()
}

// GetIssueDetailKeys returns the keys of all cached issue details that are
// still fresh based on the given updated timestamps from search results.
func (c *Cache) GetFreshDetailKeys(updatedByKey map[string]time.Time) map[string]bool {
	fresh := make(map[string]bool)
	if len(updatedByKey) == 0 {
		return fresh
	}

	var keys []string
	for k := range updatedByKey {
		keys = append(keys, k)
	}

	placeholders := strings.Repeat("?,", len(keys))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}

	rows, err := c.db.Query(
		fmt.Sprintf("SELECT key, updated FROM issue_details WHERE key IN (%s)", placeholders),
		args...,
	)
	if err != nil {
		for range updatedByKey {
			c.RecordIssueDetailLookup(false)
		}
		return fresh
	}
	defer rows.Close()

	for rows.Next() {
		var key, updatedStr string
		if err := rows.Scan(&key, &updatedStr); err != nil {
			continue
		}
		cachedUpdated, _ := time.Parse(time.RFC3339, updatedStr)
		if knownUpdated, ok := updatedByKey[key]; ok {
			if !knownUpdated.After(cachedUpdated) {
				fresh[key] = true
			}
		}
	}
	// Fresh keys are read again through GetIssueDetail by the callers that use
	// this classifier. Record only misses here so each eligible lookup is counted
	// once, whether it was absent or stale.
	for key := range updatedByKey {
		if !fresh[key] {
			c.RecordIssueDetailLookup(false)
		}
	}
	return fresh
}

// RecordIssueDetailLookup adds one hit or miss to the rolling window used by
// dashboard cache statistics. The window is process-local and holds the most
// recent ten cache-eligible issue-detail lookups.
func (c *Cache) RecordIssueDetailLookup(hit bool) {
	if base := c.canonical(); base != c {
		base.RecordIssueDetailLookup(hit)
		return
	}
	c.lookupMu.Lock()
	if len(c.detailLookups) < 10 {
		c.detailLookups = append(c.detailLookups, hit)
	} else {
		c.detailLookups[c.lookupNext] = hit
		c.lookupNext = (c.lookupNext + 1) % 10
	}
	parent := c.metricsParent
	c.lookupMu.Unlock()
	if parent != nil {
		parent.RecordIssueDetailLookup(hit)
	}
}

// IssueDetailLookupStats returns hit and sample counts for the rolling window.
func (c *Cache) IssueDetailLookupStats() (hits, lookups int) {
	if base := c.canonical(); base != c {
		return base.IssueDetailLookupStats()
	}
	c.lookupMu.Lock()
	defer c.lookupMu.Unlock()
	for _, hit := range c.detailLookups {
		lookups++
		if hit {
			hits++
		}
	}
	return hits, lookups
}

// UpsertProjects caches the tenant's project list (key + display name),
// refreshing fetched_at for every row. It is a no-op for an empty slice so a
// fetch that returns nothing never poisons or clears a good cache.
func (c *Cache) UpsertProjects(projects []jira.Project) error {
	if len(projects) == 0 {
		return nil
	}
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO projects (key, name, project_type, fetched_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name,
			project_type = excluded.project_type,
			fetched_at = excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, p := range projects {
		_, err := stmt.Exec(p.Key, p.Name, p.ProjectType, now)
		if err != nil {
			return fmt.Errorf("upsert project %s: %w", p.Key, err)
		}
	}
	slog.Info("cache upserted projects", "count", len(projects))
	return tx.Commit()
}

// GetProjects returns the cached project list, its most recent fetched_at, and
// whether any row exists. found is false when the table is empty.
func (c *Cache) GetProjects() ([]jira.Project, time.Time, bool) {
	rows, err := c.db.Query("SELECT key, name, project_type, fetched_at FROM projects ORDER BY key")
	if err != nil {
		return nil, time.Time{}, false
	}
	defer rows.Close()

	var projects []jira.Project
	var lastFetch time.Time
	for rows.Next() {
		var p jira.Project
		var fetched string
		if err := rows.Scan(&p.Key, &p.Name, &p.ProjectType, &fetched); err != nil {
			continue
		}
		projects = append(projects, p)
		if t, perr := time.Parse(time.RFC3339, fetched); perr == nil && t.After(lastFetch) {
			lastFetch = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, false
	}
	if len(projects) == 0 {
		return nil, time.Time{}, false
	}
	return projects, lastFetch, true
}

// CacheStats holds row counts for each cache table.
type CacheStats struct {
	CachedIssues int
	Issues       int
	IssueBoards  int
	IssueDetails int
	Comments     int
	IssueLinks   int
	WeeklyCache  int
	DigestLog    int
	FetchLog     int
	Projects     int
}

// Stats returns row counts for all cache tables.
func (c *Cache) Stats() CacheStats {
	if base := c.canonical(); base != c {
		return base.Stats()
	}
	var s CacheStats
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT key FROM issues UNION SELECT key FROM issue_details
	)`).Scan(&s.CachedIssues)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&s.Issues)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM issue_boards").Scan(&s.IssueBoards)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM issue_details").Scan(&s.IssueDetails)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM comments").Scan(&s.Comments)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM issue_links").Scan(&s.IssueLinks)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM weekly_cache").Scan(&s.WeeklyCache)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM digest_log").Scan(&s.DigestLog)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM fetch_log").Scan(&s.FetchLog)
	_ = c.db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&s.Projects)
	return s
}

// Path returns the database file path.
func (c *Cache) Path() string {
	if c.scopeID != "" {
		return filepath.Join(os.Getenv("HOME"), ".jira-cli", "cache-v1", c.scopeID+".db")
	}
	return ""
}

// Clear removes all cached data for a project.
func (c *Cache) Clear(project string) error {
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM issue_boards WHERE issue_key IN (SELECT key FROM issues WHERE project=?)`, `DELETE FROM issue_details WHERE project=?`, `DELETE FROM issues WHERE project=?`, `DELETE FROM fetch_log WHERE project=?`, `DELETE FROM summary_members WHERE project=?`, `DELETE FROM summary_snapshots WHERE project=?`} {
		if _, err = tx.Exec(q, project); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM weekly_cache`); err != nil {
		return err
	}
	return tx.Commit()
}
