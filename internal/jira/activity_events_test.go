package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newActivityTestClient(server *httptest.Server) *Client {
	return &Client{http: server.Client(), base: server.URL, email: "user", token: "token", ctx: context.Background()}
}

func writeActivityJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestReadActivityEventsPaginatesAllEndpointsAndFiltersStatusChanges(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		mu.Lock()
		requests = append(requests, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("maxResults") != "100" {
			t.Errorf("maxResults = %q, want 100", r.URL.Query().Get("maxResults"))
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		switch r.URL.Path {
		case "/rest/api/3/issue/PROJ-1/comment":
			if start == 0 {
				writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 1, "total": 2, "comments": []any{
					map[string]any{"id": "c1", "created": "2026-10-12T10:00:00.000-0400", "author": map[string]any{"accountId": "user-1"}, "body": "private comment body"},
				}})
				return
			}
			writeActivityJSON(w, map[string]any{"startAt": 1, "maxResults": 1, "total": 2, "comments": []any{
				map[string]any{"id": "c2", "created": "2026-10-13T10:00:00Z", "author": map[string]any{"accountId": "user-2"}, "body": "another private body"},
			}})
		case "/rest/api/3/issue/PROJ-1/worklog":
			if start == 0 {
				writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 1, "total": 2, "worklogs": []any{
					map[string]any{"id": "w1", "started": "2026-10-12T11:00:00.000+0000", "author": map[string]any{"accountId": "user-1"}, "timeSpentSeconds": 900, "comment": "private worklog text"},
				}})
				return
			}
			writeActivityJSON(w, map[string]any{"startAt": 1, "maxResults": 1, "total": 2, "worklogs": []any{
				map[string]any{"id": "w2", "started": "2026-10-14T11:00:00-0400", "author": map[string]any{"accountId": "user-2"}},
			}})
		case "/rest/api/3/issue/PROJ-1/changelog":
			if start == 0 {
				writeActivityJSON(w, map[string]any{
					"startAt": 0, "maxResults": 1, "total": 2, "isLast": false,
					"values": []any{map[string]any{"id": "h1", "items": []any{map[string]any{"field": "summary"}}}},
				})
				return
			}
			writeActivityJSON(w, map[string]any{
				"startAt": 1, "maxResults": 1, "total": 2, "isLast": true,
				"values": []any{map[string]any{
					"id": "h2", "created": "2026-10-15T12:00:00.000+0000",
					"author": map[string]any{"accountId": "user-3"},
					"items": []any{
						map[string]any{"fieldId": "status", "fromString": "Open", "toString": "In Progress"},
						map[string]any{"field": "priority", "fromString": "Low", "toString": "High"},
						map[string]any{"field": "status", "fromString": "In Progress", "toString": "Done"},
					},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5: %+v", len(got), got)
	}
	if got[0].Kind != "comment" || got[0].ID != "c1" || got[0].AuthorID != "user-1" ||
		!got[0].At.Equal(time.Date(2026, time.October, 12, 14, 0, 0, 0, time.UTC)) {
		t.Errorf("first comment = %+v", got[0])
	}
	if got[2].Kind != "worklog" || got[2].Detail != "time spent: 900 seconds" || strings.Contains(got[2].Detail, "private") {
		t.Errorf("worklog detail leaked or missing: %+v", got[2])
	}
	if got[4].Kind != "status" || got[4].ID != "h2" || got[4].Detail != "status: Open -> In Progress; status: In Progress -> Done" {
		t.Errorf("status event = %+v", got[4])
	}
	for _, event := range got {
		if strings.Contains(event.Detail, "private") {
			t.Errorf("event contains body text: %+v", event)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 6 {
		t.Fatalf("made %d requests, want two pages per endpoint: %v", len(requests), requests)
	}
	for _, endpoint := range []string{"/comment", "/worklog", "/changelog"} {
		count := 0
		for _, request := range requests {
			if strings.Contains(request, endpoint) {
				count++
			}
		}
		if count != 2 {
			t.Errorf("%s requested %d pages, want two", endpoint, count)
		}
	}
}

func TestReadActivityEventsPreservesPartialEventsOnEndpointError(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/comment") {
			writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 1, "total": 1, "comments": []any{
				map[string]any{"id": "c1", "created": "2026-10-12T10:00:00Z", "author": map[string]any{"accountId": "user-1"}},
			}})
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 1)
	if err == nil || len(events) != 1 || events[0].ID != "c1" {
		t.Fatalf("events = %+v, error = %v; want one partial comment and error", events, err)
	}
	if len(requests) != 2 || strings.HasSuffix(requests[1], "/changelog") {
		t.Errorf("reader continued after endpoint failure: %v", requests)
	}
}

func TestReadActivityEventsPreservesValidRowsBeforeMalformedRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/comment") {
			t.Errorf("unexpected endpoint after malformed page: %s", r.URL.Path)
		}
		writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 2, "comments": []any{
			map[string]any{"id": "c1", "created": "2026-10-12T10:00:00Z", "author": map[string]any{"accountId": "user-1"}},
			map[string]any{"id": "c2", "created": "not-a-time", "author": map[string]any{"accountId": "user-2"}},
		}})
	}))
	defer server.Close()

	events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 1)
	if err == nil || len(events) != 1 || events[0].ID != "c1" {
		t.Fatalf("events = %+v, error = %v; want first valid event and an error", events, err)
	}
}

