package mcpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestTransitionThenGetIssueCannotReturnPreTransitionStatus(t *testing.T) {
	const issueKey = "TEST-1"

	status := "In Progress"
	updated := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/"+issueKey+"/transitions":
			status = "Closed"
			updated = updated.Add(time.Minute)
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql":
			fmt.Fprintf(w, `{"issues":[{"key":%q,"fields":{"summary":"Cached issue","status":{"name":%q},"priority":{"name":"Major"},"updated":%q}}],"total":1}`,
				issueKey, status, updated.Format("2006-01-02T15:04:05.000-0700"))

		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/"+issueKey:
			fmt.Fprintf(w, `{"key":%q,"fields":{"summary":"Cached issue","status":{"name":%q},"priority":{"name":"Major"},"issuetype":{"name":"Story"},"labels":[],"issuelinks":[],"subtasks":[]}}`,
				issueKey, status)

		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	t.Cleanup(jiraServer.Close)

	t.Setenv("JIRA_SERVER", jiraServer.URL)
	t.Setenv("JIRA_EMAIL", "test@example.com")
	t.Setenv("JIRA_API_TOKEN", "test-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	h := newTestHandlers(t)
	cfg, err := loadJIRAConfig(makeRequest(map[string]any{}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := h.cacheForConfig(cfg)
	if err != nil {
		t.Fatalf("scope cache: %v", err)
	}
	otherCfg := *cfg
	otherCfg.JiraEmail = "other@example.com"
	otherDB, err := h.cacheForConfig(&otherCfg)
	if err != nil {
		t.Fatalf("scope other account cache: %v", err)
	}
	if err := db.UpsertIssueDetail(&jira.IssueDetail{
		Key:       issueKey,
		Summary:   "Cached issue",
		Status:    "In Progress",
		Priority:  "Major",
		IssueType: "Story",
		Updated:   updated,
	}); err != nil {
		t.Fatalf("seed stale issue detail: %v", err)
	}
	if err := otherDB.UpsertIssueDetail(&jira.IssueDetail{Key: issueKey, Summary: "Other account issue", Status: "Open", Updated: updated}); err != nil {
		t.Fatalf("seed other account issue detail: %v", err)
	}

	transitionResult, err := h.HandleTransition(t.Context(), makeRequest(map[string]any{
		"issue_key":     issueKey,
		"transition_id": "81",
	}))
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if transitionResult.IsError {
		t.Fatalf("transition returned tool error: %s", resultText(transitionResult))
	}
	if stale, ok := db.GetIssueDetail(issueKey, time.Time{}); ok {
		t.Fatalf("transition left stale detail in cache: status=%q", stale.Status)
	}
	if other, ok := otherDB.GetIssueDetail(issueKey, time.Time{}); !ok || other.Summary != "Other account issue" {
		t.Fatalf("transition crossed account cache scope: detail=%+v cached=%t", other, ok)
	}

	getResult, err := h.HandleGetIssue(t.Context(), makeRequest(map[string]any{
		"issue_key":        issueKey,
		"include_comments": false,
		"include_links":    false,
	}))
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if getResult.IsError {
		t.Fatalf("get issue returned tool error: %s", resultText(getResult))
	}
	got := resultText(getResult)
	if !strings.Contains(got, "**Status:** Closed") {
		t.Fatalf("get issue did not return post-transition status:\n%s", got)
	}
	if strings.Contains(got, "**Status:** In Progress") {
		t.Fatalf("get issue returned pre-transition status:\n%s", got)
	}
}
