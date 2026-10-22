//go:build oracle

package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

// The live smoke test has a fixed one-issue candidate cap. It validates real
// endpoint responses and strict parsed JQL without making tenant mutations or
// pretending that a bounded scan established a complete activity total.
func TestOracle_WorkActivityReadEndpoints(t *testing.T) {
	parser := oracleClient(t)
	client, err := NewClient(&config.Config{
		JiraServer: os.Getenv("JIRA_SERVER"), JiraEmail: os.Getenv("JIRA_EMAIL"),
		JiraAPIToken: os.Getenv("JIRA_API_TOKEN"), JiraProject: oracleProject(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.http = &http.Client{Timeout: client.http.Timeout, Transport: activityReadOnlyTransport{base: http.DefaultTransport}}
	r, err := client.WorkActivity(context.Background(), WorkActivityOptions{
		StartDate: "2026-10-12", EndDate: "2026-10-16", MaxIssues: 1, MaxEventPages: 20,
	})
	if err != nil && !errors.Is(err, ErrIncompleteSearch) {
		t.Fatalf("activity read smoke failed: %v", err)
	}
	if !r.CandidateExecuted || r.AccountID == "" || r.TimeZone == "" {
		t.Fatal("activity smoke did not resolve the account/timezone and execute its candidate search")
	}
	parsed := assertValid(t, parser, r.CandidateJQL)
	if parsed.Structure.Where.CountFieldClauses("project") != 1 || parsed.Structure.Where.CountFieldClauses("assignee") != 0 || parsed.Structure.Where.CountFieldClauses("updated") != 0 || parsed.Structure.Where.CountFieldClauses("status") != 0 {
		t.Fatal("activity candidates must use project scope alone")
	}
	if parsed.Structure.OrderBy == nil || len(parsed.Structure.OrderBy.Fields) != 1 {
		t.Fatal("activity candidate order was not parsed as issue key")
	}
	order := parsed.Structure.OrderBy.Fields[0]
	if (!strings.EqualFold(order.Field.Name, "key") && !strings.EqualFold(order.Field.Name, "issuekey")) || !strings.EqualFold(order.Direction, "ASC") {
		t.Fatal("activity candidate order was not parsed as issue key ascending")
	}
	if r.Candidates != 1 {
		t.Fatal("activity endpoint smoke requires a configured project with at least one visible issue")
	}
	if r.Scanned != r.Candidates {
		t.Fatalf("activity endpoint pagination did not complete for the smoke candidate: %v", r.Problems)
	}
	if r.Complete != r.CandidateComplete || (err == nil) != r.Complete {
		t.Fatal("activity completeness disagrees with the intentional candidate bound")
	}
	for _, match := range r.Matches {
		if len(match.Events) == 0 {
			t.Fatal("activity match has no evidence")
		}
		for _, event := range match.Events {
			if event.AuthorID != r.AccountID || event.At.IsZero() {
				t.Fatal("activity match has invalid authorship or timestamp")
			}
		}
	}
	t.Log("Validated one visible issue's paginated activity endpoints; the one-issue cap does not establish a complete project activity total")
}

type activityReadOnlyTransport struct{ base http.RoundTripper }

func (tr activityReadOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && !(req.Method == http.MethodPost && req.URL.Path == "/rest/api/3/search/jql") {
		return nil, fmt.Errorf("activity smoke prohibited mutating request method %s", req.Method)
	}
	return tr.base.RoundTrip(req)
}
