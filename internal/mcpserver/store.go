package mcpserver

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// ResultType identifies which template to use for rendering.
type ResultType string

const resultTimestampFormat = "2006-01-02T15:04:05.000000000Z"

const (
	ResultSummary             ResultType = "summary"
	ResultQuery               ResultType = "query"
	ResultDigest              ResultType = "digest"
	ResultEnrich              ResultType = "enrich"
	ResultCreateEpic          ResultType = "create_epic"
	ResultWeeklyStatus        ResultType = "weekly_status"
	ResultFindSimilar         ResultType = "find_similar"
	ResultConfluenceAnalytics ResultType = "confluence_analytics"
	ResultAttempt             ResultType = "attempt"
)

type StoredResult struct {
	ID        string
	Type      ResultType
	Title     string
	Data      any
	CreatedAt time.Time
}

type ResultStoreOptions struct {
	Directory  string
	InstanceID string
	MaxAge     time.Duration
	MaxResults int
}

type ResultStore struct {
	mu                   sync.RWMutex
	persistenceMu        sync.RWMutex
	results              map[string]*StoredResult
	order                []string
	db                   *sql.DB
	maxAge               time.Duration
	maxResults           int
	persistenceErr       error
	persistenceCheckedAt time.Time
	durable              bool
	closed               bool
}

// NewResultStore retains the historical no-error constructor, with bounded durable storage.
func NewResultStore() *ResultStore {
	s, err := OpenResultStore(ResultStoreOptions{MaxAge: 30 * 24 * time.Hour, MaxResults: 1000})
	if err == nil {
		return s
	}
	slog.Error("failed to open result store; using in-memory storage", "error", err)
	s = &ResultStore{results: make(map[string]*StoredResult), maxAge: 30 * 24 * time.Hour, maxResults: 1000}
	s.setPersistenceError(err)
	return s
}

func OpenResultStore(opts ResultStoreOptions) (*ResultStore, error) {
	if opts.MaxAge < 0 {
		return nil, errors.New("max age must not be negative")
	}
	if opts.MaxResults < 0 {
		return nil, errors.New("max results must not be negative")
	}
	if opts.InstanceID == "" {
		opts.InstanceID = "default"
	}
	if opts.Directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		opts.Directory = filepath.Join(home, ".jira-cli", "results-v1")
	}
	dir, err := filepath.Abs(opts.Directory)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(dir); err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(opts.InstanceID))
	path := filepath.Join(dir, fmt.Sprintf("%x.db", hash))
	if err := ensurePrivateDBFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open results database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS results (id TEXT PRIMARY KEY, type TEXT NOT NULL, title TEXT NOT NULL, data TEXT NOT NULL, created_at TEXT NOT NULL); CREATE INDEX IF NOT EXISTS idx_results_created ON results(created_at);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize results database: %w", err)
	}
	s := &ResultStore{db: db, results: make(map[string]*StoredResult), maxAge: opts.MaxAge, maxResults: opts.MaxResults, durable: true}
	if err := s.Prune(time.Now()); err != nil {
		db.Close()
		return nil, fmt.Errorf("prune results database: %w", err)
	}
	return s, nil
}

func ensurePrivateDirectory(dir string) error {
	// Parent paths can contain platform aliases (for example macOS /var -> /private/var).
	// The selected storage directory itself must be a real directory, never a symlink.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("result directory is not a real directory: %s", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure result directory: %w", err)
	}
	return nil
}

func ensurePrivateDBFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if errors.Is(e, os.ErrExist) {
			info, err = os.Lstat(path)
		} else if e != nil {
			return fmt.Errorf("create result database: %w", e)
		} else {
			if e = f.Close(); e != nil {
				return e
			}
			info, err = os.Lstat(path)
		}
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("result database is not a regular file: %s", path)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure result database: %w", err)
	}
	return nil
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
	case ResultFindSimilar:
		var v FindSimilarResultData
		return &v, json.Unmarshal(data, &v)
	case ResultConfluenceAnalytics:
		var v ConfluenceAnalyticsResultData
		return &v, json.Unmarshal(data, &v)
	case ResultAttempt:
		var v AttemptResultData
		return &v, json.Unmarshal(data, &v)
	default:
		return nil, fmt.Errorf("unknown result type: %s", typ)
	}
}

// Save stores a result and returns its UUID.
func (s *ResultStore) Save(resultType ResultType, title string, data any) string {
	id := uuid.New().String()
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.setPersistenceError(errResultStoreClosed)
		return id
	}
	if s.db != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			s.setPersistenceError(err)
			return id
		}
		tx, err := s.db.Begin()
		if err != nil {
			s.setPersistenceError(err)
			return id
		}
		_, err = tx.Exec("INSERT INTO results(id,type,title,data,created_at) VALUES(?,?,?,?,?)", id, string(resultType), title, string(raw), now.Format(resultTimestampFormat))
		if err == nil {
			err = s.pruneTx(tx, now)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		s.setPersistenceError(err)
		return id
	}
	s.results[id] = &StoredResult{ID: id, Type: resultType, Title: title, Data: data, CreatedAt: now}
	s.order = append(s.order, id)
	s.pruneMemory(now)
	return id
}

