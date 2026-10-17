package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestValidateMetadataScopeRequiresAllFlagsTogether(t *testing.T) {
	for _, tc := range []struct {
		projectID, workTypeID, workTypeName string
		wantErr                             bool
	}{
		{"", "", "", false},
		{"1", "2", "Task", false},
		{"1", "", "Task", true},
		{"", "2", "", true},
	} {
		if err := validateMetadataScope(tc.projectID, tc.workTypeID, tc.workTypeName); (err != nil) != tc.wantErr {
			t.Errorf("validateMetadataScope(%q,%q,%q) err=%v", tc.projectID, tc.workTypeID, tc.workTypeName, err)
		}
	}
}

func TestLoadMetadataFieldsScopesWithoutIssueSearch(t *testing.T) {
	var requests []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/field":
			_, _ = w.Write([]byte(`[{"id":"customfield_1","name":"Cost","schema":{"type":"number"}},{"id":"customfield_2","name":"Other","schema":{"type":"string"}}]`))
		case "/rest/api/3/project/10":
			_, _ = w.Write([]byte(`{"id":"10","key":"ENG"}`))
		case "/rest/api/3/issuetype/20":
			_, _ = w.Write([]byte(`{"id":"20","name":"Task"}`))
		case "/rest/api/3/projects/fields":
			if r.URL.Query().Get("projectId") != "10" || r.URL.Query().Get("workTypeId") != "20" {
				t.Errorf("unexpected scope query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"isLast":true,"startAt":0,"values":[{"fieldId":"customfield_1","projectId":10,"workTypeId":20}]}`))
		default:
			t.Errorf("unexpected Jira request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c, err := jira.NewClient(&config.Config{JiraServer: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := loadMetadataFields(context.Background(), c, "ENG", "10", "20", "Task")
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields[0].ID != "customfield_1" || !fields[0].ApplicabilityKnown || len(fields[0].Projects) != 2 || fields[0].Projects[0] != "ENG" || fields[0].Projects[1] != "10" || len(fields[0].IssueTypes) != 2 || fields[0].IssueTypes[0] != "Task" || fields[0].IssueTypes[1] != "20" {
		t.Fatalf("scoped fields=%+v", fields)
	}
	if len(requests) != 4 || requests[0] != "/rest/api/3/field" || requests[1] != "/rest/api/3/project/10" || requests[2] != "/rest/api/3/issuetype/20" || requests[3] != "/rest/api/3/projects/fields" {
		t.Fatalf("unexpected requests (issue search should not run): %v", requests)
	}
}

func TestLoadMetadataFieldsRejectsUnverifiedScopeLabels(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/field":
			_, _ = w.Write([]byte(`[{"id":"customfield_1","name":"Cost"}]`))
		case "/rest/api/3/project/10":
			_, _ = w.Write([]byte(`{"id":"10","key":"OTHER"}`))
		case "/rest/api/3/issuetype/20":
			_, _ = w.Write([]byte(`{"id":"20","name":"Task"}`))
		default:
			t.Errorf("scope mismatch should stop before further Jira requests: %s", r.URL.Path)
		}
	}))
	defer s.Close()
	c, _ := jira.NewClient(&config.Config{JiraServer: s.URL})
	if _, err := loadMetadataFields(context.Background(), c, "ENG", "10", "20", "Task"); err == nil {
		t.Fatal("accepted a project key that did not match the numeric project ID")
	}
}

func TestLoadMetadataFieldsLeavesApplicabilityUnknownWithoutScope(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/field" {
			t.Errorf("unexpected Jira request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"customfield_1","name":"Cost"}]`))
	}))
	defer s.Close()
	c, _ := jira.NewClient(&config.Config{JiraServer: s.URL})
	fields, err := loadMetadataFields(context.Background(), c, "ENG", "", "", "")
	if err != nil || len(fields) != 1 || fields[0].ApplicabilityKnown {
		t.Fatalf("fields=%+v err=%v", fields, err)
	}
}
