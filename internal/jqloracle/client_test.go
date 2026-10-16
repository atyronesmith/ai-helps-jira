//go:build oracle

package jqloracle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// These tests exercise the oracle protocol locally. CI can run this package
// with the oracle tag without supplying credentials or contacting Jira.
func TestParseStrictPreservesErrorsAndStructure(t *testing.T) {
	queries := []string{"project = PROJ", "project = PROJ AND imaginary = nope"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/jql/parse" || r.URL.Query().Get("validation") != "strict" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if user, token, ok := r.BasicAuth(); !ok || user != "test@example.invalid" || token != "test-token" {
			t.Error("missing expected test authentication")
		}
		var body struct {
			Queries []string `json:"queries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body.Queries, queries) {
			t.Errorf("unexpected request queries: %v (decode error: %v)", body.Queries, err)
		}
		w.Header().Set("Content-Type", "application/json")
		// Jira uses HTTP 200 for both valid and invalid JQL. The client must
		// preserve per-query errors and the parsed AST for callers to inspect.
		_, _ = w.Write([]byte(`{"queries":[
			{"query":"project = PROJ","errors":[],"structure":{"where":{"field":{"name":"project"},"operator":"="},"orderBy":{"fields":[{"field":{"name":"updated"},"direction":"desc"}]}}},
			{"query":"project = PROJ AND imaginary = nope","errors":["Unknown field"],"structure":null}
		]}`))
	}))
	defer server.Close()
	c := &Client{http: server.Client(), base: server.URL, email: "test@example.invalid", token: "test-token"}
	got, err := c.ParseStrict(queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Errors) != 0 || got[0].Structure == nil || !got[0].Structure.Where.HasTopLevelFieldPredicate("project") {
		t.Fatalf("valid query lost parsed shape: %+v", got[0])
	}
	if got[0].Structure.OrderBy == nil || len(got[0].Structure.OrderBy.Fields) != 1 || got[0].Structure.OrderBy.Fields[0].Field.Name != "updated" {
		t.Fatalf("valid query lost ordering: %+v", got[0].Structure)
	}
	if !reflect.DeepEqual(got[1].Errors, []string{"Unknown field"}) || got[1].Structure != nil {
		t.Fatalf("HTTP 200 concealed invalid JQL: %+v", got[1])
	}
}

func TestParseStrictRejectsUnusableResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"authentication failure", http.StatusUnauthorized, `{}`, "unexpected status 401"},
		{"invalid JSON", http.StatusOK, `{`, "decode response"},
		{"missing results", http.StatusOK, `{"queries":[]}`, "expected 1 results, got 0"},
		{"extra results", http.StatusOK, `{"queries":[{},{}]}`, "expected 1 results, got 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := &Client{http: server.Client(), base: server.URL}
			if _, err := c.ParseStrict([]string{"project = PROJ"}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ParseStrict error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewClientFromEnvRequiresExplicitCredentials(t *testing.T) {
	for _, missing := range []string{"JIRA_SERVER", "JIRA_EMAIL", "JIRA_API_TOKEN"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("JIRA_SERVER", "https://example.invalid")
			t.Setenv("JIRA_EMAIL", "test@example.invalid")
			t.Setenv("JIRA_API_TOKEN", "test-token")
			t.Setenv(missing, "")
			if _, err := NewClientFromEnv(); err == nil {
				t.Fatal("missing explicit credentials must not be silently loaded from local configuration")
			}
		})
	}
}
