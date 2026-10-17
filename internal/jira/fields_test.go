package jira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListFields(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/field" || r.Method != "GET" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"customfield_42","stableId":"sf_42","name":"Cost Center","clauseNames":["cf[42]","costCenter"],"searcherKey":"text-searcher", "schema":{"type":"array","items":"string"}}]`))
	}))
	defer s.Close()
	c := &Client{http: s.Client(), base: s.URL, email: "x", token: "y"}
	fields, err := c.ListFields(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields[0].ID != "customfield_42" || fields[0].StableID != "sf_42" || fields[0].Type != "array" || fields[0].ItemType != "string" || fields[0].Searcher != "text-searcher" || fields[0].ApplicabilityKnown || len(fields[0].Aliases) != 2 {
		t.Fatalf("fields=%+v", fields)
	}
}

func TestListProjectFieldIDsPaginatesAndValidatesScope(t *testing.T) {
	var starts []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/projects/fields" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("projectId") != "10001" || r.URL.Query().Get("workTypeId") != "10002" || r.URL.Query().Get("maxResults") != "100" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		start := r.URL.Query().Get("startAt")
		starts = append(starts, start)
		w.Header().Set("Content-Type", "application/json")
		if start == "0" {
			_, _ = w.Write([]byte(`{"isLast":false,"startAt":0,"maxResults":100,"total":3,"values":[{"fieldId":"summary","projectId":10001,"workTypeId":10002},{"fieldId":"customfield_1","projectId":"10001","workTypeId":"10002"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"isLast":true,"startAt":2,"maxResults":100,"total":3,"values":[{"fieldId":"customfield_2","projectId":10001,"workTypeId":10002}]}`))
	}))
	defer s.Close()
	c := &Client{http: s.Client(), base: s.URL}
	ids, err := c.ListProjectFieldIDs(context.Background(), "10001", "10002")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "summary" || ids[2] != "customfield_2" || len(starts) != 2 || starts[1] != "2" {
		t.Fatalf("ids=%v starts=%v", ids, starts)
	}
}

func TestListProjectFieldIDsRejectsIncompleteOrInvalidPages(t *testing.T) {
	for _, body := range []string{
		`{"startAt":0,"values":[]}`,
		`{"isLast":false,"startAt":0,"values":[]}`,
		`{"isLast":true,"startAt":0,"values":[{"fieldId":"x","projectId":9,"workTypeId":2}]}`,
		`{"isLast":true,"startAt":0,"values":[{"fieldId":"x","projectId":1,"workTypeId":2},{"fieldId":"x","projectId":1,"workTypeId":2}]}`,
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		c := &Client{http: s.Client(), base: s.URL}
		_, err := c.ListProjectFieldIDs(context.Background(), "1", "2")
		s.Close()
		if err == nil {
			t.Errorf("accepted invalid response %s", body)
		}
	}
}

func TestListProjectFieldIDsAcceptsEmptyLastPageAndRejectsInvalidIDs(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"isLast":true,"startAt":0,"values":[]}`))
	}))
	c := &Client{http: s.Client(), base: s.URL}
	ids, err := c.ListProjectFieldIDs(context.Background(), "1", "2")
	s.Close()
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := c.ListProjectFieldIDs(context.Background(), "project-key", "2"); err == nil {
		t.Fatal("accepted nonnumeric project ID")
	}
}

func TestResolveMetadataScopeRejectsMismatchedIDs(t *testing.T) {
	for _, tc := range []struct {
		path, body string
	}{
		{"/rest/api/3/project/10", `{"id":"11","key":"ENG"}`},
		{"/rest/api/3/issuetype/20", `{"id":"21","name":"Task"}`},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == tc.path {
				_, _ = w.Write([]byte(tc.body))
				return
			}
			if r.URL.Path == "/rest/api/3/project/10" {
				_, _ = w.Write([]byte(`{"id":"10","key":"ENG"}`))
				return
			}
			t.Errorf("unexpected lookup %s", r.URL.Path)
		}))
		c := &Client{http: s.Client(), base: s.URL}
		_, _, err := c.ResolveMetadataScope(context.Background(), "10", "20")
		s.Close()
		if err == nil {
			t.Errorf("accepted mismatched identity response for %s", tc.path)
		}
	}
}

func TestListFieldsRejectsDuplicateAndMalformedIDs(t *testing.T) {
	for _, body := range []string{`[{"id":"field","name":"A"},{"id":"field","name":"B"}]`, `[{"id":"../bad","name":"A"}]`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		c := &Client{http: s.Client(), base: s.URL}
		_, err := c.ListFields(context.Background())
		s.Close()
		if err == nil {
			t.Fatalf("accepted invalid response %s", body)
		}
	}
}
