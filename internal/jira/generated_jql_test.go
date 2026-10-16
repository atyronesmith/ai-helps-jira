package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

type generatedQuery struct {
	name       string
	query      string
	projected  bool
	noProject  bool
	parentOnly bool
}

func assertGeneratedQueries(t *testing.T, got []generatedQuery) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("no generated JQL was captured")
	}
	for _, item := range got {
		t.Run(item.name, func(t *testing.T) {
			parsed, err := jql.Parse(item.query)
			if err != nil {
				t.Fatalf("parse generated JQL %q: %v", item.query, err)
			}
			rendered := parsed.Render()
			roundTrip, err := jql.Parse(rendered)
			if err != nil {
				t.Fatalf("parse rendered JQL %q: %v", rendered, err)
			}
			if !reflect.DeepEqual(parsed, roundTrip) {
				t.Errorf("Parse(Render(Parse(JQL))) changed the AST:\nfirst: %#v\nagain: %#v", parsed, roundTrip)
			}
			if rendered != item.query {
				t.Errorf("generated JQL is not in canonical AST form:\n got: %s\nwant: %s", item.query, rendered)
			}
			if len(parsed.OrderBy) == 0 {
				t.Fatal("generated query lost its ORDER BY clause")
			}
			blanked := strings.ToUpper(jql.BlankQuotedLiterals(item.query))
			orderAt := strings.Index(blanked, "ORDER BY")
			if strings.Count(blanked, "ORDER BY") != 1 || orderAt < 0 || strings.TrimSpace(item.query[orderAt:]) != strings.TrimSpace(rendered[strings.Index(rendered, "ORDER BY"):]) {
				t.Errorf("ORDER BY is not preserved as the terminal clause: %q", item.query)
			}
			if item.projected {
				if !parsed.HasProjectScope() {
					t.Error("search query lacks a positive project restriction")
				}
				if err := jql.Validate(item.query, jql.Options{DefaultProject: "TEST"}); err != nil {
					t.Errorf("project-scoped query failed local validation: %v", err)
				}
			}
			if item.noProject && parsed.HasProjectScope() {
				t.Errorf("board-scoped query unexpectedly has project scope: %s", item.query)
			}
			if item.noProject {
				if err := jql.Validate(item.query, jql.Options{}); err != nil {
					t.Errorf("board-scoped query failed local validation: %v", err)
				}
			}
			if item.parentOnly {
				if parsed.HasProjectScope() || !strings.HasPrefix(item.query, "parent = ") {
					t.Errorf("child lookup should be scoped directly to its parent: %s", item.query)
				}
				if err := jql.Validate(item.query, jql.Options{}); err != nil {
					t.Errorf("parent-scoped query failed local structural validation: %v", err)
				}
			}
		})
	}
}

