// Package fieldmeta resolves Jira field vocabulary against a cached catalog.
package fieldmeta

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

var (
	ErrMissingMetadata   = errors.New("field metadata is missing")
	ErrAmbiguousField    = errors.New("field name is ambiguous")
	ErrInapplicableField = errors.New("field is not known to apply to this scope")
)

type Field struct {
	ID                 string   `json:"id"`
	StableID           string   `json:"stableId,omitempty"`
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	ItemType           string   `json:"itemType,omitempty"`
	Searcher           string   `json:"searcher,omitempty"`
	Type               string   `json:"type,omitempty"`
	Aliases            []string `json:"aliases,omitempty"`
	Projects           []string `json:"projects,omitempty"`
	IssueTypes         []string `json:"issueTypes,omitempty"`
	ApplicabilityKnown bool     `json:"applicabilityKnown"`
}

type Resolver struct {
	fields         []Field
	aliases        map[string]string
	aliasConflicts map[string]bool
}

func New(fields []Field, aliases map[string]string) *Resolver {
	r := &Resolver{fields: append([]Field(nil), fields...), aliases: make(map[string]string, len(aliases)), aliasConflicts: map[string]bool{}}
	for i := range r.fields {
		r.fields[i].Aliases = append([]string(nil), fields[i].Aliases...)
		r.fields[i].Projects = append([]string(nil), fields[i].Projects...)
		r.fields[i].IssueTypes = append([]string(nil), fields[i].IssueTypes...)
	}
	for k, v := range aliases {
		if old, ok := r.aliases[key(k)]; ok && !strings.EqualFold(old, v) {
			r.aliasConflicts[key(k)] = true
		}
		r.aliases[key(k)] = v
	}
	return r
}

func key(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func (r *Resolver) Resolve(name, project, issueType string) (Field, error) {
	q := strings.TrimSpace(name)
	if f, ok := Core(q); ok {
		return f, nil
	}
	if strings.HasPrefix(strings.ToLower(q), "cf[") && strings.HasSuffix(q, "]") {
		q = "customfield_" + q[3:len(q)-1]
	}
	if r == nil || len(q) > 256 || q == "" {
		return Field{}, fmt.Errorf("%w: invalid or uncached field name", ErrMissingMetadata)
	}
	var exact []Field
	for _, f := range r.fields {
		if strings.EqualFold(f.ID, q) || (f.StableID != "" && strings.EqualFold(f.StableID, q)) {
			exact = append(exact, f)
		}
	}
	if len(exact) > 1 {
		return Field{}, fmt.Errorf("%w: %q", ErrAmbiguousField, q)
	}
	if len(exact) == 1 {
		return r.scoped(exact[0], project, issueType)
	}
	if r.aliasConflicts[key(q)] {
		return Field{}, fmt.Errorf("%w: conflicting aliases for %q", ErrAmbiguousField, q)
	}
	if target, ok := r.aliases[key(q)]; ok {
		if f, found := r.byID(target); found {
			return r.scoped(f, project, issueType)
		}
		return Field{}, fmt.Errorf("%w: alias %q targets uncached field %q; run jira-cli metadata refresh", ErrMissingMetadata, q, target)
	}
	for _, f := range r.fields {
		if strings.EqualFold(f.Name, q) {
			exact = append(exact, f)
		}
	}
	if len(exact) > 1 {
		return Field{}, fmt.Errorf("%w: %q", ErrAmbiguousField, q)
	}
	if len(exact) == 1 {
		return r.scoped(exact[0], project, issueType)
	}
	// A missing explicit field ID is never repaired into a different field.
	if strings.HasPrefix(strings.ToLower(q), "customfield_") {
		return Field{}, fmt.Errorf("%w: %q; run jira-cli metadata refresh", ErrMissingMetadata, q)
	}
	for _, f := range r.fields {
		for _, a := range f.Aliases {
			if strings.EqualFold(a, q) {
				exact = append(exact, f)
				break
			}
		}
	}
	if len(exact) > 1 {
		return Field{}, fmt.Errorf("%w: %q", ErrAmbiguousField, q)
	}
	if len(exact) == 1 {
		return r.scoped(exact[0], project, issueType)
	}
	best, second, score := Field{}, Field{}, int(^uint(0)>>1)
	for _, f := range r.fields {
		if !applicable(f, project, issueType) || ((project != "" || issueType != "") && !f.ApplicabilityKnown) {
			continue
		}
		d := distance(key(q), key(f.Name))
		if d < score {
			second = best
			best = f
			score = d
		} else if second.ID == "" || d < distance(key(q), key(second.Name)) {
			second = f
		}
	}
	limit := min(2, len([]rune(q))/4)
	if limit < 1 {
		limit = 1
	}
	if best.ID != "" && score <= limit {
		if second.ID != "" && distance(key(q), key(second.Name)) <= score+1 {
			return Field{}, fmt.Errorf("%w: %q", ErrAmbiguousField, q)
		}
		return r.scoped(best, project, issueType)
	}
	return Field{}, fmt.Errorf("%w: %q; run jira-cli metadata refresh for this site/account or configure JIRA_FIELD_ALIASES", ErrMissingMetadata, q)
}

func (r *Resolver) byID(id string) (Field, bool) {
	if f, ok := Core(id); ok {
		return f, true
	}
	for _, f := range r.fields {
		if strings.EqualFold(f.ID, id) {
			return f, true
		}
	}
	return Field{}, false
}
func (r *Resolver) scoped(f Field, p, t string) (Field, error) {
	if f.ApplicabilityKnown {
		if p != "" && !contains(f.Projects, p) {
			return Field{}, fmt.Errorf("%w: %s for project %s", ErrInapplicableField, f.Name, p)
		}
		if t != "" && !contains(f.IssueTypes, t) {
			return Field{}, fmt.Errorf("%w: %s for issue type %s", ErrInapplicableField, f.Name, t)
		}
	}
	return f, nil
}
func applicable(f Field, p, t string) bool {
	if !f.ApplicabilityKnown {
		return true
	}
	return (p == "" || contains(f.Projects, p)) && (t == "" || contains(f.IssueTypes, t))
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
func (r *Resolver) Core(name string) (Field, bool) {
	return Core(name)
}

// Core returns a credential-free built-in Jira field, even when no catalog exists.
func Core(name string) (Field, bool) {
	switch strings.ToLower(name) {
	case "due":
		name = "duedate"
	case "resolved":
		name = "resolutiondate"
	case "type":
		name = "issuetype"
	case "component":
		name = "components"
	case "timeoriginalestimate":
		name = "originalEstimate"
	case "issuekey":
		name = "key"
	}
	for _, f := range coreFields {
		if strings.EqualFold(f.Name, name) || strings.EqualFold(f.ID, name) {
			return f, true
		}
	}
	return Field{}, false
}
func JQLName(f Field) string {
	if strings.HasPrefix(strings.ToLower(f.ID), "customfield_") {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(f.ID), "customfield_"))
		if err == nil {
			return fmt.Sprintf("cf[%d]", n)
		}
	}
	return jql.QuoteIdent(f.ID)
}

