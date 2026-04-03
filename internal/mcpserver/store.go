package mcpserver

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// ResultType identifies which template to use for rendering.
type ResultType string

const (
	ResultSummary    ResultType = "summary"
	ResultQuery      ResultType = "query"
	ResultDigest     ResultType = "digest"
	ResultEnrich     ResultType = "enrich"
	ResultCreateEpic ResultType = "create_epic"
)

// StoredResult holds a tool result for the web server.
type StoredResult struct {
	ID        string
	Type      ResultType
	Title     string
	Data      any
	CreatedAt time.Time
}

// ResultStore is an in-memory store for MCP tool results, keyed by UUID.
type ResultStore struct {
	mu      sync.RWMutex
	results map[string]*StoredResult
	order   []string
}

// NewResultStore creates a new empty result store.
func NewResultStore() *ResultStore {
	return &ResultStore{
		results: make(map[string]*StoredResult),
	}
}

// Save stores a result and returns its UUID.
func (s *ResultStore) Save(resultType ResultType, title string, data any) string {
	id := uuid.New().String()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[id] = &StoredResult{
		ID:        id,
		Type:      resultType,
		Title:     title,
		Data:      data,
		CreatedAt: time.Now(),
	}
	s.order = append(s.order, id)
	return id
}

// Get retrieves a result by ID.
func (s *ResultStore) Get(id string) (*StoredResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[id]
	return r, ok
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
