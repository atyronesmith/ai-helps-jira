package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTransitionSucceedsWhenPostMutationCacheCannotOpen(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	transitioned := false
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/issue/TEST-1/transitions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode transition request: %v", err)
		}
		transitioned = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer jiraServer.Close()
	t.Setenv("JIRA_SERVER", jiraServer.URL)
	t.Setenv("JIRA_EMAIL", "transition-test@example.invalid")
	t.Setenv("JIRA_API_TOKEN", "not-a-real-token")
	t.Setenv("JIRA_PROJECT", "TEST")
	oldUser, oldProject := flagUser, flagProject
	t.Cleanup(func() { flagUser, flagProject = oldUser, oldProject })
	flagUser, flagProject = "", ""

	if err := runTransition(nil, []string{"TEST-1", "31"}); err != nil {
		t.Fatalf("successful Jira transition surfaced cache failure: %v", err)
	}
	if !transitioned {
		t.Fatal("Jira transition endpoint was not called")
	}
}