var coreFields = []Field{
	{ID: "summary", Name: "Summary", Type: "string"},
	{ID: "description", Name: "Description", Type: "string"},
	{ID: "text", Name: "Text", Type: "string"},
	{ID: "status", Name: "Status", Type: "option"},
	{ID: "statusCategory", Name: "Status Category", Type: "option"},
	{ID: "project", Name: "Project", Type: "project"},
	{ID: "assignee", Name: "Assignee", Type: "user"},
	{ID: "reporter", Name: "Reporter", Type: "user"},
	{ID: "priority", Name: "Priority", Type: "option"},
	{ID: "issuetype", Name: "Issue Type", Type: "option"},
	{ID: "created", Name: "Created", Type: "date"},
	{ID: "updated", Name: "Updated", Type: "date"},
	{ID: "resolution", Name: "Resolution", Type: "option"},
	{ID: "labels", Name: "Labels", Type: "array"},
	{ID: "key", Name: "Key", Type: "string"},
	{ID: "fixVersion", Name: "Fix Version", Type: "array"},
	{ID: "components", Name: "Component", Type: "array"},
	{ID: "duedate", Name: "Due Date", Type: "date"},
	{ID: "votes", Name: "Votes", Type: "number"},
	{ID: "watches", Name: "Watchers", Type: "number"},
	{ID: "originalEstimate", Name: "Original Estimate", Type: "duration"},
	{ID: "remainingEstimate", Name: "Remaining Estimate", Type: "duration"},
	{ID: "timespent", Name: "Time Spent", Type: "duration"},
	{ID: "resolutiondate", Name: "Resolution Date", Type: "date"},
	{ID: "parent", Name: "Parent", Type: "string"},
	{ID: "id", Name: "ID", Type: "number"},
	{ID: "creator", Name: "Creator", Type: "user"},
}

func distance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, x := range ar {
		cur := make([]int, len(br)+1)
		cur[0] = i + 1
		for j, y := range br {
			cost := 1
			if x == y {
				cost = 0
			}
			cur[j+1] = min(cur[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}