func TestGeneratedJQLCorpus(t *testing.T) {
	captureSearch := func(t *testing.T, handler http.HandlerFunc, run func(*Client) error) []generatedQuery {
		t.Helper()
		var captured []generatedQuery
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql" {
				body := readBody(t, r)
				captured = append(captured, generatedQuery{query: body["jql"].(string)})
				jsonResponse(t, w, http.StatusOK, map[string]any{"issues": []any{}, "total": 0, "isLast": true})
				return
			}
			handler(w, r)
		}))
		defer server.Close()
		if err := run(newTestClient(server)); err != nil {
			t.Fatalf("run generator: %v", err)
		}
		return captured
	}

	t.Run("open-work, since, and release", func(t *testing.T) {
		captured := captureSearch(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}, func(c *Client) error {
			if _, err := c.GetOpenIssues(); err != nil {
				return err
			}
			if _, err := c.GetOpenIssuesSince(time.Date(2026, 10, 15, 12, 30, 0, 0, time.UTC)); err != nil {
				return err
			}
			_, err := c.SearchByRelease(`v1.0 " OR project = OTHER`)
			return err
		})
		if len(captured) != 3 {
			t.Fatalf("captured %d search queries, want 3", len(captured))
		}
		captured[0].name, captured[0].projected = "GetOpenIssues", true
		captured[1].name, captured[1].projected = "GetOpenIssuesSince", true
		captured[2].name, captured[2].projected = "SearchByRelease with hostile literal", true
		assertGeneratedQueries(t, captured)
	})

	t.Run("children", func(t *testing.T) {
		captured := captureSearch(t, func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}, func(c *Client) error {
			_, err := c.GetChildIssues("TEST-1")
			return err
		})
		if len(captured) != 1 {
			t.Fatalf("captured %d child queries, want 1", len(captured))
		}
		captured[0].name, captured[0].parentOnly = "GetChildIssues", true
		assertGeneratedQueries(t, captured)
	})

	boardsHandler := func(captured *[]generatedQuery) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
				body := readBody(t, r)
				*captured = append(*captured, generatedQuery{name: "Scrum sprint JQL", query: body["jql"].(string), projected: true})
				jsonResponse(t, w, http.StatusOK, map[string]any{
					"issues": []any{map[string]any{"key": "TEST-21", "fields": map[string]any{
						"status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"},
					}}}, "total": 1, "isLast": true,
				})
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board":
				jsonResponse(t, w, http.StatusOK, map[string]any{
					"values": []any{
						map[string]any{"id": 11, "name": "Scrum", "type": "scrum"},
						map[string]any{"id": 12, "name": "Kanban", "type": "kanban"},
					}, "startAt": 0, "total": 2, "isLast": true,
				})
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board/11/sprint":
				jsonResponse(t, w, http.StatusOK, map[string]any{"values": []any{map[string]any{"id": 21, "name": "Active", "state": "active"}}, "startAt": 0, "total": 1, "isLast": true})
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board/12/sprint":
				// GetBoardIssues probes for sprints before taking the Kanban path.
				http.NotFound(w, r)
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board/12/issue":
				q := r.URL.Query().Get("jql")
				*captured = append(*captured, generatedQuery{name: "Kanban board JQL", query: q, noProject: true})
				jsonResponse(t, w, http.StatusOK, map[string]any{"issues": []any{}, "startAt": 0, "total": 0, "isLast": true})
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
				http.NotFound(w, r)
			}
		}
	}

	t.Run("board discovery", func(t *testing.T) {
		var captured []generatedQuery
		server := httptest.NewServer(boardsHandler(&captured))
		defer server.Close()
		c := newTestClient(server)
		boards, err := c.GetBoardIssues()
		if err != nil {
			t.Fatal(err)
		}
		if len(boards) != 1 || boards[0].Name != "Scrum" {
			t.Fatalf("unexpected mock board result: %#v", boards)
		}
		if len(captured) != 2 {
			t.Fatalf("captured %d board queries, want Scrum search and Kanban board query", len(captured))
		}
		assertGeneratedQueries(t, captured)
	})

	t.Run("summary board and sprint refresh", func(t *testing.T) {
		var captured []generatedQuery
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
				body := readBody(t, r)
				captured = append(captured, generatedQuery{name: fmt.Sprintf("search page %d", len(captured)+1), query: body["jql"].(string), projected: true})
				jsonResponse(t, w, http.StatusOK, map[string]any{"issues": []any{}, "total": 0, "isLast": true})
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board":
				boardsHandler(&captured)(w, r)
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board/11/sprint":
				jsonResponse(t, w, http.StatusOK, map[string]any{"values": []any{map[string]any{"id": 21, "name": "Active", "state": "active"}}, "startAt": 0, "total": 1, "isLast": true})
			case r.Method == http.MethodGet && r.URL.Path == "/rest/agile/1.0/board/12/issue":
				captured = append(captured, generatedQuery{name: "summary Kanban board JQL", query: r.URL.Query().Get("jql"), noProject: true})
				jsonResponse(t, w, http.StatusOK, map[string]any{"issues": []any{}, "startAt": 0, "total": 0, "isLast": true})
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		c := newTestClient(server)
		if _, _, err := c.FetchSummary(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		assertGeneratedQueries(t, captured)
	})

	t.Run("weekly activity", func(t *testing.T) {
		query, err := WeeklyActivityJQL("TEST", `"test@example.com"`, "2026-10-01", "2026-10-07")
		if err != nil {
			t.Fatal(err)
		}
		assertGeneratedQueries(t, []generatedQuery{{name: "WeeklyActivityJQL", query: query, projected: true}})
	})
}
