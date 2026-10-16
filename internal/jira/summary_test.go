package jira

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchSummaryRechecksInvalidatedReassignedIssue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/myself":
			jsonResponse(t, w, 200, map[string]any{"accountId": "current"})
		case "/rest/api/3/search/jql":
			jsonResponse(t, w, 200, map[string]any{"issues": []any{map[string]any{"key": "TEST-1", "fields": map[string]any{"summary": "stale row", "status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"accountId": "old", "emailAddress": "test@example.com"}}}}, "isLast": true})
		case "/rest/api/3/issue/TEST-1":
			jsonResponse(t, w, 200, map[string]any{"key": "TEST-1", "fields": map[string]any{"summary": "reassigned", "status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"accountId": "other", "emailAddress": "other@example.com"}}})
		case "/rest/agile/1.0/board":
			jsonResponse(t, w, 200, map[string]any{"values": []any{}, "total": 0, "isLast": true})
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := newTestClient(ts)
	c.authEmail = "test@example.com"
	_, issues, err := c.FetchSummary(t.Context(), []string{"TEST-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("reassigned issue remained in summary: %#v", issues)
	}
}

func TestFetchSummaryRejectsMissingCategory(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		jsonResponse(t, w, 200, map[string]any{"issues": []any{map[string]any{"key": "TEST-1", "fields": map[string]any{"status": map[string]any{"name": "Open"}, "project": map[string]any{"key": "TEST"}}}}, "isLast": true})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	_, _, err := c.FetchSummary(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "missing project or status category") {
		t.Fatalf("expected strict missing category error, got %v", err)
	}
}

func TestIsOpenIssueUsesStatusCategory(t *testing.T) {
	for _, tc := range []struct {
		category string
		want     bool
	}{{"done", false}, {"Done", false}, {"new", true}, {"indeterminate", true}, {"", false}} {
		if got := IsOpenIssue(Issue{Status: "Closed", StatusCategory: tc.category}); got != tc.want {
			t.Errorf("category %q: IsOpenIssue = %v, want %v", tc.category, got, tc.want)
		}
	}
}

func TestFetchSummaryResolvesHiddenEmailForInvalidatedIssueWithoutSearchRow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			jsonResponse(t, w, 200, map[string]any{"issues": []any{}, "isLast": true})
		case "/rest/api/3/myself":
			jsonResponse(t, w, 200, map[string]any{"accountId": "me"})
		case "/rest/api/3/issue/TEST-1":
			jsonResponse(t, w, 200, map[string]any{"key": "TEST-1", "fields": map[string]any{"summary": "current", "status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"accountId": "me"}}})
		case "/rest/agile/1.0/board":
			jsonResponse(t, w, 200, map[string]any{"values": []any{}, "total": 0, "isLast": true})
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := newTestClient(ts)
	c.authEmail = "me@example.com"
	_, issues, err := c.FetchSummary(t.Context(), []string{"TEST-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Key != "TEST-1" {
		t.Fatalf("hidden-email invalidated issue not recovered: %#v", issues)
	}
}

func TestListCompleteUsesActualShortPageSize(t *testing.T) {
	var requested []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("startAt"), "%d", &start)
		requested = append(requested, start)
		if start == 0 {
			jsonResponse(t, w, 200, map[string]any{"values": []any{"a", "b"}, "startAt": 0, "total": 3, "isLast": false})
			return
		}
		jsonResponse(t, w, 200, map[string]any{"values": []any{"c"}, "startAt": 2, "total": 3, "isLast": true})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	var rows []string
	err := c.listComplete(t.Context(), "/pages", func(raw json.RawMessage) error {
		var page []string
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		rows = append(rows, page...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(requested) != "[0 2]" || fmt.Sprint(rows) != "[a b c]" {
		t.Fatalf("requested starts %v, rows %v", requested, rows)
	}
}

func TestListCompleteRejectsEmptyNonterminalPage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(t, w, 200, map[string]any{"values": []any{}, "startAt": 0, "total": 1, "isLast": false})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	err := c.listComplete(t.Context(), "/pages", func(json.RawMessage) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty nonterminal error, got %v", err)
	}
}

func TestListCompleteAcceptsEmptyLegacyResult(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(t, w, 200, map[string]any{"values": []any{}})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	if err := c.listComplete(t.Context(), "/pages", func(json.RawMessage) error { return nil }); err != nil {
		t.Fatalf("empty legacy response rejected: %v", err)
	}
}

func TestListCompleteAcceptsEmptyReportedZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(t, w, 200, map[string]any{"values": []any{}, "startAt": 0, "total": 0})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	if err := c.listComplete(t.Context(), "/pages", func(json.RawMessage) error { return nil }); err != nil {
		t.Fatalf("empty total=0 response rejected: %v", err)
	}
}

func TestListCompleteRejectsChangingTotals(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			jsonResponse(t, w, 200, map[string]any{"values": []any{"a"}, "startAt": 0, "total": 2, "isLast": false})
			return
		}
		jsonResponse(t, w, 200, map[string]any{"values": []any{"b"}, "startAt": 1, "total": 3, "isLast": false})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	err := c.listComplete(t.Context(), "/pages", func(json.RawMessage) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected changing total error, got %v", err)
	}
}

func TestListCompleteRejectsMalformedTotals(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{name: "negative total", body: map[string]any{"values": []any{}, "total": -1, "isLast": true}},
		{name: "rows exceed total", body: map[string]any{"values": []any{"one"}, "total": 0, "isLast": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { jsonResponse(t, w, 200, tc.body) }))
			defer ts.Close()
			c := newTestClient(ts)
			err := c.listComplete(t.Context(), "/pages", func(json.RawMessage) error { return nil })
			if err == nil {
				t.Fatal("expected malformed pagination error")
			}
		})
	}
}

func TestSummaryAccountIDRejectsAmbiguousDisplayName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(t, w, 200, []any{map[string]any{"accountId": "one", "displayName": "Alex"}, map[string]any{"accountId": "two", "displayName": "Alex"}})
	}))
	defer ts.Close()
	c := newTestClient(ts)
	c.userOverride = "Alex"
	_, err := c.summaryAccountID(t.Context())
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous user error, got %v", err)
	}
}

func TestFetchSummaryIncompleteSearchFailsClosed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/search/jql" {
			jsonResponse(t, w, 200, map[string]any{"issues": []any{}, "isLast": false})
			return
		}
		t.Errorf("unexpected request %s", r.URL.String())
		http.NotFound(w, r)
	}))
	defer ts.Close()
	_, _, err := newTestClient(ts).FetchSummary(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete search error, got %v", err)
	}
}