func TestReadActivityEventsRequiresStatusActorAndTime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry map[string]any
	}{
		{
			name: "missing actor",
			entry: map[string]any{"id": "h1", "created": "2026-10-12T10:00:00Z", "items": []any{
				map[string]any{"fieldId": "status", "fromString": "Open", "toString": "Done"},
			}},
		},
		{
			name: "invalid time",
			entry: map[string]any{"id": "h1", "created": "invalid", "author": map[string]any{"accountId": "user-1"}, "items": []any{
				map[string]any{"field": "status", "fromString": "Open", "toString": "Done"},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/comment"):
					writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "comments": []any{}})
				case strings.HasSuffix(r.URL.Path, "/worklog"):
					writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "worklogs": []any{}})
				case strings.HasSuffix(r.URL.Path, "/changelog"):
					writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 1, "values": []any{tc.entry}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 1)
			if err == nil || len(events) != 0 {
				t.Fatalf("events = %+v, error = %v; want error and no malformed status event", events, err)
			}
		})
	}
}

func TestReadActivityEventsRejectsMissingOrNullRecordArrays(t *testing.T) {
	for _, endpointCase := range []struct {
		name     string
		endpoint string
		arrayKey string
	}{
		{"comments", "/comment", "comments"},
		{"worklogs", "/worklog", "worklogs"},
		{"changelog values", "/changelog", "values"},
	} {
		for _, arrayValue := range []struct {
			name string
			set  bool
		}{
			{"missing", false}, {"null", true},
		} {
			t.Run(endpointCase.name+"/"+arrayValue.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasSuffix(r.URL.Path, endpointCase.endpoint) {
						key := "comments"
						if strings.HasSuffix(r.URL.Path, "/worklog") {
							key = "worklogs"
						} else if strings.HasSuffix(r.URL.Path, "/changelog") {
							key = "values"
						}
						writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, key: []any{}})
						return
					}
					response := map[string]any{"startAt": 0, "maxResults": 100, "total": 0}
					if arrayValue.set {
						response[endpointCase.arrayKey] = nil
					}
					writeActivityJSON(w, response)
				}))
				defer server.Close()
				events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 1)
				if err == nil || len(events) != 0 {
					t.Fatalf("events = %+v, error = %v; want missing-array error", events, err)
				}
			})
		}
	}
}

func TestReadActivityEventsRequiresChangelogItemsAndFieldIdentity(t *testing.T) {
	entries := []struct {
		name  string
		entry map[string]any
	}{
		{"missing items", map[string]any{"id": "h1"}},
		{"null items", map[string]any{"id": "h1", "items": nil}},
		{"unknown field", map[string]any{"id": "h1", "items": []any{map[string]any{"fromString": "x", "toString": "y"}}}},
	}
	for _, tc := range entries {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/comment"):
					writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "comments": []any{}})
				case strings.HasSuffix(r.URL.Path, "/worklog"):
					writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "worklogs": []any{}})
				case strings.HasSuffix(r.URL.Path, "/changelog"):
					writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 1, "values": []any{tc.entry}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			if events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 1); err == nil || len(events) != 0 {
				t.Fatalf("events = %+v, error = %v; want changelog shape error", events, err)
			}
		})
	}
}

func TestReadActivityEventsRejectsNegativeWorklogDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/comment"):
			writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "comments": []any{}})
		case strings.HasSuffix(r.URL.Path, "/worklog"):
			writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 1, "worklogs": []any{
				map[string]any{"id": "w1", "started": "2026-10-12T10:00:00Z", "author": map[string]any{"accountId": "user-1"}, "timeSpentSeconds": -1},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", 1); err == nil || len(events) != 0 {
		t.Fatalf("events = %+v, error = %v; want negative duration error", events, err)
	}
}

func TestReadActivityEventsPropagatesCancellationToHTTPRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := newActivityTestClient(server).ReadActivityEvents(ctx, "PROJ-1", 1)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("HTTP request ignored cancellation")
	}
}

