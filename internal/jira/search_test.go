package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func searchPage(keys []string, meta map[string]any) map[string]any {
	issues := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		issues = append(issues, map[string]any{"key": key, "fields": map[string]any{"summary": key}})
	}
	page := map[string]any{"issues": issues}
	for k, v := range meta {
		page[k] = v
	}
	return page
}

func runSearch(t *testing.T, handler http.Handler, opts SearchOptions) (SearchResult, error) {
	t.Helper()
	ts := httptest.NewServer(handler)
	defer ts.Close()
	return newTestClient(ts).SearchJQLResult(context.Background(), "project = TEST", opts)
}

func assertKeys(t *testing.T, result SearchResult, want ...string) {
	t.Helper()
	got := make([]string, len(result.Issues))
	for i, issue := range result.Issues {
		got[i] = issue.Key
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("issue keys = %v, want %v", got, want)
	}
}

func assertTotal(t *testing.T, info SearchInfo, want int, kind CountKind) {
	t.Helper()
	if info.Total == nil || *info.Total != want || info.TotalKind != kind {
		t.Fatalf("total = %v (%s), want %d (%s)", info.Total, info.TotalKind, want, kind)
	}
}

func TestSearchJQLResultCarriesJQLAndPageToken(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["jql"] != "project = TEST" {
			t.Errorf("JQL = %v", body["jql"])
		}
		if body["maxResults"] != float64(2) {
			t.Errorf("maxResults = %v", body["maxResults"])
		}
		if requests == 1 {
			if _, ok := body["nextPageToken"]; ok {
				t.Errorf("first request unexpectedly has token: %v", body["nextPageToken"])
			}
			jsonResponse(t, w, 200, searchPage([]string{"TEST-1"}, map[string]any{"total": 3, "nextPageToken": "opaque / + token"}))
			return
		}
		if body["nextPageToken"] != "opaque / + token" {
			t.Errorf("next token = %v", body["nextPageToken"])
		}
		jsonResponse(t, w, 200, searchPage([]string{"TEST-2", "TEST-3"}, map[string]any{"total": 3, "isLast": true}))
	}))
	defer server.Close()
	got, err := newTestClient(server).SearchJQLResult(context.Background(), "project = TEST", SearchOptions{Limit: 5, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, got, "TEST-1", "TEST-2", "TEST-3")
	if requests != 2 || !got.Complete || got.Enumerated != 3 || got.Returned != 3 {
		t.Fatalf("requests=%d info=%+v", requests, got.SearchInfo)
	}
	assertTotal(t, got.SearchInfo, 3, CountExact)
}

func TestSearchJQLResultTerminalMetadataAndUnknownCases(t *testing.T) {
	tests := []struct {
		name         string
		body         map[string]any
		wantN        int
		wantComplete bool
		wantKind     CountKind
		wantTotal    *int
	}{
		{name: "missing metadata is unknown", body: searchPage([]string{"TEST-1"}, nil), wantN: 1, wantKind: CountUnknown},
		{name: "empty terminal is exact zero", body: searchPage(nil, map[string]any{"isLast": true, "total": 0}), wantComplete: true, wantKind: CountExact, wantTotal: ptr(0)},
		{name: "empty missing metadata remains unknown", body: searchPage(nil, nil), wantKind: CountUnknown},
		{name: "reported is unverified", body: searchPage([]string{"TEST-1"}, map[string]any{"total": 10}), wantN: 1, wantKind: CountReported, wantTotal: ptr(10)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { jsonResponse(t, w, 200, tc.body) }), SearchOptions{Limit: 10})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Issues) != tc.wantN || got.Complete != tc.wantComplete || got.TotalKind != tc.wantKind {
				t.Fatalf("result = %+v", got.SearchInfo)
			}
			if tc.wantTotal == nil {
				if got.Total != nil {
					t.Fatalf("total = %v, want unknown", *got.Total)
				}
			} else if got.Total == nil || *got.Total != *tc.wantTotal {
				t.Fatalf("total = %v, want %d", got.Total, *tc.wantTotal)
			}
		})
	}
}

func ptr(n int) *int { return &n }

