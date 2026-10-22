package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type activityHTTPResponse struct {
	status int
	body   any
}

type activityAPIFixture struct {
	profile       activityHTTPResponse
	searchPages   map[string]activityHTTPResponse
	activities    map[string]map[string]activityHTTPResponse
	userSearch    activityHTTPResponse
	userByAccount activityHTTPResponse

	mu             sync.Mutex
	searchRequests []map[string]any
	paths          []string
}

func newActivityWorkflowClient(t *testing.T, fixture *activityAPIFixture) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.paths = append(fixture.paths, r.Method+" "+r.URL.Path)
		fixture.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/myself":
			writeActivityResponse(t, w, fixture.profile)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/user/search":
			writeActivityResponse(t, w, fixture.userSearch)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/user":
			writeActivityResponse(t, w, fixture.userByAccount)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
			request := readBody(t, r)
			fixture.mu.Lock()
			fixture.searchRequests = append(fixture.searchRequests, request)
			fixture.mu.Unlock()
			token, _ := request["nextPageToken"].(string)
			response, ok := fixture.searchPages[token]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeActivityResponse(t, w, response)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/"), "/")
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			endpoint := parts[1]
			response, ok := fixture.activities[parts[0]][endpoint]
			if !ok {
				response = activityPageResponse(endpoint, []any{})
			}
			writeActivityResponse(t, w, response)
		default:
			t.Errorf("unexpected activity request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	client := newTestClient(server)
	client.authEmail = "authenticated@example.com"
	return client, server.Close
}

func writeActivityResponse(t *testing.T, w http.ResponseWriter, response activityHTTPResponse) {
	t.Helper()
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	jsonResponse(t, w, status, response.body)
}

func activityProfile(accountID, email, zone string) activityHTTPResponse {
	return activityHTTPResponse{body: map[string]any{"accountId": accountID, "emailAddress": email, "timeZone": zone}}
}

func activitySearchResponse(rows []any, total int, last bool, next string) activityHTTPResponse {
	if rows == nil {
		rows = []any{}
	}
	body := map[string]any{"issues": rows, "total": total, "isLast": last}
	if next != "" {
		body["nextPageToken"] = next
		delete(body, "isLast")
	}
	return activityHTTPResponse{body: body}
}

func activityIssueRow(key, project, assigneeID, updated string) any {
	var assignee any
	if assigneeID != "" {
		assignee = map[string]any{"accountId": assigneeID, "emailAddress": assigneeID + "@example.com"}
	}
	return map[string]any{
		"key": key,
		"fields": map[string]any{
			"summary": key + " summary", "updated": updated,
			"project":        map[string]any{"key": project},
			"status":         map[string]any{"name": "Open"},
			"statusCategory": map[string]any{"key": "new"},
			"assignee":       assignee,
		},
	}
}

func activityPageResponse(endpoint string, records []any) activityHTTPResponse {
	field := map[string]string{"comment": "comments", "worklog": "worklogs", "changelog": "values"}[endpoint]
	if records == nil {
		records = []any{}
	}
	return activityHTTPResponse{body: map[string]any{
		"startAt": 0, "maxResults": 100, "total": len(records), "isLast": true, field: records,
	}}
}

func activityCommentRecord(id, created, actor string) any {
	return map[string]any{"id": id, "created": created, "author": map[string]any{"accountId": actor}, "body": "private body"}
}

func activityWorklogRecord(id, started, created, actor string) any {
	return map[string]any{"id": id, "started": started, "created": created, "author": map[string]any{"accountId": actor}, "timeSpentSeconds": 60}
}

func activityStatusRecord(id, created, actor string, items []any) any {
	return map[string]any{"id": id, "created": created, "author": map[string]any{"accountId": actor}, "items": items}
}