func (s *ResultStore) Get(id string) (*StoredResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.setPersistenceError(errResultStoreClosed)
		return nil, false
	}
	if s.db == nil {
		r, ok := s.results[id]
		return r, ok
	}
	var typ, title, data, created string
	err := s.db.QueryRow("SELECT type,title,data,created_at FROM results WHERE id=?", id).Scan(&typ, &title, &data, &created)
	if err != nil {
		if err == sql.ErrNoRows {
			err = nil
		}
		s.setPersistenceError(err)
		return nil, false
	}
	result, err := decodeStoredResult(id, ResultType(typ), title, data, created)
	if err != nil {
		s.setPersistenceError(err)
		return nil, false
	}
	s.setPersistenceError(nil)
	return result, true
}

func (s *ResultStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.setPersistenceError(errResultStoreClosed)
		return false
	}
	if s.db != nil {
		res, err := s.db.Exec("DELETE FROM results WHERE id=?", id)
		s.setPersistenceError(err)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n > 0
	}
	if _, ok := s.results[id]; !ok {
		return false
	}
	delete(s.results, id)
	s.removeOrder(id)
	return true
}

func (s *ResultStore) List() []*StoredResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.setPersistenceError(errResultStoreClosed)
		return nil
	}
	if s.db == nil {
		out := make([]*StoredResult, 0, len(s.order))
		for i := len(s.order) - 1; i >= 0; i-- {
			if r := s.results[s.order[i]]; r != nil {
				out = append(out, r)
			}
		}
		return out
	}
	rows, err := s.db.Query("SELECT id,type,title,data,created_at FROM results ORDER BY created_at DESC,id DESC")
	if err != nil {
		s.setPersistenceError(err)
		return nil
	}
	defer rows.Close()
	out := make([]*StoredResult, 0)
	var decodeErr error
	for rows.Next() {
		var id, typ, title, data, created string
		if err = rows.Scan(&id, &typ, &title, &data, &created); err != nil {
			break
		}
		r, e := decodeStoredResult(id, ResultType(typ), title, data, created)
		if e != nil {
			decodeErr = errors.Join(decodeErr, e)
			continue
		}
		out = append(out, r)
	}
	err = errors.Join(err, rows.Err(), decodeErr)
	s.setPersistenceError(err)
	return out
}

func decodeStoredResult(id string, typ ResultType, title, data, created string) (*StoredResult, error) {
	value, err := deserializeData(typ, []byte(data))
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", created)
		if err != nil {
			return nil, err
		}
	}
	return &StoredResult{ID: id, Type: typ, Title: title, Data: value, CreatedAt: t}, nil
}

func (s *ResultStore) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.setPersistenceError(errResultStoreClosed)
		return errResultStoreClosed
	}
	if s.db != nil {
		tx, err := s.db.Begin()
		if err == nil {
			err = s.pruneTx(tx, now.UTC())
		}
		if err == nil {
			err = tx.Commit()
		} else if tx != nil {
			_ = tx.Rollback()
		}
		s.setPersistenceError(err)
		return err
	}
	s.pruneMemory(now)
	return nil
}
func (s *ResultStore) pruneTx(tx *sql.Tx, now time.Time) error {
	if s.maxAge > 0 {
		if _, err := tx.Exec("DELETE FROM results WHERE created_at < ?", now.Add(-s.maxAge).UTC().Format(resultTimestampFormat)); err != nil {
			return err
		}
	}
	if s.maxResults > 0 {
		if _, err := tx.Exec("DELETE FROM results WHERE id IN (SELECT id FROM results ORDER BY created_at DESC,id DESC LIMIT -1 OFFSET ?)", s.maxResults); err != nil {
			return err
		}
	}
	return nil
}
func (s *ResultStore) pruneMemory(now time.Time) {
	if s.maxAge > 0 {
		cut := now.Add(-s.maxAge)
		for id, r := range s.results {
			if r.CreatedAt.Before(cut) {
				delete(s.results, id)
			}
		}
	}
	ids := make([]string, 0, len(s.results))
	for id := range s.results {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.results[ids[i]], s.results[ids[j]]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return ids[i] > ids[j]
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	if s.maxResults > 0 && len(ids) > s.maxResults {
		for _, id := range ids[s.maxResults:] {
			delete(s.results, id)
		}
	}
	s.order = s.order[:0]
	for i := len(ids) - 1; i >= 0; i-- {
		if s.results[ids[i]] != nil {
			s.order = append(s.order, ids[i])
		}
	}
}
func (s *ResultStore) removeOrder(id string) {
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

var errResultStoreClosed = errors.New("result store is closed")

var errResultStoreNotDurable = errors.New("result store is not durable")

func (s *ResultStore) setPersistenceError(err error) {
	s.persistenceMu.Lock()
	s.persistenceErr = err
	s.persistenceCheckedAt = time.Now().UTC()
	s.persistenceMu.Unlock()
}
func (s *ResultStore) PersistenceError() error {
	err, _ := s.PersistenceObservation()
	return err
}

// PersistenceObservation reports the outcome and time of the last storage operation.
// Memory-only stores are never reported as durable.
func (s *ResultStore) PersistenceObservation() (error, time.Time) {
	s.persistenceMu.RLock()
	defer s.persistenceMu.RUnlock()
	if !s.durable && s.persistenceErr == nil {
		return errResultStoreNotDurable, s.persistenceCheckedAt
	}
	return s.persistenceErr, s.persistenceCheckedAt
}
func (s *ResultStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.db == nil {
		s.setPersistenceError(errResultStoreClosed)
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		s.setPersistenceError(err)
	} else {
		s.setPersistenceError(errResultStoreClosed)
	}
	return err
}