func TestSearchJQLResultLimitAndTerminalBoundary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		keys         []string
		terminal     bool
		wantComplete bool
		wantHasMore  *bool
	}{
		{name: "terminal at exact boundary", keys: []string{"TEST-1", "TEST-2"}, terminal: true, wantComplete: true, wantHasMore: ptrBool(false)},
		{name: "terminal with server overshoot", keys: []string{"TEST-1", "TEST-2", "TEST-3"}, terminal: true, wantComplete: false, wantHasMore: ptrBool(true)},
		{name: "token at limit", keys: []string{"TEST-1", "TEST-2", "TEST-3"}, terminal: false, wantComplete: false, wantHasMore: ptrBool(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				jsonResponse(t, w, 200, searchPage(tc.keys, map[string]any{"isLast": tc.terminal, "nextPageToken": func() any {
					if tc.terminal {
						return nil
					}
					return "next"
				}()}))
			}), SearchOptions{Limit: 2, PageSize: 3})
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			assertKeys(t, got, "TEST-1", "TEST-2")
			if calls != 1 || got.Complete != tc.wantComplete || got.HasMore == nil || *got.HasMore != *tc.wantHasMore {
				t.Fatalf("calls=%d info=%+v", calls, got.SearchInfo)
			}
		})
	}
}

func ptrBool(b bool) *bool { return &b }

func TestSearchJQLResultRejectsInvalidPagination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		first     map[string]any
		second    map[string]any
		wantCalls int
	}{
		{"false without token", map[string]any{"isLast": false}, nil, 1},
		{"blank token", map[string]any{"nextPageToken": "  "}, nil, 1},
		{"conflicting terminal token", map[string]any{"isLast": true, "nextPageToken": "next"}, nil, 1},
		{"repeated token", map[string]any{"nextPageToken": "same"}, map[string]any{"nextPageToken": "same"}, 2},
		{"token cycle", map[string]any{"nextPageToken": "a"}, map[string]any{"nextPageToken": "b"}, 3},
		{"non-string token", map[string]any{"nextPageToken": 42}, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				meta := tc.first
				if calls == 2 {
					meta = tc.second
				} else if calls > 2 {
					meta = map[string]any{"nextPageToken": "a"}
				}
				jsonResponse(t, w, 200, searchPage([]string{fmt.Sprintf("TEST-%d", calls)}, meta))
			}), SearchOptions{Limit: 10})
			if err == nil {
				t.Fatal("expected protocol error")
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want %d", calls, tc.wantCalls)
			}
			if got.Complete || got.TotalKind == CountExact {
				t.Fatalf("invalid result claimed complete/exact: %+v", got.SearchInfo)
			}
		})
	}
}

func TestSearchJQLResultUnknownPaginationClearsStaleHasMore(t *testing.T) {
	calls := 0
	got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			jsonResponse(t, w, 200, searchPage([]string{"TEST-1"}, map[string]any{"nextPageToken": "next"}))
			return
		}
		jsonResponse(t, w, 200, searchPage([]string{"TEST-2"}, nil))
	}), SearchOptions{Limit: 10})
	if err != nil || calls != 2 || got.Complete || got.HasMore != nil || got.TotalKind != CountUnknown {
		t.Fatalf("calls=%d info=%+v err=%v", calls, got.SearchInfo, err)
	}
}

func TestSearchJQLResultExactCountScansPastPreviewAndRejectsDuplicate(t *testing.T) {
	t.Run("exact count", func(t *testing.T) {
		calls := 0
		got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				jsonResponse(t, w, 200, searchPage([]string{"TEST-1", "TEST-2"}, map[string]any{"total": 3, "nextPageToken": "next"}))
				return
			}
			jsonResponse(t, w, 200, searchPage([]string{"TEST-3"}, map[string]any{"total": 3, "isLast": true}))
		}), SearchOptions{Limit: 1, PageSize: 2, ExactCount: true})
		if err != nil {
			t.Fatal(err)
		}
		assertKeys(t, got, "TEST-1")
		if calls != 2 || got.Enumerated != 3 || got.Complete || got.HasMore == nil || !*got.HasMore {
			t.Fatalf("calls=%d info=%+v", calls, got.SearchInfo)
		}
		assertTotal(t, got.SearchInfo, 3, CountExact)
	})
	t.Run("duplicate key is incomplete", func(t *testing.T) {
		got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			jsonResponse(t, w, 200, searchPage([]string{"TEST-1", "TEST-1"}, map[string]any{"isLast": true}))
		}), SearchOptions{Limit: 10, ExactCount: true})
		if err == nil {
			t.Fatal("expected duplicate-key error")
		}
		if got.Complete || got.TotalKind == CountExact {
			t.Fatalf("duplicate inflated exact count: %+v", got.SearchInfo)
		}
	})
}

