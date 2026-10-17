package nl2jql

import (
	"fmt"
	"strings"
)

// A single-valued field cannot equal two different values in one Boolean
// branch. nil denotes an unrestricted branch; OR unions and AND intersects.
func exclusiveValues(i *Intent, field, self string) (map[string]bool, error) {
	if i == nil {
		return nil, nil
	}
	valueKey := func(s string) string { return strings.ToLower(unquoteField(s)) }
	if field == "assignee" && i.Kind == IntentOwner {
		value := self
		if value == "" {
			value = "currentUser()"
		}
		if !i.Owner.Self {
			value = i.Owner.Name
		}
		return map[string]bool{valueKey(value): true}, nil
	}
	if i.Kind == IntentComparison && i.Field == field {
		switch i.Operator {
		case "=":
			return map[string]bool{valueKey(i.Value): true}, nil
		case "IN":
			values := map[string]bool{}
			for _, v := range canonicalList(i.Value) {
				values[valueKey(v)] = true
			}
			return values, nil
		}
	}
	if i.Kind != IntentAll && i.Kind != IntentAny && i.Kind != IntentNot {
		return nil, nil
	}
	var result map[string]bool
	unrestricted := false
	for _, child := range i.Children {
		values, err := exclusiveValues(child, field, self)
		if err != nil {
			return nil, err
		}
		if i.Kind == IntentAny {
			if values == nil {
				unrestricted = true
				continue
			}
			if result == nil {
				result = map[string]bool{}
			}
			for v := range values {
				result[v] = true
			}
		} else if i.Kind == IntentAll && values != nil {
			if result == nil {
				result = values
				continue
			}
			for v := range result {
				if !values[v] {
					delete(result, v)
				}
			}
			if len(result) == 0 {
				return nil, fmt.Errorf("%w: conflicting %s values in a Boolean branch", ErrAmbiguousIntent, field)
			}
		}
	}
	if i.Kind == IntentNot || unrestricted {
		return nil, nil
	}
	return result, nil
}
