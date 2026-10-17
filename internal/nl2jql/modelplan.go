package nl2jql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// ModelPlan is the sole external translation contract. It contains no JQL.
type ModelPlan struct {
	Version int          `json:"version"`
	Outcome string       `json:"outcome"`
	Mode    QueryMode    `json:"mode,omitempty"`
	Filter  *ModelIntent `json:"filter,omitempty"`
	Sort    []ModelSort  `json:"sort,omitempty"`
}
type ModelSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}
type ModelIntent struct {
	Kind     IntentKind     `json:"kind"`
	Field    string         `json:"field,omitempty"`
	Operator string         `json:"operator,omitempty"`
	Value    *ModelValue    `json:"value,omitempty"`
	Values   []ModelValue   `json:"values,omitempty"`
	Children []*ModelIntent `json:"children,omitempty"`
	Status   string         `json:"status,omitempty"`
	Actor    *ModelValue    `json:"actor,omitempty"`
	Start    string         `json:"start,omitempty"`
	End      string         `json:"end,omitempty"`
}
type ModelValue struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Days   int    `json:"days,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

var modelNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

func (m ModelPlan) plan(resolver *fieldmeta.Resolver, project, self string) (*QueryPlan, error) {
	if m.Version != 1 {
		return nil, invalidOutput("unsupported intent contract version")
	}
	if m.Outcome != "supported" {
		if m.Filter != nil || len(m.Sort) != 0 {
			return nil, invalidOutput("unsupported outcome carries executable intent")
		}
		switch m.Outcome {
		case "unsupported":
			return nil, translationerror.New(translationerror.Unsupported, ErrUnmappable)
		case "ambiguous":
			return nil, fmt.Errorf("%w: model requests clarification", ErrAmbiguousIntent)
		default:
			return nil, invalidOutput("invalid outcome")
		}
	}
	if m.Mode != ModeList && m.Mode != ModeCount && m.Mode != ModeRecommendation {
		return nil, invalidOutput("invalid query mode")
	}
	if m.Filter == nil {
		return nil, invalidOutput("missing filter")
	}
	// Applicability is checked in every explicitly requested project/type, never
	// merely against a configured default that the request overrides.
	projects, types := modelScopes(m.Filter)
	if len(projects) == 0 {
		projects = []string{project}
	}
	if len(types) == 0 {
		types = []string{""}
	}
	resolve := func(name string) (fieldmeta.Field, error) {
		var resolved fieldmeta.Field
		for _, p := range projects {
			for _, t := range types {
				f, err := resolver.Resolve(name, p, t)
				if err != nil {
					return f, err
				}
				if resolved.ID != "" && resolved.ID != f.ID {
					return f, fmt.Errorf("%w: field varies between scopes", fieldmeta.ErrAmbiguousField)
				}
				resolved = f
			}
		}
		return resolved, nil
	}
	budget := 128
	filter, err := compileModelIntent(m.Filter, resolve, self, 0, &budget)
	if err != nil {
		return nil, err
	}
	if _, err := statusPossibilities(filter); err != nil {
		return nil, err
	}
	for _, field := range []string{"status", "assignee"} {
		if _, err := exclusiveValues(filter, field, self); err != nil {
			return nil, err
		}
	}
	plan := &QueryPlan{Filter: filter, Self: self, Mode: m.Mode}
	if len(m.Sort) > 16 {
		return nil, invalidOutput("too many sort fields")
	}
	for _, s := range m.Sort {
		f, err := resolve(s.Field)
		if err != nil {
			return nil, err
		}
		if s.Direction != "ASC" && s.Direction != "DESC" {
			return nil, invalidOutput("invalid sort direction")
		}
		plan.Sort = append(plan.Sort, jql.Sort{Field: fieldmeta.JQLName(f), Direction: s.Direction})
	}
	return plan, nil
}
func modelScopes(i *ModelIntent) (projects, types []string) {
	if i == nil {
		return
	}
	if i.Kind == IntentComparison && (i.Operator == "=" || i.Operator == "IN") {
		var vs []ModelValue
		if i.Value != nil {
			vs = append(vs, *i.Value)
		}
		vs = append(vs, i.Values...)
		for _, v := range vs {
			if v.Kind == "string" {
				switch strings.ToLower(i.Field) {
				case "project":
					projects = append(projects, v.Text)
				case "issuetype", "type":
					types = append(types, v.Text)
				}
			}
		}
	}
	for _, c := range i.Children {
		p, t := modelScopes(c)
		projects = append(projects, p...)
		types = append(types, t...)
	}
	return
}
func compileModelIntent(i *ModelIntent, resolve func(string) (fieldmeta.Field, error), self string, depth int, budget *int) (*Intent, error) {
	*budget = *budget - 1
	if i == nil || depth > 16 || *budget < 0 {
		return nil, invalidOutput("intent complexity limit exceeded")
	}
	if i.Kind == IntentAll || i.Kind == IntentAny || i.Kind == IntentNot {
		if i.Field != "" || i.Operator != "" || i.Value != nil || len(i.Values) > 0 || i.Status != "" || i.Actor != nil || i.Start != "" || i.End != "" {
			return nil, invalidOutput("Boolean node has predicate data")
		}
		if len(i.Children) == 0 || (i.Kind == IntentNot && len(i.Children) != 1) {
			return nil, invalidOutput("invalid Boolean arity")
		}
		out := &Intent{Kind: i.Kind}
		for _, c := range i.Children {
			child, err := compileModelIntent(c, resolve, self, depth+1, budget)
			if err != nil {
				return nil, err
			}
			out.Children = append(out.Children, child)
		}
		return out, nil
	}
	if len(i.Children) != 0 {
		return nil, invalidOutput("predicate has children")
	}
	if i.Kind == IntentHistory {
		if i.Field != "" || i.Operator != "" || i.Value != nil || len(i.Values) > 0 || i.Status == "" || i.Actor == nil {
			return nil, invalidOutput("invalid history node")
		}
		if _, err := time.Parse("2006-01-02", i.Start); err != nil {
			return nil, invalidOutput("invalid history start")
		}
		if _, err := time.Parse("2006-01-02", i.End); err != nil || i.End <= i.Start {
			return nil, invalidOutput("invalid history end")
		}
		actor, err := modelOwner(i.Actor)
		if err != nil {
			return nil, err
		}
		return &Intent{Kind: IntentHistory, History: &HistoryIntent{Status: i.Status, Actor: actor, Start: i.Start, End: i.End}}, nil
	}
	if i.Status != "" || i.Actor != nil || i.Start != "" || i.End != "" {
		return nil, invalidOutput("unexpected history data")
	}
	if i.Kind == IntentOwner {
		if i.Field != "" || i.Operator != "" || len(i.Values) > 0 {
			return nil, invalidOutput("invalid owner node")
		}
		owner, err := modelOwner(i.Value)
		if err != nil {
			return nil, err
		}
		return &Intent{Kind: IntentOwner, Owner: &owner}, nil
	}
	if i.Kind != IntentComparison {
		return nil, invalidOutput("unknown intent kind")
	}
	f, err := resolve(i.Field)
	if err != nil {
		return nil, err
	}
	op := i.Operator
	switch op {
	case "=", "!=", ">", ">=", "<", "<=", "~", "!~", "IN", "NOT IN", "IS", "IS NOT":
	default:
		return nil, invalidOutput("invalid comparison operator")
	}
	if (op == ">" || op == ">=" || op == "<" || op == "<=") && f.Type != "number" && f.Type != "integer" && f.Type != "date" && f.Type != "datetime" {
		return nil, invalidOutput("ordered comparison requires numeric or date field")
	}
	if (op == "~" || op == "!~") && f.Type != "string" {
		return nil, invalidOutput("text operator requires a text field")
	}
	if op == "IN" || op == "NOT IN" {
		if i.Value != nil || len(i.Values) == 0 || len(i.Values) > 100 {
			return nil, invalidOutput("invalid membership values")
		}
		values := make([]string, 0, len(i.Values))
		for _, v := range i.Values {
			value, err := modelOperand(v, f, self)
			if err != nil {
				return nil, err
			}
			if v.Kind == "empty" {
				return nil, invalidOutput("empty membership operand")
			}
			values = append(values, value)
		}
		return comparison(fieldmeta.JQLName(f), op, "("+strings.Join(values, ", ")+")"), nil
	}
	if i.Value == nil || len(i.Values) > 0 {
		return nil, invalidOutput("comparison requires one value")
	}
	if (op == "IS" || op == "IS NOT") != (i.Value.Kind == "empty") {
		return nil, invalidOutput("invalid null comparison")
	}
	value, err := modelOperand(*i.Value, f, self)
	if err != nil {
		return nil, err
	}
	return comparison(fieldmeta.JQLName(f), op, value), nil
}
func modelOwner(v *ModelValue) (OwnerIntent, error) {
	if v == nil || v.Days != 0 || v.Offset != 0 {
		return OwnerIntent{}, invalidOutput("invalid owner")
	}
	if v.Kind == "self" && v.Text == "" {
		return OwnerIntent{Self: true}, nil
	}
	if v.Kind == "string" && strings.TrimSpace(v.Text) != "" {
		return OwnerIntent{Name: v.Text}, nil
	}
	return OwnerIntent{}, invalidOutput("owner must be self or a named user")
}
func modelOperand(v ModelValue, f fieldmeta.Field, self string) (string, error) {
	if v.Kind != "relative_days" && v.Days != 0 || v.Kind != "calendar_function" && v.Offset != 0 {
		return "", invalidOutput("unexpected operand data")
	}
	if f.Type == "array" && f.ItemType != "" {
		f.Type = f.ItemType
	}
	date := f.Type == "date" || f.Type == "datetime"
	switch v.Kind {
	case "empty":
		if v.Text == "" {
			return "EMPTY", nil
		}
	case "self":
		if v.Text == "" && (f.Type == "user" || f.ID == "assignee" || f.ID == "reporter" || f.ID == "creator") {
			if self == "" {
				self = "currentUser()"
			}
			return self, nil
		}
	case "string":
		if f.Type != "" && !date && f.Type != "number" && f.Type != "integer" && f.Type != "boolean" {
			return jql.QuoteIdent(v.Text), nil
		}
	case "number":
		if len(v.Text) <= 64 && modelNumber.MatchString(v.Text) && (f.Type == "number" || f.Type == "integer" || f.ID == "id") {
			return v.Text, nil
		}
	case "boolean":
		if f.Type == "boolean" && (v.Text == "true" || v.Text == "false") {
			return v.Text, nil
		}
	case "relative_days":
		if date && v.Days > 0 && v.Days <= 100000 && v.Text == "" {
			return "-" + strconv.Itoa(v.Days) + "d", nil
		}
	case "calendar_date":
		if date {
			if _, err := time.Parse("2006-01-02", v.Text); err == nil {
				return jql.Quote(v.Text), nil
			}
		}
	case "calendar_function":
		if date && v.Offset >= -100000 && v.Offset <= 100000 {
			switch v.Text {
			case "now":
				if v.Offset != 0 {
					break
				}
				fallthrough
			case "startOfDay", "startOfWeek", "startOfMonth", "endOfDay", "endOfWeek", "endOfMonth":
				return (DateBound{Kind: BoundFunction, Function: v.Text, Offset: v.Offset}).JQL(), nil
			}
		}
	}
	return "", invalidOutput("operand does not match field type or contract")
}
