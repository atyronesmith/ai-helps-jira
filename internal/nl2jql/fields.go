package nl2jql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

// Deterministic plans use the same catalog boundary as model plans. Core
// operands and their established Phase 3 semantics remain untouched.
func resolvePlanFields(p *QueryPlan, r *fieldmeta.Resolver, project string) error {
	var projects, types []string
	var scopes func(*Intent)
	scopes = func(i *Intent) {
		if i == nil {
			return
		}
		if i.Kind == IntentComparison && (i.Operator == "=" || i.Operator == "IN") {
			values := []string{unquoteField(i.Value)}
			if i.Operator == "IN" {
				values = canonicalList(i.Value)
			}
			switch i.Field {
			case "project":
				projects = append(projects, values...)
			case "issuetype":
				types = append(types, values...)
			}
		}
		for _, c := range i.Children {
			scopes(c)
		}
	}
	scopes(p.Filter)
	if len(projects) == 0 {
		projects = []string{project}
	}
	if len(types) == 0 {
		types = []string{""}
	}
	resolve := func(name string) (string, error) {
		name = unquoteField(name)
		if _, ok := fieldmeta.Core(name); ok {
			return name, nil
		}
		var resolved fieldmeta.Field
		for _, project := range projects {
			for _, kind := range types {
				f, err := r.Resolve(name, project, kind)
				if err != nil {
					return "", err
				}
				if resolved.ID != "" && resolved.ID != f.ID {
					return "", fieldmeta.ErrAmbiguousField
				}
				resolved = f
			}
		}
		return fieldmeta.JQLName(resolved), nil
	}
	var walk func(*Intent) error
	walk = func(i *Intent) error {
		if i == nil {
			return nil
		}
		if i.Kind == IntentComparison {
			field, err := resolve(i.Field)
			if err != nil {
				return err
			}
			if _, core := fieldmeta.Core(unquoteField(i.Field)); !core {
				f, err := r.Resolve(unquoteField(i.Field), projects[0], types[0])
				if err != nil {
					return err
				}
				if (i.Operator == ">" || i.Operator == ">=" || i.Operator == "<" || i.Operator == "<=") && (f.Type != "number" && f.Type != "integer") {
					return fmt.Errorf("%w: %s requires numeric metadata", fieldmeta.ErrMissingMetadata, f.Name)
				}
			}
			i.Field = field
		}
		for _, c := range i.Children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(p.Filter); err != nil {
		return err
	}
	for n := range p.Sort {
		field, err := resolve(p.Sort[n].Field)
		if err != nil {
			return err
		}
		if strings.EqualFold(unquoteField(p.Sort[n].Field), "Story Points") {
			for _, project := range projects {
				for _, kind := range types {
					meta, err := r.Resolve("Story Points", project, kind)
					if err != nil {
						return err
					}
					if meta.Type != "number" && meta.Type != "integer" {
						return fmt.Errorf("%w: Story Points ordering requires numeric metadata", fieldmeta.ErrMissingMetadata)
					}
				}
			}
		}
		p.Sort[n].Field = field
	}
	return nil
}
func unquoteField(s string) string {
	if value, err := strconv.Unquote(s); err == nil {
		return value
	}
	return strings.Trim(s, "'")
}

// canonicalList reads parser-produced scalar lists, preserving quoted commas.
func canonicalList(raw string) []string {
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "("), ")")
	var result []string
	start := 0
	quoted := false
	escaped := false
	for n, c := range raw {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quoted {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == ',' && !quoted {
			result = append(result, unquoteField(strings.TrimSpace(raw[start:n])))
			start = n + 1
		}
	}
	return append(result, unquoteField(strings.TrimSpace(raw[start:])))
}
