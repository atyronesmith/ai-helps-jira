package mcpserver

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// ResultType identifies which template to use for rendering.
type ResultType string

const (
	ResultSummary    ResultType = "summary"
	ResultQuery      ResultType = "query"
	ResultDigest     ResultType = "digest"
	ResultEnrich     ResultType = "enrich"
	ResultCreateEpic    ResultType = "create_epic"
	ResultWeeklyStatus ResultType = "weekly_status"
)

// StoredResult holds a tool result for the web server.
type StoredResult struct {
	ID        string
	Type      ResultType
	Title     string
	Data      any
	CreatedAt time.Time
}

// ResultStore persists MCP tool results in SQLite, with an in-memory cache.
type ResultStore struct {
	mu      sync.RWMutex
	results map[string]*StoredResult
	order   []string
	db      *sql.DB
}

// NewResultStore creates a result store backed by SQLite.
func NewResultStore() *ResultStore {
	s := &ResultStore{
		results: make(map[string]*StoredResult),
	}

	db, err := openResultDB()
	if err != nil {
		slog.Error("failed to open result store DB, using in-memory only", "error", err)
		return s
	}
	s.db = db

	s.loadFromDB()
	return s
}

func openResultDB() (*sql.DB, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".jira-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	dbPath := filepath.Join(dir, "cache.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS results (
			id         TEXT PRIMARY KEY,
			type       TEXT NOT NULL,
			title      TEXT NOT NULL,
			data       TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_results_created ON results(created_at);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create results table: %w", err)
	}

	return db, nil
}

func (s *ResultStore) loadFromDB() {
	if s.db == nil {
		return
	}

	rows, err := s.db.Query("SELECT id, type, title, data, created_at FROM results ORDER BY created_at ASC")
	if err != nil {
		slog.Error("failed to load results from DB", "error", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, typ, title, dataJSON, createdStr string
		if err := rows.Scan(&id, &typ, &title, &dataJSON, &createdStr); err != nil {
			slog.Warn("skip corrupt result row", "error", err)
			continue
		}

		data, err := deserializeData(ResultType(typ), []byte(dataJSON))
		if err != nil {
			slog.Warn("skip result with bad data", "id", id, "type", typ, "error", err)
			continue
		}

		created, _ := time.Parse("2006-01-02T15:04:05Z", createdStr)

		s.results[id] = &StoredResult{
			ID:        id,
			Type:      ResultType(typ),
			Title:     title,
			Data:      data,
			CreatedAt: created,
		}
		s.order = append(s.order, id)
		count++
	}
	if count > 0 {
		slog.Info("loaded results from DB", "count", count)
	}
}

func deserializeData(typ ResultType, data []byte) (any, error) {
	switch typ {
	case ResultSummary:
		var v SummaryResult
		return &v, json.Unmarshal(data, &v)
	case ResultQuery:
		var v QueryResultData
		return &v, json.Unmarshal(data, &v)
	case ResultDigest:
		var v DigestResultData
		return &v, json.Unmarshal(data, &v)
	case ResultEnrich:
		var v EnrichResultData
		return &v, json.Unmarshal(data, &v)
	case ResultCreateEpic:
		var v CreateEpicResultData
		return &v, json.Unmarshal(data, &v)
	case ResultWeeklyStatus:
		var v WeeklyStatusResultData
		return &v, json.Unmarshal(data, &v)
	default:
		return nil, fmt.Errorf("unknown result type: %s", typ)
	}
}

// Save stores a result and returns its UUID.
func (s *ResultStore) Save(resultType ResultType, title string, data any) string {
	id := uuid.New().String()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.results[id] = &StoredResult{
		ID:        id,
		Type:      resultType,
		Title:     title,
		Data:      data,
		CreatedAt: now,
	}
	s.order = append(s.order, id)

	// Persist to SQLite
	if s.db != nil {
		dataJSON, err := json.Marshal(data)
		if err != nil {
			slog.Error("failed to marshal result data", "id", id, "error", err)
			return id
		}
		_, err = s.db.Exec(
			"INSERT INTO results (id, type, title, data, created_at) VALUES (?, ?, ?, ?, ?)",
			id, string(resultType), title, string(dataJSON), now.Format("2006-01-02T15:04:05Z"),
		)
		if err != nil {
			slog.Error("failed to persist result", "id", id, "error", err)
		}
	}

	return id
}

// Get retrieves a result by ID.
func (s *ResultStore) Get(id string) (*StoredResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[id]
	return r, ok
}

// Delete removes a result by ID.
func (s *ResultStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.results[id]; !ok {
		return false
	}

	delete(s.results, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}

	if s.db != nil {
		if _, err := s.db.Exec("DELETE FROM results WHERE id = ?", id); err != nil {
			slog.Error("failed to delete result from DB", "id", id, "error", err)
		}
	}

	return true
}

// List returns all results in reverse chronological order.
func (s *ResultStore) List() []*StoredResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*StoredResult, len(s.order))
	for i, id := range s.order {
		out[len(s.order)-1-i] = s.results[id]
	}
	return out
}