func TestFetchSummaryRemovesInvisibleOrClosedInvalidatedRows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		category string
	}{
		{name: "invisible 403", status: http.StatusForbidden},
		{name: "invisible 404", status: http.StatusNotFound},
		{name: "closed", status: http.StatusOK, category: "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/rest/api/3/myself":
					jsonResponse(t, w, 200, map[string]any{"accountId": "me"})
				case "/rest/api/3/search/jql":
					jsonResponse(t, w, 200, map[string]any{"issues": []any{map[string]any{"key": "TEST-2", "fields": map[string]any{"status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"accountId": "me"}}}}, "isLast": true})
				case "/rest/api/3/issue/TEST-2":
					if tc.status != http.StatusOK {
						w.WriteHeader(tc.status)
						return
					}
					jsonResponse(t, w, 200, map[string]any{"key": "TEST-2", "fields": map[string]any{"status": map[string]any{"name": "Closed"}, "statusCategory": map[string]any{"key": tc.category}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"accountId": "me"}}})
				case "/rest/agile/1.0/board":
					jsonResponse(t, w, 200, map[string]any{"values": []any{}, "total": 0, "isLast": true})
				default:
					t.Errorf("unexpected request %s", r.URL.String())
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			_, issues, err := newTestClient(ts).FetchSummary(t.Context(), []string{"TEST-2"})
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) != 0 {
				t.Fatalf("stale invalidated row remained: %#v", issues)
			}
		})
	}
}

func TestFetchSummaryBoardsPropagatesBoardAndSprintFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		boardType string
		failPath  string
	}{
		{name: "board listing", failPath: "/rest/agile/1.0/board"},
		{name: "sprint listing", boardType: "scrum", failPath: "/rest/agile/1.0/board/4/sprint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, tc.failPath) {
					http.Error(w, "failed", http.StatusInternalServerError)
					return
				}
				if r.URL.Path == "/rest/agile/1.0/board" {
					jsonResponse(t, w, 200, map[string]any{"values": []any{map[string]any{"id": 4, "name": "Board", "type": tc.boardType}}, "total": 1, "isLast": true})
					return
				}
				t.Errorf("unexpected request %s", r.URL.String())
				http.NotFound(w, r)
			}))
			defer ts.Close()
			_, err := newTestClient(ts).fetchSummaryBoards(t.Context(), "")
			if err == nil {
				t.Fatal("expected board retrieval failure")
			}
		})
	}
}

