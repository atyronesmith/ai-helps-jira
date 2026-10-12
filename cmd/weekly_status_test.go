package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A 400 from JIRA's search endpoint (a malformed query) must surface as an
// error from runWeeklyStatus rather than being treated as "no issues found",
// and Jira's own diagnostic text must survive into it along with the
// offending JQL (FIELDCTO-2814). This pins the CLI path; the MCP path is
// covered in internal/mcpserver.
func TestRunWeeklyStatus_SearchErrorSurfacesJiraMessageAndJQL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errorMessages": []string{"Error in the JQL Query: Expecting ',' but got 'AND'. (line 1, character 39)"},
		})
	}))
	t.Cleanup(ts.Close)

	t.Setenv("JIRA_SERVER", ts.URL)
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	// Reset package-level flags that other tests/commands might have mutated.
	flagUser = ""
	flagProject = ""
	flagWSStartDate = "2026-01-01"
	flagWSEndDate = "2026-01-07"
	flagWSConfluence = false

	err := runWeeklyStatus(weeklyStatusCmd, nil)
	if err == nil {
		t.Fatal("expected an error for a 400 search response, got nil")
	}
	if !strings.Contains(err.Error(), "Expecting ',' but got 'AND'") {
		t.Errorf("Jira's errorMessages text did not survive into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "project = TEST") {
		t.Errorf("the offending JQL did not survive into the error: %v", err)
	}
}
