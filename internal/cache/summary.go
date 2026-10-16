package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

var ErrNoSummary = errors.New("no verified cached summary for the requested user and project; refresh it first")

// LoadSummary shares the synchronization contract between CLI and MCP. Online
// reads always enumerate the entire current set: an open-only delta cannot see
// closures, reassignment, moves, deletion, or permission loss.
func LoadSummary(ctx context.Context, c *Cache, cfg *config.Config, cacheOnly bool, fetch func(context.Context, []string) ([]jira.BoardInfo, []jira.Issue, error)) ([]jira.BoardInfo, []jira.Issue, time.Time, error) {
	if c == nil || cfg == nil {
		return nil, nil, time.Time{}, errors.New("summary requires a scoped cache and configuration")
	}
	op, err := c.operation()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	owner, project := cfg.Assignee(), cfg.JiraProject
	if cacheOnly {
		return op.GetSummary(owner, project)
	}
	if fetch == nil {
		return nil, nil, time.Time{}, errors.New("summary fetcher unavailable")
	}
	started := time.Now().UTC()
	invalidated, err := op.InvalidatedKeys()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	boards, issues, err := fetch(ctx, invalidated)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, time.Time{}, err
	}
	return op.replaceSummary(owner, project, boards, issues, started)
}

// Invalidations survive refreshes because Jira's search index can lag a direct
// issue read. Another owner/project refresh must not consume their protection.
func (c *Cache) InvalidatedKeys() ([]string, error) {
	rows, err := c.db.Query("SELECT key FROM issue_invalidations ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (c *Cache) replaceSummary(owner, project string, boards []jira.BoardInfo, issues []jira.Issue, fetchedAt time.Time) ([]jira.BoardInfo, []jira.Issue, time.Time, error) {
	tx, err := c.beginWrite()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRow("SELECT fetched_at FROM summary_snapshots WHERE owner=? AND project=?", owner, project).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, time.Time{}, err
	}
	if prior, err := time.Parse(time.RFC3339Nano, previous); err == nil && prior.After(fetchedAt) {
		return nil, nil, time.Time{}, ErrCacheChanged
	}
	current := make(map[string]jira.Issue, len(issues))
	for _, issue := range issues {
		if issue.Project == "" {
			issue.Project = projectPrefix(issue.Key)
		}
		if issue.Key == "" || issue.Project != project || !jira.IsOpenIssue(issue) {
			return nil, nil, time.Time{}, fmt.Errorf("unverified summary member %q", issue.Key)
		}
		if _, exists := current[issue.Key]; exists {
			return nil, nil, time.Time{}, fmt.Errorf("duplicate summary member %q", issue.Key)
		}
		newer, err := newerIssueRow(tx, issue.Key, issue.Updated)
		if err != nil {
			return nil, nil, time.Time{}, err
		}
		if newer {
			return nil, nil, time.Time{}, ErrCacheChanged
		}
		current[issue.Key] = issue
	}
	if err := upsertIssuesTx(tx, issues); err != nil {
		return nil, nil, time.Time{}, err
	}
	if _, err := tx.Exec("DELETE FROM summary_members WHERE owner=? AND project=?", owner, project); err != nil {
		return nil, nil, time.Time{}, err
	}
	for key := range current {
		if _, err := tx.Exec("INSERT INTO summary_members(owner,project,issue_key,kind) VALUES(?,?,?,'open')", owner, project, key); err != nil {
			return nil, nil, time.Time{}, err
		}
	}
	for _, board := range boards {
		// An empty board still conveys an active sprint or known empty board.
		if _, err := tx.Exec("INSERT OR IGNORE INTO summary_members(owner,project,issue_key,kind,board,board_type,sprint) VALUES(?,?,'','board',?,?,?)", owner, project, board.Name, board.BoardType, board.SprintName); err != nil {
			return nil, nil, time.Time{}, err
		}
		for _, issue := range board.Issues {
			if _, ok := current[issue.Key]; !ok {
				return nil, nil, time.Time{}, fmt.Errorf("board contains unverified summary member %q", issue.Key)
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO summary_members(owner,project,issue_key,kind,board,board_type,sprint) VALUES(?,?,?,'board',?,?,?)", owner, project, issue.Key, board.Name, board.BoardType, board.SprintName); err != nil {
				return nil, nil, time.Time{}, err
			}
		}
	}
	stamp := fetchedAt.Format(time.RFC3339Nano)
	if _, err := tx.Exec("INSERT INTO summary_snapshots(owner,project,fetched_at) VALUES(?,?,?) ON CONFLICT(owner,project) DO UPDATE SET fetched_at=excluded.fetched_at", owner, project, stamp); err != nil {
		return nil, nil, time.Time{}, err
	}
	if _, err := tx.Exec("DELETE FROM fetch_log WHERE project=? AND assignee=?", project, owner); err != nil {
		return nil, nil, time.Time{}, err
	}
	if _, err := tx.Exec("INSERT INTO fetch_log(project,assignee,fetched_at) VALUES(?,?,?)", project, owner, stamp); err != nil {
		return nil, nil, time.Time{}, err
	}
	b, i, at, err := readSummary(tx, owner, project)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, time.Time{}, err
	}
	return b, i, at, nil
}