func TestFetchSummaryBoardsRejectsDuplicateResources(t *testing.T) {
	for _, tc := range []struct {
		name          string
		boardType     string
		duplicatePath string
	}{
		{name: "boards", duplicatePath: "/rest/agile/1.0/board"},
		{name: "sprints", boardType: "scrum", duplicatePath: "/rest/agile/1.0/board/4/sprint"},
		{name: "kanban issues", boardType: "kanban", duplicatePath: "/rest/agile/1.0/board/4/issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/agile/1.0/board" {
					if tc.duplicatePath == r.URL.Path {
						board := map[string]any{"id": 4, "name": "Board", "type": tc.boardType}
						jsonResponse(t, w, 200, map[string]any{"values": []any{board, board}, "total": 2, "isLast": true})
					} else {
						jsonResponse(t, w, 200, map[string]any{"values": []any{map[string]any{"id": 4, "name": "Board", "type": tc.boardType}}, "total": 1, "isLast": true})
					}
					return
				}
				if strings.HasPrefix(r.URL.Path, tc.duplicatePath) && tc.duplicatePath != "/rest/agile/1.0/board" {
					switch tc.name {
					case "sprints":
						sprint := map[string]any{"id": 8, "name": "Sprint"}
						jsonResponse(t, w, 200, map[string]any{"values": []any{sprint, sprint}, "total": 2, "isLast": true})
					case "kanban issues":
						issue := map[string]any{"key": "TEST-1", "fields": map[string]any{"statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}}}
						jsonResponse(t, w, 200, map[string]any{"issues": []any{issue, issue}, "total": 2, "isLast": true})
					}
					return
				}
				t.Errorf("unexpected request %s", r.URL.String())
				http.NotFound(w, r)
			}))
			defer ts.Close()
			_, err := newTestClient(ts).fetchSummaryBoards(t.Context(), "")
			if err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("expected duplicate resource error, got %v", err)
			}
		})
	}
}

func TestFetchSummaryUsesValidatedOpenAndBoardScopedJQL(t *testing.T) {
	var searchQueries, kanbanQueries []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
			body := readBody(t, r)
			query, _ := body["jql"].(string)
			searchQueries = append(searchQueries, query)
			jsonResponse(t, w, 200, map[string]any{"issues": []any{map[string]any{"key": "TEST-1", "fields": map[string]any{"summary": "Open", "status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"emailAddress": "test@example.com", "accountId": "acct"}}}}, "total": 1, "isLast": true})
		case r.URL.Path == "/rest/agile/1.0/board":
			jsonResponse(t, w, 200, map[string]any{"values": []any{map[string]any{"id": 4, "name": "Scrum Board", "type": "scrum"}, map[string]any{"id": 5, "name": "Kanban Board", "type": "kanban"}}, "startAt": 0, "total": 2, "isLast": true})
		case r.URL.Path == "/rest/agile/1.0/board/4/sprint":
			jsonResponse(t, w, 200, map[string]any{"values": []any{map[string]any{"id": 8, "name": "Sprint 1", "state": "active"}}, "startAt": 0, "total": 1, "isLast": true})
		case r.URL.Path == "/rest/agile/1.0/board/5/issue":
			kanbanQueries = append(kanbanQueries, r.URL.Query().Get("jql"))
			jsonResponse(t, w, 200, map[string]any{"issues": []any{map[string]any{"key": "TEST-1", "fields": map[string]any{"status": map[string]any{"name": "Open"}, "statusCategory": map[string]any{"key": "new"}, "project": map[string]any{"key": "TEST"}, "assignee": map[string]any{"accountId": "acct"}}}}, "startAt": 0, "total": 1, "isLast": true})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := newTestClient(ts)
	c.authEmail = "test@example.com"
	boards, issues, err := c.FetchSummary(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantSearch := []string{
		`project = TEST AND assignee = "test@example.com" AND statusCategory != Done ORDER BY priority ASC, status ASC`,
		`project = TEST AND assignee = "test@example.com" AND sprint = 8 AND statusCategory != Done ORDER BY priority ASC`,
	}
	if fmt.Sprint(searchQueries) != fmt.Sprint(wantSearch) {
		t.Fatalf("search JQL = %q, want %q", searchQueries, wantSearch)
	}
	wantKanban := `assignee = "test@example.com" AND statusCategory != Done ORDER BY priority ASC, status ASC`
	if fmt.Sprint(kanbanQueries) != fmt.Sprint([]string{wantKanban}) {
		t.Fatalf("kanban JQL = %q, want %q", kanbanQueries, wantKanban)
	}
	if len(issues) != 1 || len(boards) != 2 || boards[0].SprintName != "Sprint 1" || boards[0].Issues[0].Board != "Scrum Board" || boards[0].Issues[0].Sprint != "Sprint 1" || boards[1].Issues[0].Board != "Kanban Board" {
		t.Fatalf("summary board/sprint labels missing: boards=%#v issues=%#v", boards, issues)
	}
}

func TestSummaryKanbanGuardAllowsBoardScopedQueryWithoutDefaultProject(t *testing.T) {
	var gotJQL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/agile/1.0/board":
			jsonResponse(t, w, 200, map[string]any{"values": []any{map[string]any{"id": 5, "name": "Kanban", "type": "kanban"}}})
		case "/rest/agile/1.0/board/5/issue":
			gotJQL = r.URL.Query().Get("jql")
			jsonResponse(t, w, 200, map[string]any{"issues": []any{}})
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := newTestClient(ts)
	c.project = ""
	if _, err := c.fetchSummaryBoards(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if gotJQL != `assignee = "test@example.com" AND statusCategory != Done ORDER BY priority ASC, status ASC` || strings.Contains(gotJQL, "project =") {
		t.Fatalf("board-scoped query unexpectedly depended on default project: %q", gotJQL)
	}
}
