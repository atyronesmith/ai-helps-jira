//go:build oracle

// Package jqloracle is a thin client for Jira's own JQL parser,
// POST /rest/api/3/jql/parse?validation=strict. It exists only to let the
// CI corpus test in internal/llm and internal/jira validate generated JQL
// against this tenant's authoritative grammar and vocabulary -- no local
// unit test can substitute for either.
//
// This whole package is build-tag gated behind "oracle" so it never
// compiles (let alone runs) as part of the default `go build ./...` /
// `go vet ./...` / `go test ./...`, keeping the default suite hermetic and
// offline. It is not wired into any runtime request path; calling the
// parse endpoint on every query was considered and explicitly rejected
// (see docs/jql-validation-plan.md, "Out of scope") because it roughly
// doubles query latency and adds a hard network dependency to an otherwise
// local-first path.
package jqloracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Client posts batches of JQL strings to Jira's /jql/parse endpoint.
type Client struct {
	http  *http.Client
	base  string
	email string
	token string
}

// NewClientFromEnv builds a Client from JIRA_SERVER / JIRA_EMAIL /
// JIRA_API_TOKEN. It reads the environment directly (not config.Load*)
// because the oracle suite must run with exactly the three JIRA_* variables
// exported by the caller -- see CONTRIBUTING.md -- and must not silently
// require JIRA_PROJECT or any LLM configuration the rest of the config
// package demands.
//
// It returns an error (never skips) when credentials are missing; callers
// that want a soft skip should check that error and call t.Skip themselves,
// which keeps the "no credentials -> skip" policy visible at the call site
// instead of buried in this helper.
func NewClientFromEnv() (*Client, error) {
	server := os.Getenv("JIRA_SERVER")
	email := os.Getenv("JIRA_EMAIL")
	token := os.Getenv("JIRA_API_TOKEN")
	if server == "" || email == "" || token == "" {
		return nil, fmt.Errorf("jqloracle: JIRA_SERVER, JIRA_EMAIL, and JIRA_API_TOKEN must all be set (see CONTRIBUTING.md)")
	}
	return &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  server,
		email: email,
		token: token,
	}, nil
}

// QueryResult is one element of the batch response. Jira returns HTTP 200
// for every request regardless of whether individual queries are valid, so
// callers MUST inspect Errors rather than any HTTP status code.
type QueryResult struct {
	Query     string     `json:"query"`
	Errors    []string   `json:"errors"`
	Warnings  []string   `json:"warnings"`
	Structure *Structure `json:"structure"`
}

// Structure is the parsed AST Jira returns for a valid query.
type Structure struct {
	Where   *Clause  `json:"where"`
	OrderBy *OrderBy `json:"orderBy"`
}

// Clause is a node in the parsed WHERE tree. A leaf clause (a single
// predicate, e.g. "project = FIELDCTO" or "issuetype IN (Bug, Story)")
// carries Field and Operator; a compound clause (AND/OR) carries Operator
// ("and"/"or") and Clauses, each itself a Clause -- compound nodes can
// nest arbitrarily (e.g. a parenthesised OR under a top-level AND).
//
// The operand (the compared value or value list) is deliberately not
// modeled: every assertion this corpus makes is about clause shape and
// field presence, never about operand values, so there is no need to chase
// the different wire shapes Jira uses for a scalar vs. a membership list.
// encoding/json ignores the "operand" key it doesn't recognize.
type Clause struct {
	Operator string    `json:"operator"`
	Clauses  []*Clause `json:"clauses,omitempty"`
	Field    *Field    `json:"field,omitempty"`
}

// Field names the JQL field a leaf clause predicates on.
type Field struct {
	Name string `json:"name"`
}

// OrderBy is the parsed sort clause.
type OrderBy struct {
	Fields []struct {
		Field     Field  `json:"field"`
		Direction string `json:"direction"`
	} `json:"fields"`
}

// IsCompound reports whether c is an AND/OR node (as opposed to a leaf
// predicate).
func (c *Clause) IsCompound() bool {
	return c != nil && len(c.Clauses) > 0
}

// HasTopLevelFieldPredicate reports whether c -- a possibly-compound
// clause -- has a direct (non-nested) predicate on the named field. "Direct"
// means the predicate sits at this clause's own level: for a compound node
// that means one of its immediate Clauses is a leaf on field; for a leaf
// node it means the node itself is on field. It deliberately does NOT
// recurse into a nested compound sub-clause, because a predicate buried
// inside a parenthesised OR is exactly the kind of leak the FIELDCTO-2807
// scope invariant exists to catch -- the oracle assertion needs to tell a
// top-level `project = X AND (...)` apart from a project predicate stranded
// inside the parenthesised branch.
func (c *Clause) HasTopLevelFieldPredicate(field string) bool {
	if c == nil {
		return false
	}
	if !c.IsCompound() {
		return c.Field != nil && c.Field.Name == field
	}
	for _, sub := range c.Clauses {
		if sub != nil && !sub.IsCompound() && sub.Field != nil && sub.Field.Name == field {
			return true
		}
	}
	return false
}

// CountFieldClauses recursively counts leaf clauses (anywhere in the tree,
// nested or not) that predicate on the named field. It exists for the
// FIELDCTO-2808 regression: same-field predicates that were AND-ed
// ("issuetype = Bug AND issuetype = Story", never satisfiable) must
// collapse into a single IN (...) leaf clause ("issuetype IN (Bug,
// Story)"), so a correct render counts 1 and a reverted/regressed one
// counts 2 or more.
func (c *Clause) CountFieldClauses(field string) int {
	if c == nil {
		return 0
	}
	n := 0
	if !c.IsCompound() {
		if c.Field != nil && c.Field.Name == field {
			n++
		}
		return n
	}
	for _, sub := range c.Clauses {
		n += sub.CountFieldClauses(field)
	}
	return n
}

// ParseStrict posts queries to /rest/api/3/jql/parse?validation=strict and
// returns one QueryResult per input query, in order. The endpoint always
// answers HTTP 200; a non-200 response here means something below the JQL
// layer failed (auth, network, tenant outage) and is returned as an error
// rather than folded into the per-query results.
func (c *Client) ParseStrict(queries []string) ([]QueryResult, error) {
	reqBody, err := json.Marshal(map[string]any{"queries": queries})
	if err != nil {
		return nil, fmt.Errorf("jqloracle: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.base+"/rest/api/3/jql/parse?validation=strict", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("jqloracle: build request: %w", err)
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jqloracle: request to %s failed: %w", c.base, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jqloracle: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jqloracle: unexpected status %d from %s: %s", resp.StatusCode, c.base, truncate(string(body), 500))
	}

	var decoded struct {
		Queries []QueryResult `json:"queries"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("jqloracle: decode response: %w (body: %s)", err, truncate(string(body), 500))
	}
	if len(decoded.Queries) != len(queries) {
		return nil, fmt.Errorf("jqloracle: expected %d results, got %d", len(queries), len(decoded.Queries))
	}
	return decoded.Queries, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