// GetSummary reads membership and canonical rows in one database snapshot.
func (c *Cache) GetSummary(owner, project string) ([]jira.BoardInfo, []jira.Issue, time.Time, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	defer tx.Rollback()
	if c.expectedRevision != nil {
		var revision int64
		if err := tx.QueryRow("SELECT revision FROM cache_epoch WHERE id=1").Scan(&revision); err != nil {
			return nil, nil, time.Time{}, err
		}
		if revision != *c.expectedRevision {
			return nil, nil, time.Time{}, ErrCacheChanged
		}
	}
	return readSummary(tx, owner, project)
}

func readSummary(tx *sql.Tx, owner, project string) ([]jira.BoardInfo, []jira.Issue, time.Time, error) {
	var stamp string
	if err := tx.QueryRow("SELECT fetched_at FROM summary_snapshots WHERE owner=? AND project=?", owner, project).Scan(&stamp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNoSummary
		}
		return nil, nil, time.Time{}, err
	}
	fetchedAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	rows, err := tx.Query(`SELECT m.kind,m.board,m.board_type,m.sprint,
	 COALESCE(i.key,''),COALESCE(i.project,''),COALESCE(i.status,''),COALESCE(i.status_category,''),
	 COALESCE(i.priority,''),COALESCE(i.summary,''),COALESCE(i.updated,''),COALESCE(i.assignee,'')
	 FROM summary_members m LEFT JOIN issues i ON i.key=m.issue_key
	 WHERE m.owner=? AND m.project=? AND (m.issue_key='' OR
	 (i.project=? AND i.status_category!='' AND lower(i.status_category)!='done'))
	 ORDER BY m.kind,m.board,m.sprint,i.priority,i.status,i.key`, owner, project, project)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	defer rows.Close()
	var boards []jira.BoardInfo
	var issues []jira.Issue
	indices := make(map[[3]string]int)
	for rows.Next() {
		var kind, board, boardType, sprint, updated string
		var issue jira.Issue
		if err := rows.Scan(&kind, &board, &boardType, &sprint, &issue.Key, &issue.Project, &issue.Status, &issue.StatusCategory, &issue.Priority, &issue.Summary, &updated, &issue.Assignee); err != nil {
			return nil, nil, time.Time{}, err
		}
		issue.Updated, _ = time.Parse(time.RFC3339Nano, updated)
		if kind == "open" {
			issues = append(issues, issue)
			continue
		}
		id := [3]string{board, boardType, sprint}
		index, ok := indices[id]
		if !ok {
			index = len(boards)
			indices[id] = index
			boards = append(boards, jira.BoardInfo{Name: board, BoardType: boardType, SprintName: sprint})
		}
		if issue.Key != "" {
			issue.Board, issue.Sprint = board, sprint
			boards[index].Issues = append(boards[index].Issues, issue)
		}
	}
	return boards, issues, fetchedAt, rows.Err()
}
