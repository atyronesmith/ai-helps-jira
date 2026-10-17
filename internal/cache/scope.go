package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

var ErrCacheChanged = errors.New("cache changed during retrieval; retry the operation")

// OpenManager owns request-scoped databases. Its in-memory database is only a metrics root.
func OpenManager() (*Cache, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initializeSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}

func scopeName(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", errors.New("cache scope requires configuration")
	}
	site := strings.TrimSpace(cfg.JiraServer)
	if site != "" {
		u, err := url.Parse(site)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("cache scope requires an HTTP Jira base URL without credentials, query, or fragment")
		}
		u.Host = strings.ToLower(u.Host)
		u.Path = strings.TrimRight(u.Path, "/")
		site = u.String()
	}
	encoded, _ := json.Marshal([]string{site, strings.ToLower(strings.TrimSpace(cfg.JiraEmail))})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func openScope(name string) (*Cache, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".jira-cli", "cache-v1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create scoped cache directory: %w", err)
	}
	path := filepath.Join(dir, name+".db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create scoped cache: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// Acquire the writer lock before a write transaction inspects schema or
	// epoch state. Deferred read-to-write upgrades can return SQLITE_BUSY
	// immediately when simultaneous first opens hold shared schema locks.
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection also makes busy_timeout apply consistently. Transactions
	// never open a nested cache operation on this connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, err
	}
	if err := initializeSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db, scopeID: name}, nil
}

// OpenForConfig isolates tenant/account data without storing credentials in a
// file name. Tokenless translation can use an explicitly identified namespace;
// an unidentified namespace never imports the old unattributed cache.db.
func OpenForConfig(cfg *config.Config) (*Cache, error) {
	name, err := scopeName(cfg)
	if err != nil {
		return nil, err
	}
	base, err := openScope(name)
	if err != nil {
		return nil, err
	}
	snapshot, err := base.Snapshot()
	if err != nil {
		base.Close()
		return nil, err
	}
	snapshot.ownedBase = true
	return snapshot, nil
}

func (c *Cache) canonical() *Cache {
	if c.base != nil {
		return c.base
	}
	return c
}

// ForConfig returns a per-operation view. The manager owns its connections;
// handlers must not mutate its scope to service concurrent requests.
func (c *Cache) ForConfig(cfg *config.Config) (*Cache, error) {
	if c == nil {
		return nil, errors.New("cache unavailable")
	}
	name, err := scopeName(cfg)
	if err != nil {
		return nil, err
	}
	base := c.canonical()
	if base.scopeID == name {
		return base.Snapshot()
	}
	base.scopeMu.Lock()
	defer base.scopeMu.Unlock()
	if base.children == nil {
		base.children = make(map[string]*Cache)
	}
	child := base.children[name]
	if child == nil {
		child, err = openScope(name)
		if err != nil {
			return nil, err
		}
		child.metricsParent = base
		base.children[name] = child
	}
	return child.Snapshot()
}

// Snapshot captures a mutation revision before retrieval. A later mutation
// prevents this operation from repopulating the cache with its earlier reads.
func (c *Cache) Snapshot() (*Cache, error) {
	base := c.canonical()
	var revision int64
	if err := base.db.QueryRow("SELECT revision FROM cache_epoch WHERE id = 1").Scan(&revision); err != nil {
		return nil, err
	}
	return &Cache{db: base.db, base: base, scopeID: base.scopeID, expectedRevision: &revision}, nil
}

// operation preserves an already captured fence. In particular it must not
// bless an in-flight response by taking a newer revision after retrieval.
func (c *Cache) operation() (*Cache, error) {
	if c.expectedRevision != nil {
		return c, nil
	}
	return c.Snapshot()
}

func (c *Cache) beginWrite() (*sql.Tx, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return nil, err
	}
	query := "UPDATE cache_epoch SET revision = revision WHERE id = 1"
	var args []any
	if c.expectedRevision != nil {
		query += " AND revision = ?"
		args = append(args, *c.expectedRevision)
	}
	result, err := tx.Exec(query, args...)
	if err == nil {
		var n int64
		n, err = result.RowsAffected()
		if err == nil && n != 1 {
			err = ErrCacheChanged
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (c *Cache) closeScoped() (bool, error) {
	if c.base != nil {
		if c.ownedBase {
			return true, c.base.Close()
		}
		return true, nil
	}
	c.scopeMu.Lock()
	children := c.children
	c.children = nil
	c.scopeMu.Unlock()
	var joined error
	for _, child := range children {
		joined = errors.Join(joined, child.Close())
	}
	// Base handles also own their database. Close it here so child errors
	// cannot cause callers to leave the base connection open.
	return true, errors.Join(joined, c.db.Close())
}

// AggregateStats counts isolated records across the manager's open namespaces.
func (c *Cache) AggregateStats() CacheStats {
	base := c.canonical()
	stats := base.Stats()
	base.scopeMu.Lock()
	children := make([]*Cache, 0, len(base.children))
	for _, child := range base.children {
		children = append(children, child)
	}
	base.scopeMu.Unlock()
	for _, child := range children {
		s := child.Stats()
		stats.CachedIssues += s.CachedIssues
		stats.Issues += s.Issues
		stats.IssueBoards += s.IssueBoards
		stats.IssueDetails += s.IssueDetails
		stats.Comments += s.Comments
		stats.IssueLinks += s.IssueLinks
		stats.WeeklyCache += s.WeeklyCache
		stats.Projects += s.Projects
		stats.DigestLog += s.DigestLog
		stats.FetchLog += s.FetchLog
	}
	return stats
}
