package nl2jql

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

func requestOwner(self bool, name string) *ModelIntent {
	value := &ModelValue{Kind: "self"}
	if !self {
		value = &ModelValue{Kind: "string", Text: name}
	}
	return &ModelIntent{Kind: IntentOwner, Value: value}
}
func requestQueue() *ModelIntent {
	return &ModelIntent{Kind: IntentComparison, Field: "labels", Operator: "=", Value: &ModelValue{Kind: "string", Text: "triage"}}
}
func requestAll(children ...*ModelIntent) *ModelIntent {
	return &ModelIntent{Kind: IntentAll, Children: children}
}
func runRequestFallback(t *testing.T, query, self string, filter *ModelIntent, sorts []ModelSort) (string, error) {
	t.Helper()
	raw, err := json.Marshal(ModelPlan{Version: 1, Outcome: "supported", Mode: ModeList, Filter: filter, Sort: sorts})
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{jql: string(raw)}
	engine, err := NewFallbackEngine(provider)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.generate(context.Background(), query, nil, fieldmeta.New(nil, nil), "PROJ", self)
	if atomic.LoadInt32(&provider.calls) != 1 {
		t.Fatal("expected exactly one fallback call")
	}
	return result, err
}

func TestFallbackRequestRequiresOwnerInEveryBranch(t *testing.T) {
	for _, tc := range []struct {
		name, self string
		filter     *ModelIntent
		wantErr    bool
	}{
		{name: "missing", filter: requestQueue(), wantErr: true},
		{name: "wrong owner", filter: requestAll(requestOwner(false, "other"), requestQueue()), wantErr: true},
		{name: "negated owner", filter: requestAll(&ModelIntent{Kind: IntentNot, Children: []*ModelIntent{requestOwner(true, "")}}, requestQueue()), wantErr: true},
		{name: "leaking alternative", filter: &ModelIntent{Kind: IntentAny, Children: []*ModelIntent{requestOwner(true, ""), requestQueue()}}, wantErr: true},
		{name: "history actor is not assignee", filter: &ModelIntent{Kind: IntentHistory, Status: "Closed", Actor: &ModelValue{Kind: "self"}, Start: "2026-10-10", End: "2026-10-17"}, wantErr: true},
		{name: "function-looking owner literal", filter: requestAll(requestOwner(false, "currentUser()"), requestQueue()), wantErr: true},
		{name: "positive global owner", filter: requestAll(requestOwner(true, ""), requestQueue())},
		{name: "each alternative owned", filter: &ModelIntent{Kind: IntentAny, Children: []*ModelIntent{requestAll(requestOwner(true, ""), requestQueue()), requestAll(requestOwner(true, ""), &ModelIntent{Kind: IntentComparison, Field: "priority", Operator: "=", Value: &ModelValue{Kind: "string", Text: "Critical"}})}}},
		{name: "configured self", self: `"reviewer@example.com"`, filter: requestAll(requestOwner(true, ""), requestQueue())},
		{name: "configured equivalent literal", self: `"reviewer@example.com"`, filter: requestAll(requestOwner(false, "reviewer@example.com"), requestQueue())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRequestFallback(t, "list all my triage queue please", tc.self, tc.filter, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("JQL=%q err=%v", got, err)
			}
			if tc.wantErr && (got != "" || translationerror.KindOf(err) != translationerror.InvalidOutput) {
				t.Fatalf("unsafe fallback result=%q err=%v", got, err)
			}
			if !tc.wantErr && !strings.Contains(got, "assignee") {
				t.Fatalf("missing owner: %s", got)
			}
		})
	}
}

func TestFallbackRequestPreservesRecognizedOrdering(t *testing.T) {
	filter := requestAll(requestOwner(true, ""), requestQueue())
	for _, tc := range []struct {
		name, query string
		sorts       []ModelSort
		wantErr     bool
	}{
		{name: "priority default descending", query: "my triage queue please and order by priority", sorts: []ModelSort{{Field: "priority", Direction: "DESC"}}},
		{name: "changed implicit direction", query: "my triage queue please and order by priority", sorts: []ModelSort{{Field: "priority", Direction: "ASC"}}, wantErr: true},
		{name: "dropped ordering", query: "my triage queue please and order by priority", wantErr: true},
		{name: "wrong field", query: "my triage queue please ordered by priority", sorts: []ModelSort{{Field: "created", Direction: "DESC"}}, wantErr: true},
		{name: "explicit ascending", query: "my triage queue please ordered by priority ascending", sorts: []ModelSort{{Field: "priority", Direction: "ASC"}}},
		{name: "updated default descending", query: "my triage queue please ordered by last updated", sorts: []ModelSort{{Field: "updated", Direction: "DESC"}}},
		{name: "core date alias", query: "my triage queue please ordered by due date", sorts: []ModelSort{{Field: "duedate", Direction: "ASC"}}},
		{name: "multiple fields", query: "my triage queue please ordered by priority, then updated", sorts: []ModelSort{{Field: "priority", Direction: "DESC"}, {Field: "updated", Direction: "DESC"}}},
		{name: "reordered fields", query: "my triage queue please ordered by priority, then updated", sorts: []ModelSort{{Field: "updated", Direction: "DESC"}, {Field: "priority", Direction: "DESC"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runRequestFallback(t, tc.query, "", filter, tc.sorts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("JQL=%q err=%v", got, err)
			}
			if tc.wantErr && (got != "" || translationerror.KindOf(err) != translationerror.InvalidOutput) {
				t.Fatalf("JQL=%q err=%v", got, err)
			}
		})
	}
}

func TestFallbackRequestIgnoresLiteralOwnershipAndOrderingWords(t *testing.T) {
	for _, query := range []string{
		`search for "my triage queue ordered by priority"`,
		`list issues in "my project"`,
		`items assigned to my@example.com`,
		`find items that contain the words my, ordered or priority`,
	} {
		if got, err := runRequestFallback(t, query, "", requestQueue(), nil); err != nil || got == "" {
			t.Fatalf("query=%q JQL=%q err=%v", query, got, err)
		}
	}
}