func TestSearchJQLResultInconsistentTotalsAndPageFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages []map[string]any
	}{
		{"negative", []map[string]any{searchPage([]string{"TEST-1"}, map[string]any{"total": -1, "isLast": true})}},
		{"decreasing", []map[string]any{searchPage([]string{"TEST-1"}, map[string]any{"total": 3, "nextPageToken": "next"}), searchPage([]string{"TEST-2"}, map[string]any{"total": 1, "isLast": true})}},
		{"disagrees with enumeration", []map[string]any{searchPage([]string{"TEST-1", "TEST-2"}, map[string]any{"total": 1, "isLast": true})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				p := tc.pages[calls]
				calls++
				jsonResponse(t, w, 200, p)
			}), SearchOptions{Limit: 10, ExactCount: true})
			if err == nil {
				t.Fatal("expected inconsistent total error")
			}
			if got.Complete || got.TotalKind == CountExact {
				t.Fatalf("inconsistent result claimed exact: %+v", got.SearchInfo)
			}
		})
	}
	var calls atomic.Int32
	got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			jsonResponse(t, w, 200, searchPage([]string{"TEST-1"}, map[string]any{"total": 2, "nextPageToken": "next"}))
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}), SearchOptions{Limit: 10, ExactCount: true})
	if err == nil || got.Complete || got.TotalKind == CountExact || got.Enumerated != 1 {
		t.Fatalf("partial failure result=%+v err=%v", got.SearchInfo, err)
	}
}

func TestSearchJQLResultMaxPagesAndCancellation(t *testing.T) {
	t.Run("page budget", func(t *testing.T) {
		calls := 0
		got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			jsonResponse(t, w, 200, searchPage([]string{fmt.Sprintf("TEST-%d", calls)}, map[string]any{"nextPageToken": fmt.Sprintf("token-%d", calls)}))
		}), SearchOptions{Limit: 20, MaxPages: 2, PageSize: 1, ExactCount: true})
		if err == nil || calls != 2 || got.Enumerated != 2 || got.Complete || got.TotalKind == CountExact {
			t.Fatalf("calls=%d info=%+v err=%v", calls, got.SearchInfo, err)
		}
	})
	t.Run("cancel between pages", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			jsonResponse(t, w, 200, searchPage([]string{"TEST-1"}, map[string]any{"nextPageToken": "next"}))
			cancel()
		}))
		defer ts.Close()
		got, err := newTestClient(ts).SearchJQLResult(ctx, "project = TEST", SearchOptions{Limit: 10, ExactCount: true})
		if err == nil || calls != 1 || got.Complete || got.TotalKind == CountExact {
			t.Fatalf("calls=%d info=%+v err=%v", calls, got.SearchInfo, err)
		}
	})
	t.Run("cancel during page", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		entered := make(chan struct{})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
				t.Error("request context did not cancel")
			}
		}))
		defer ts.Close()
		result := make(chan struct {
			got SearchResult
			err error
		}, 1)
		go func() {
			got, err := newTestClient(ts).SearchJQLResult(ctx, "project = TEST", SearchOptions{Limit: 10})
			result <- struct {
				got SearchResult
				err error
			}{got, err}
		}()
		<-entered
		cancel()
		out := <-result
		if out.err == nil || calls.Load() != 1 || out.got.Complete || out.got.TotalKind == CountExact {
			t.Fatalf("calls=%d info=%+v err=%v", calls.Load(), out.got.SearchInfo, out.err)
		}
	})
}

func TestSearchJQLResultExactAndListMissingMetadata(t *testing.T) {
	response := searchPage([]string{"TEST-1"}, nil)
	list, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { jsonResponse(t, w, 200, response) }), SearchOptions{Limit: 10})
	if err != nil || list.Complete || list.TotalKind != CountUnknown {
		t.Fatalf("list=%+v err=%v", list.SearchInfo, err)
	}
	count, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { jsonResponse(t, w, 200, response) }), SearchOptions{Limit: 10, ExactCount: true})
	if err == nil || count.Complete || count.TotalKind == CountExact {
		t.Fatalf("exact count=%+v err=%v", count.SearchInfo, err)
	}
}

func TestSearchJQLResultShortPageDoesNotProveExhaustion(t *testing.T) {
	calls := 0
	got, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			jsonResponse(t, w, 200, searchPage([]string{"TEST-1"}, map[string]any{"nextPageToken": "next"}))
			return
		}
		jsonResponse(t, w, 200, searchPage([]string{"TEST-2"}, map[string]any{"isLast": true}))
	}), SearchOptions{Limit: 10, PageSize: 100})
	if err != nil || calls != 2 || !got.Complete {
		t.Fatalf("calls=%d info=%+v err=%v", calls, got.SearchInfo, err)
	}
}

func TestSearchJQLResultRejectsInvalidOptions(t *testing.T) {
	for _, opts := range []SearchOptions{{Limit: -1}, {PageSize: -1}, {MaxPages: -1}} {
		_, err := runSearch(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("request made for invalid options") }), opts)
		if err == nil {
			t.Fatalf("opts=%+v err=%v", opts, err)
		}
	}
}
