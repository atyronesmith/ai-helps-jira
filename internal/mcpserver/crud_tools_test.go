package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// TestHandleGetIssue_RefetchQuotesHostileKey is the FIELDCTO-2812 negative
// test for the post-mutation refetch in HandleGetIssue
// (client.SearchJQL(fmt.Sprintf("key = %s", ...), 1)): issue_key arrives as
// an MCP tool argument, so a caller can supply anything, including a value
// shaped to break out of the `key = ...` clause and widen the query with a
// top-level OR. The refetch must quote it so the clause still only ever
// matches by identity.
func TestHandleGetIssue_RefetchQuotesHostileKey(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	var recordedJQL string
	var sawSearch bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql" {
			sawSearch = true
			var body struct {
				JQL string `json:"jql"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			recordedJQL = body.JQL
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{}, "total": 0})
			return
		}
		// Everything else (the issue-detail GET) is irrelevant to this test:
		// the refetch's JQL is recorded before this point, and the handler
		// is allowed to error out afterward for an issue that does not
		// really exist.
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)

	hostileKey := `" OR project = OTHER`
	req := makeRequest(map[string]any{"issue_key": hostileKey})
	_, err := h.HandleGetIssue(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleGetIssue returned transport err: %v", err)
	}
	if !sawSearch {
		t.Fatal("refetch never reached the search endpoint")
	}

	// The default project for this test env is "TEST" (setTestJIRAEnv).
	if err := jql.Validate(recordedJQL, jql.Options{DefaultProject: "TEST"}); err != nil {
		t.Errorf("expected quoted refetch JQL to validate, got: %v\njql: %s", err, recordedJQL)
	}
	blanked := jql.BlankQuotedLiterals(recordedJQL)
	if jql.TopLevelKeywordIndex(blanked, "OR") >= 0 {
		t.Errorf("hostile OR escaped quoting into a top-level keyword: %s", recordedJQL)
	}
	if strings.Contains(blanked, "OTHER") {
		t.Errorf("hostile project reference leaked outside the quoted literal: %s", recordedJQL)
	}
}
