package fieldmeta

import (
	"errors"
	"testing"
)

func TestResolvePrecedenceAndAmbiguity(t *testing.T) {
	fields := []Field{{ID: "customfield_12", Name: "Cost Center", Aliases: []string{"department"}}, {ID: "assignee", Name: "Assignee"}, {ID: "customfield_13", Name: "Cost Centre"}}
	r := New(fields, map[string]string{"dept": "customfield_12"})
	for _, tc := range []struct{ q, want string }{{"customfield_12", "customfield_12"}, {"dept", "customfield_12"}, {"department", "customfield_12"}, {"Cost Center", "customfield_12"}, {"assignee", "assignee"}} {
		got, err := r.Resolve(tc.q, "", "")
		if err != nil || got.ID != tc.want {
			t.Errorf("Resolve(%q)=(%+v,%v), want %s", tc.q, got, err, tc.want)
		}
	}
	if got := JQLName(fields[0]); got != "cf[12]" {
		t.Errorf("JQLName = %q", got)
	}
	amb := New([]Field{{ID: "x", Name: "Owner"}, {ID: "y", Name: "Owner"}}, nil)
	if _, err := amb.Resolve("Owner", "", ""); !errors.Is(err, ErrAmbiguousField) {
		t.Fatalf("ambiguity error=%v", err)
	}
}

func TestResolveFuzzyAndScope(t *testing.T) {
	r := New([]Field{{ID: "customfield_1", Name: "Customer Segment", ApplicabilityKnown: true, Projects: []string{"ENG"}}, {ID: "customfield_2", Name: "Component"}, {ID: "customfield_3", Name: "Restricted", ApplicabilityKnown: true, Projects: []string{"OPS"}, IssueTypes: []string{"Task"}}}, nil)
	got, err := r.Resolve("Customer Segmant", "ENG", "")
	if err != nil || got.ID != "customfield_1" {
		t.Fatalf("fuzzy=%+v err=%v", got, err)
	}
	if _, err := r.Resolve("Restricted", "ENG", ""); !errors.Is(err, ErrInapplicableField) {
		t.Fatalf("scope error=%v", err)
	}
	if _, err := r.Resolve("Restricted", "OPS", "Bug"); !errors.Is(err, ErrInapplicableField) {
		t.Fatalf("type error=%v", err)
	}
	if _, err := r.Resolve("unknown tenant field", "", ""); !errors.Is(err, ErrMissingMetadata) {
		t.Fatalf("missing error=%v", err)
	}
	if f, ok := r.Core("statusCategory"); !ok || f.ID != "statusCategory" {
		t.Fatalf("core=%+v, %v", f, ok)
	}
}