func TestReadActivityEventsRejectsMalformedPaginationAndRows(t *testing.T) {
	baseComment := map[string]any{"id": "c1", "created": "2026-10-12T10:00:00Z", "author": map[string]any{"accountId": "user-1"}}
	tests := []struct {
		name       string
		responses  []map[string]any
		maxPages   int
		wantEvents int
	}{
		{"missing metadata", []map[string]any{{"comments": []any{baseComment}}}, 1, 0},
		{"wrong startAt", []map[string]any{{"startAt": 1, "maxResults": 100, "total": 1, "comments": []any{baseComment}}}, 1, 0},
		{"nonpositive maxResults", []map[string]any{{"startAt": 0, "maxResults": 0, "total": 1, "comments": []any{baseComment}}}, 1, 0},
		{"negative total", []map[string]any{{"startAt": 0, "maxResults": 100, "total": -1, "comments": []any{}}}, 1, 0},
		{"length exceeds maxResults", []map[string]any{{"startAt": 0, "maxResults": 1, "total": 2, "comments": []any{baseComment, baseComment}}}, 1, 0},
		{"length exceeds remaining total", []map[string]any{{"startAt": 0, "maxResults": 2, "total": 1, "comments": []any{baseComment, baseComment}}}, 1, 0},
		{"isLast disagrees with total", []map[string]any{{"startAt": 0, "maxResults": 100, "total": 2, "isLast": true, "comments": []any{baseComment}}}, 1, 1},
		{"nonprogressing page", []map[string]any{{"startAt": 0, "maxResults": 100, "total": 1, "comments": []any{}}}, 1, 0},
		{"changed total", []map[string]any{
			{"startAt": 0, "maxResults": 1, "total": 2, "comments": []any{baseComment}},
			{"startAt": 1, "maxResults": 1, "total": 3, "comments": []any{map[string]any{"id": "c2", "created": "2026-10-12T10:00:00Z", "author": map[string]any{"accountId": "user-1"}}}},
		}, 3, 1},
		{"duplicate record", []map[string]any{
			{"startAt": 0, "maxResults": 1, "total": 2, "comments": []any{baseComment}},
			{"startAt": 1, "maxResults": 1, "total": 2, "comments": []any{baseComment}},
		}, 3, 1},
		{"page cap", []map[string]any{{"startAt": 0, "maxResults": 1, "total": 2, "comments": []any{baseComment}}}, 1, 1},
		{"missing author", []map[string]any{{"startAt": 0, "maxResults": 100, "total": 1, "comments": []any{map[string]any{"id": "c1", "created": "2026-10-12T10:00:00Z"}}}}, 1, 0},
		{"invalid timestamp", []map[string]any{{"startAt": 0, "maxResults": 100, "total": 1, "comments": []any{map[string]any{"id": "c1", "created": "not-a-time", "author": map[string]any{"accountId": "user-1"}}}}}, 1, 0},
		{"missing ID", []map[string]any{{"startAt": 0, "maxResults": 100, "total": 1, "comments": []any{map[string]any{"created": "2026-10-12T10:00:00Z", "author": map[string]any{"accountId": "user-1"}}}}}, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var call int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call++
				if !strings.HasSuffix(r.URL.Path, "/comment") {
					t.Errorf("unexpected endpoint after malformed comments: %s", r.URL.Path)
				}
				if call > len(tc.responses) {
					t.Fatalf("unexpected extra request %d", call)
				}
				response := make(map[string]any, len(tc.responses[call-1]))
				for key, value := range tc.responses[call-1] {
					response[key] = value
				}
				comments, exists := response["comments"]
				if exists {
					response["comments"] = comments
				}
				writeActivityJSON(w, response)
			}))
			defer server.Close()
			events, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ-1", tc.maxPages)
			if err == nil || len(events) != tc.wantEvents {
				t.Fatalf("events = %+v, err = %v; want error and %d events", events, err, tc.wantEvents)
			}
		})
	}
}

func TestReadActivityEventsRejectsInvalidArgumentsAndHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s", r.URL)
	}))
	defer server.Close()
	client := newActivityTestClient(server)
	for _, tc := range []struct {
		key string
		max int
	}{
		{"", 1}, {"PROJ-1", 0}, {"PROJ-1", 101},
	} {
		if _, err := client.ReadActivityEvents(context.Background(), tc.key, tc.max); err == nil {
			t.Errorf("key=%q maxPages=%d unexpectedly succeeded", tc.key, tc.max)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ReadActivityEvents(ctx, "PROJ-1", 1); err == nil {
		t.Fatal("cancelled request unexpectedly succeeded")
	}
}

func TestActivityIssueKeyIsPathEscaped(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.EscapedPath())
		switch {
		case strings.HasSuffix(r.URL.EscapedPath(), "/comment"):
			writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "comments": []any{}})
		case strings.HasSuffix(r.URL.EscapedPath(), "/worklog"):
			writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "worklogs": []any{}})
		case strings.HasSuffix(r.URL.EscapedPath(), "/changelog"):
			writeActivityJSON(w, map[string]any{"startAt": 0, "maxResults": 100, "total": 0, "values": []any{}})
		}
	}))
	defer server.Close()
	_, err := newActivityTestClient(server).ReadActivityEvents(context.Background(), "PROJ/1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPaths) != 3 {
		t.Fatalf("got paths %v, want three endpoint requests", gotPaths)
	}
	for _, gotPath := range gotPaths {
		if !strings.Contains(gotPath, url.PathEscape("PROJ/1")) {
			t.Errorf("request path = %q, issue key was not escaped", gotPath)
		}
	}
}