func baseActivityFixture(rows ...any) *activityAPIFixture {
	return &activityAPIFixture{
		profile:     activityProfile("actor-1", "authenticated@example.com", "UTC"),
		searchPages: map[string]activityHTTPResponse{"": activitySearchResponse(rows, len(rows), true, "")},
		activities:  make(map[string]map[string]activityHTTPResponse),
		userSearch:  activityHTTPResponse{body: []any{}},
	}
}

func (f *activityAPIFixture) setEvents(issue string, comment, worklog, changelog activityHTTPResponse) {
	f.activities[issue] = map[string]activityHTTPResponse{
		"comment": comment, "worklog": worklog, "changelog": changelog,
	}
}

func defaultActivityOptions() WorkActivityOptions {
	return WorkActivityOptions{Project: "TEST", StartDate: "2026-10-12", EndDate: "2026-10-16", TimeZone: "UTC"}
}

func (f *activityAPIFixture) searchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.searchRequests)
}

func (f *activityAPIFixture) searchJQL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.searchRequests) == 0 {
		return ""
	}
	jql, _ := f.searchRequests[0]["jql"].(string)
	return jql
}

func TestWorkActivityUsesProjectOnlyCandidatesAndAuthoredEvidence(t *testing.T) {
	fixture := baseActivityFixture(
		activityIssueRow("TEST-1", "TEST", "other-assignee", "2026-12-01T10:00:00Z"),
		activityIssueRow("TEST-2", "TEST", "actor-1", "2026-12-01T11:00:00Z"),
	)
	fixture.setEvents("TEST-1",
		activityPageResponse("comment", []any{activityCommentRecord("c1", "2026-10-13T12:00:00Z", "actor-1")}),
		activityPageResponse("worklog", []any{activityWorklogRecord("w1", "2026-10-14T12:00:00Z", "2026-12-02T12:00:00Z", "actor-1")}),
		activityPageResponse("changelog", []any{
			activityStatusRecord("h1", "2026-10-15T12:00:00Z", "actor-1", []any{
				map[string]any{"fieldId": "summary", "fromString": "A", "toString": "B"},
				map[string]any{"field": "status", "fromString": "Open", "toString": "In Progress"},
			}),
			activityStatusRecord("h2", "2026-10-15T13:00:00Z", "other", []any{map[string]any{"fieldId": "status", "fromString": "Open", "toString": "Done"}}),
			activityStatusRecord("h3", "2026-10-15T14:00:00Z", "actor-1", []any{map[string]any{"field": "summary"}}),
		}),
	)
	fixture.setEvents("TEST-2",
		activityPageResponse("comment", []any{activityCommentRecord("c2", "2026-10-13T12:00:00Z", "other")}),
		activityPageResponse("worklog", []any{}),
		activityPageResponse("changelog", []any{}),
	)
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	result, err := client.WorkActivity(context.Background(), defaultActivityOptions())
	if err != nil {
		t.Fatalf("WorkActivity: %v", err)
	}
	if got, want := fixture.searchJQL(), "project = TEST ORDER BY key ASC"; got != want {
		t.Fatalf("candidate JQL = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"assignee", "status", "updated"} {
		if strings.Contains(strings.ToLower(fixture.searchJQL()), forbidden) {
			t.Fatalf("candidate JQL unexpectedly filtered by %q: %s", forbidden, fixture.searchJQL())
		}
	}
	if len(result.Matches) != 1 || result.Matches[0].Issue.Key != "TEST-1" {
		t.Fatalf("matches = %+v, want only authored activity on TEST-1", result.Matches)
	}
	events := result.Matches[0].Events
	if len(events) != 3 {
		t.Fatalf("events = %+v, want comment, worklog, and status", events)
	}
	if events[0].Kind != ActivityKindComment || events[1].Kind != ActivityKindWorklog || events[2].Kind != ActivityKindStatus {
		t.Fatalf("event kinds/order = %+v", events)
	}
	if events[1].At.Format(time.RFC3339) != "2026-10-14T12:00:00Z" {
		t.Errorf("worklog used created time instead of started time: %s", events[1].At)
	}
	if events[2].Detail != "status: Open -> In Progress" {
		t.Errorf("status event detail = %q; nonstatus/other-author changes must be excluded", events[2].Detail)
	}
	if !result.Complete || !result.CandidateComplete || result.Scanned != 2 {
		t.Errorf("completion/scanned = complete:%v candidates:%v scanned:%d", result.Complete, result.CandidateComplete, result.Scanned)
	}
}

func TestWorkActivityNewYorkInclusiveCalendarBounds(t *testing.T) {
	fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "other", "2026-10-20T00:00:00Z"))
	fixture.setEvents("TEST-1",
		activityPageResponse("comment", []any{
			activityCommentRecord("before", "2026-10-12T03:59:59Z", "actor-1"),
			activityCommentRecord("start", "2026-10-12T04:00:00Z", "actor-1"),
			activityCommentRecord("end", "2026-10-17T03:59:59Z", "actor-1"),
			activityCommentRecord("after", "2026-10-17T04:00:00Z", "actor-1"),
		}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.TimeZone = "America/New_York"
	result, err := client.WorkActivity(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || len(result.Matches[0].Events) != 2 {
		t.Fatalf("matched boundary events = %+v, want exact inclusive-start/exclusive-next-day behavior", result.Matches)
	}
	if result.Matches[0].Events[0].ID != "start" || result.Matches[0].Events[1].ID != "end" {
		t.Errorf("boundary event IDs = %s, %s", result.Matches[0].Events[0].ID, result.Matches[0].Events[1].ID)
	}
}

func TestWorkActivityCandidateLimitReturnsPartialAndIncomplete(t *testing.T) {
	fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z"))
	fixture.searchPages = map[string]activityHTTPResponse{
		"":     activitySearchResponse([]any{activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z")}, 2, false, "next"),
		"next": activitySearchResponse([]any{activityIssueRow("TEST-2", "TEST", "other", "2026-12-01T00:00:00Z")}, 2, true, ""),
	}
	fixture.setEvents("TEST-1", activityPageResponse("comment", []any{}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.MaxIssues = 1
	result, err := client.WorkActivity(context.Background(), opts)
	if !errors.Is(err, ErrIncompleteSearch) || result.Complete || result.CandidateComplete || result.Candidates != 1 {
		t.Fatalf("result=%+v err=%v; want partial one-candidate result and ErrIncompleteSearch", result, err)
	}
}

func TestWorkActivityEndpointFailureRetainsEvidenceAndMarksPartial(t *testing.T) {
	fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z"))
	fixture.setEvents("TEST-1",
		activityPageResponse("comment", []any{activityCommentRecord("c1", "2026-10-13T12:00:00Z", "actor-1")}),
		activityHTTPResponse{status: http.StatusForbidden, body: map[string]any{"error": "restricted"}},
		activityPageResponse("changelog", []any{}))
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	result, err := client.WorkActivity(context.Background(), defaultActivityOptions())
	if !errors.Is(err, ErrIncompleteSearch) || result.Complete || len(result.Matches) != 1 || result.Matches[0].Events[0].ID != "c1" {
		t.Fatalf("result=%+v err=%v; want retained comment and partial status", result, err)
	}
}

func TestWorkActivityMalformedAuthorAndPaginationAreIncomplete(t *testing.T) {
	tests := []struct {
		name string
		page activityHTTPResponse
	}{
		{
			name: "missing author account ID",
			page: activityPageResponse("comment", []any{map[string]any{"id": "c1", "created": "2026-10-13T12:00:00Z", "author": map[string]any{}}}),
		},
		{
			name: "malformed pagination metadata",
			page: activityHTTPResponse{body: map[string]any{"startAt": 0, "total": 1, "isLast": true, "comments": []any{activityCommentRecord("c1", "2026-10-13T12:00:00Z", "actor-1")}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z"))
			fixture.setEvents("TEST-1", tc.page, activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
			client, closeServer := newActivityWorkflowClient(t, fixture)
			defer closeServer()
			result, err := client.WorkActivity(context.Background(), defaultActivityOptions())
			if !errors.Is(err, ErrIncompleteSearch) || result.Complete || len(result.Problems) == 0 {
				t.Fatalf("result=%+v err=%v; malformed activity must be partial", result, err)
			}
		})
	}
}

func TestWorkActivityResolvesOverridesByAccountIDOrExactEmail(t *testing.T) {
	tests := []struct {
		name     string
		override string
	}{
		{name: "account ID", override: "target-account"},
		{name: "exact email", override: "target@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "someone", "2026-12-01T00:00:00Z"))
			fixture.setEvents("TEST-1", activityPageResponse("comment", []any{
				activityCommentRecord("target", "2026-10-13T12:00:00Z", "target-account"),
				activityCommentRecord("display-only", "2026-10-14T12:00:00Z", "display-only-account"),
			}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
			fixture.userByAccount = activityHTTPResponse{body: map[string]any{"accountId": "target-account", "displayName": "Target User"}}
			fixture.userSearch = activityHTTPResponse{body: []any{
				map[string]any{"accountId": "target-account", "emailAddress": "target@example.com", "displayName": "Target User"},
				map[string]any{"accountId": "display-only-account", "emailAddress": "other@example.com", "displayName": "target@example.com"},
			}}
			client, closeServer := newActivityWorkflowClient(t, fixture)
			defer closeServer()
			client.userOverride = tc.override
			result, err := client.WorkActivity(context.Background(), defaultActivityOptions())
			if err != nil {
				t.Fatal(err)
			}
			if result.AccountID != "target-account" || len(result.Matches) != 1 || len(result.Matches[0].Events) != 1 || result.Matches[0].Events[0].ID != "target" {
				t.Fatalf("resolved result = %+v", result)
			}
		})
	}
}

func TestWorkActivityRejectsDisplayNameAndAmbiguousEmailBeforeSearch(t *testing.T) {
	tests := []struct {
		name     string
		override string
		byID     activityHTTPResponse
		search   activityHTTPResponse
	}{
		{
			name:     "display name is not an identity",
			override: "Alice",
			byID:     activityHTTPResponse{body: map[string]any{"accountId": "actual-id", "displayName": "Alice"}},
		},
		{
			name:     "ambiguous exact email",
			override: "same@example.com",
			search: activityHTTPResponse{body: []any{
				map[string]any{"accountId": "id-one", "emailAddress": "same@example.com"},
				map[string]any{"accountId": "id-two", "emailAddress": "same@example.com"},
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := baseActivityFixture()
			fixture.userByAccount, fixture.userSearch = tc.byID, tc.search
			client, closeServer := newActivityWorkflowClient(t, fixture)
			defer closeServer()
			client.userOverride = tc.override
			if _, err := client.WorkActivity(context.Background(), defaultActivityOptions()); err == nil {
				t.Fatal("expected identity resolution failure")
			}
			if fixture.searchCount() != 0 {
				t.Fatalf("candidate search ran %d times before identity resolution", fixture.searchCount())
			}
		})
	}
}

func TestWorkActivityPreconditionsAvoidCandidateSearch(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*Client, *activityAPIFixture, *WorkActivityOptions) context.Context
		wantReq int
	}{
		{name: "invalid date", prepare: func(_ *Client, _ *activityAPIFixture, o *WorkActivityOptions) context.Context {
			o.StartDate = "2026-02-30"
			return context.Background()
		}},
		{name: "invalid budget", prepare: func(_ *Client, _ *activityAPIFixture, o *WorkActivityOptions) context.Context {
			o.MaxIssues = 10001
			return context.Background()
		}},
		{name: "canceled context", prepare: func(_ *Client, _ *activityAPIFixture, _ *WorkActivityOptions) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{name: "profile without account ID", prepare: func(_ *Client, f *activityAPIFixture, _ *WorkActivityOptions) context.Context {
			f.profile = activityHTTPResponse{body: map[string]any{"emailAddress": "user@example.com", "timeZone": "UTC"}}
			return context.Background()
		}, wantReq: 1},
		{name: "invalid profile timezone", prepare: func(_ *Client, f *activityAPIFixture, o *WorkActivityOptions) context.Context {
			f.profile = activityProfile("actor-1", "user@example.com", "Not/A_Zone")
			o.TimeZone = ""
			return context.Background()
		}, wantReq: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := baseActivityFixture()
			client, closeServer := newActivityWorkflowClient(t, fixture)
			defer closeServer()
			opts := defaultActivityOptions()
			ctx := tc.prepare(client, fixture, &opts)
			_, err := client.WorkActivity(ctx, opts)
			if err == nil {
				t.Fatal("expected precondition failure")
			}
			if fixture.searchCount() != 0 {
				t.Fatalf("candidate search ran %d times", fixture.searchCount())
			}
			fixture.mu.Lock()
			requests := len(fixture.paths)
			fixture.mu.Unlock()
			if requests != tc.wantReq {
				t.Fatalf("remote request count = %d, want %d", requests, tc.wantReq)
			}
		})
	}
}

func TestWorkActivityAssumesUTCWhenProfileZoneMissing(t *testing.T) {
	fixture := baseActivityFixture()
	fixture.profile = activityProfile("actor-1", "authenticated@example.com", "")
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.TimeZone = ""
	result, err := client.WorkActivity(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.TimeZone != "UTC" || !result.TimeZoneAssumed {
		t.Fatalf("timezone = %q assumed=%v; want assumed UTC", result.TimeZone, result.TimeZoneAssumed)
	}
}

func TestActivityBoundsFollowDSTCalendarDays(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		start, end string
		want       time.Duration
	}{
		{start: "2026-03-08", end: "2026-03-08", want: 23 * time.Hour},
		{start: "2026-11-01", end: "2026-11-01", want: 25 * time.Hour},
	}
	for _, tc := range tests {
		start, until, err := activityBounds(tc.start, tc.end, loc)
		if err != nil {
			t.Fatal(err)
		}
		if got := until.Sub(start); got != tc.want {
			t.Errorf("calendar day %s duration = %s, want %s", tc.start, got, tc.want)
		}
	}
}

func TestWorkActivitySearchRequestIsProjectScoped(t *testing.T) {
	fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "actor-1", "2026-10-13T00:00:00Z"))
	fixture.setEvents("TEST-1", activityPageResponse("comment", []any{}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	if _, err := client.WorkActivity(context.Background(), defaultActivityOptions()); err != nil {
		t.Fatal(err)
	}
	if got, want := fixture.searchJQL(), "project = TEST ORDER BY key ASC"; got != want {
		t.Fatalf("JQL = %q, want project-only query %q", got, want)
	}
	if fixture.searchCount() != 1 {
		t.Fatal("candidate search was not executed")
	}
}

func TestWorkActivityGlobalEventBudgetIsSharedAcrossIssues(t *testing.T) {
	fixture := baseActivityFixture(
		activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z"),
		activityIssueRow("TEST-2", "TEST", "other", "2026-12-01T00:00:00Z"),
		activityIssueRow("TEST-3", "TEST", "other", "2026-12-01T00:00:00Z"),
	)
	for _, key := range []string{"TEST-1", "TEST-2", "TEST-3"} {
		fixture.setEvents(key,
			activityPageResponse("comment", []any{
				activityCommentRecord(key+"-1", "2026-10-13T10:00:00Z", "actor-1"),
				activityCommentRecord(key+"-2", "2026-10-13T11:00:00Z", "actor-1"),
				activityCommentRecord(key+"-3", "2026-10-13T12:00:00Z", "actor-1"),
			}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
	}
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.MaxEvents = 2
	result, err := client.WorkActivity(context.Background(), opts)
	if !errors.Is(err, ErrIncompleteSearch) {
		t.Fatalf("error = %v, want ErrIncompleteSearch at the shared evidence budget", err)
	}
	if !result.CandidateComplete || result.Complete {
		t.Fatalf("candidate/overall completion = %v/%v, want true/false", result.CandidateComplete, result.Complete)
	}
	if got := observedActivityEventCount(result); got == 0 || got > opts.MaxEvents {
		t.Fatalf("retained %d matching events, want evidence preserved within shared cap 1..%d", got, opts.MaxEvents)
	}
	if !containsActivityProblem(result.Problems, "global matching-evidence limit") {
		t.Fatalf("problems = %v, want global evidence budget diagnosis", result.Problems)
	}
}

func TestWorkActivityExactEventBudgetRemainsComplete(t *testing.T) {
	fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z"))
	fixture.setEvents("TEST-1",
		activityPageResponse("comment", []any{
			activityCommentRecord("c1", "2026-10-13T10:00:00Z", "actor-1"),
			activityCommentRecord("c2", "2026-10-13T11:00:00Z", "actor-1"),
		}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.MaxEvents = 2
	result, err := client.WorkActivity(context.Background(), opts)
	if err != nil || !result.Complete || !result.CandidateComplete {
		t.Fatalf("result completion = %v/%v, error = %v; exact budget use should complete", result.CandidateComplete, result.Complete, err)
	}
	if got := observedActivityEventCount(result); got != opts.MaxEvents {
		t.Fatalf("retained %d events, want exactly %d", got, opts.MaxEvents)
	}
}

func TestWorkActivityNonmatchingEventsDoNotConsumeEventBudget(t *testing.T) {
	fixture := baseActivityFixture(activityIssueRow("TEST-1", "TEST", "other", "2026-12-01T00:00:00Z"))
	fixture.setEvents("TEST-1",
		activityPageResponse("comment", []any{
			activityCommentRecord("other-author", "2026-10-13T10:00:00Z", "someone-else"),
			activityCommentRecord("before-window", "2026-10-11T23:59:59Z", "actor-1"),
			activityCommentRecord("after-window", "2026-10-17T00:00:00Z", "actor-1"),
			activityCommentRecord("in-window", "2026-10-13T11:00:00Z", "actor-1"),
		}), activityPageResponse("worklog", []any{}), activityPageResponse("changelog", []any{}))
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.MaxEvents = 1
	result, err := client.WorkActivity(context.Background(), opts)
	if err != nil || !result.Complete {
		t.Fatalf("nonmatching records exhausted budget: complete=%v error=%v problems=%v", result.Complete, err, result.Problems)
	}
	if got := observedActivityEventCount(result); got != 1 || result.Matches[0].Events[0].ID != "in-window" {
		t.Fatalf("retained events = %+v, want only the in-window actor event", result.Matches)
	}
}

func TestWorkActivityRejectsNegativeEventBudgetBeforeRequests(t *testing.T) {
	fixture := baseActivityFixture()
	client, closeServer := newActivityWorkflowClient(t, fixture)
	defer closeServer()
	opts := defaultActivityOptions()
	opts.MaxEvents = -1
	if _, err := client.WorkActivity(context.Background(), opts); err == nil {
		t.Fatal("negative evidence budget should be rejected")
	}
	fixture.mu.Lock()
	requests := len(fixture.paths)
	fixture.mu.Unlock()
	if requests != 0 {
		t.Fatalf("invalid budget made %d requests, want no requests", requests)
	}
}

func observedActivityEventCount(result ActivityResult) int {
	total := 0
	for _, match := range result.Matches {
		total += len(match.Events)
	}
	return total
}

func containsActivityProblem(problems []string, part string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, part) {
			return true
		}
	}
	return false
}
