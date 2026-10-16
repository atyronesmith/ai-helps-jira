package jql

import (
	"strings"
)

// Expr is a boolean JQL expression.
type Expr interface{ render(parent int) string }
type Clause struct{ Raw string }

func (c Clause) render(int) string { return strings.TrimSpace(c.Raw) }

type And struct{ Terms []Expr }

func (a And) render(parent int) string {
	s := renderJoin(a.Terms, " AND ", 2)
	if parent > 2 {
		return "(" + s + ")"
	}
	return s
}

type Or struct{ Terms []Expr }

func (o Or) render(parent int) string {
	s := renderJoin(o.Terms, " OR ", 1)
	if parent > 1 {
		return "(" + s + ")"
	}
	return s
}

type Not struct{ Term Expr }

func (n Not) render(parent int) string {
	s := "NOT " + n.Term.render(3)
	if parent > 3 {
		return "(" + s + ")"
	}
	return s
}
func renderJoin(es []Expr, sep string, p int) string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		if e != nil {
			out = append(out, e.render(p))
		}
	}
	return strings.Join(out, sep)
}
func All(es ...Expr) Expr { return combine(es, true) }
func Any(es ...Expr) Expr { return combine(es, false) }
func combine(es []Expr, and bool) Expr {
	flat := []Expr{}
	for _, e := range es {
		if e == nil {
			continue
		}
		if and {
			if a, ok := e.(And); ok {
				flat = append(flat, a.Terms...)
				continue
			}
		} else if a, ok := e.(Or); ok {
			flat = append(flat, a.Terms...)
			continue
		}
		flat = append(flat, e)
	}
	if len(flat) == 0 {
		return nil
	}
	if len(flat) == 1 {
		return flat[0]
	}
	if and {
		return And{flat}
	}
	return Or{flat}
}
func Negate(e Expr) Expr {
	if e == nil {
		return nil
	}
	return Not{e}
}
func Atom(raw string) (Expr, error) {
	if _, _, _, err := parseClause(raw); err != nil {
		return nil, err
	}
	return Clause{strings.TrimSpace(raw)}, nil
}

// Compare constructs an atomic comparison. field and value are JQL operands;
// quote untrusted literals with Quote/QuoteIdent. Execution boundaries still
// Validate the rendered tree, including any caller-supplied atomic syntax.
func Compare(field, op, value string) Expr {
	return Clause{strings.TrimSpace(field) + " " + strings.TrimSpace(op) + " " + strings.TrimSpace(value)}
}

type Sort struct{ Field, Direction string }
type Query struct {
	Where   Expr
	OrderBy []Sort
}

func (q *Query) Render() string {
	if q == nil {
		return ""
	}
	s := ""
	if q.Where != nil {
		s = q.Where.render(0)
	}
	if len(q.OrderBy) > 0 {
		xs := make([]string, 0, len(q.OrderBy))
		for _, v := range q.OrderBy {
			d := strings.ToUpper(v.Direction)
			x := strings.TrimSpace(v.Field)
			if d != "" {
				x += " " + d
			}
			xs = append(xs, x)
		}
		if s != "" {
			s += " "
		}
		s += "ORDER BY " + strings.Join(xs, ", ")
	}
	return s
}
func (q *Query) AndClause(e Expr) {
	if q == nil || e == nil {
		return
	}
	q.Where = All(q.Where, e)
}
func (q *Query) HasProjectScope() bool    { return q != nil && scoped(q.Where) }
func (q *Query) DirectIdentityOnly() bool { return q != nil && identityOnly(q.Where) }

// HasField reports actual predicates, including beneath negation, rather than
// matching field-like words inside a quoted value.
func (q *Query) HasField(field string) bool {
	var visit func(Expr) bool
	visit = func(e Expr) bool {
		switch v := e.(type) {
		case And:
			for _, child := range v.Terms {
				if visit(child) {
					return true
				}
			}
		case Or:
			for _, child := range v.Terms {
				if visit(child) {
					return true
				}
			}
		case Not:
			return visit(v.Term)
		case Clause:
			f, _, _, ok := clauseParts(v.Raw)
			return ok && strings.EqualFold(f, field)
		}
		return false
	}
	return q != nil && visit(q.Where)
}

func scoped(e Expr) bool {
	switch v := e.(type) {
	case And:
		for _, x := range v.Terms {
			if scoped(x) {
				return true
			}
		}
	case Or:
		for _, x := range v.Terms {
			if !scoped(x) {
				return false
			}
		}
		return len(v.Terms) > 0
	case Not:
		return false
	case Clause:
		f, op, _, ok := clauseParts(v.Raw)
		return ok && strings.EqualFold(f, "project") && (op == "=" || strings.EqualFold(op, "IN"))
	}
	return false
}
func identityOnly(e Expr) bool {
	switch v := e.(type) {
	case And:
		for _, x := range v.Terms {
			if !identityOnly(x) {
				return false
			}
		}
		return len(v.Terms) > 0
	case Or:
		for _, x := range v.Terms {
			if !identityOnly(x) {
				return false
			}
		}
		return len(v.Terms) > 0
	case Not:
		return false
	case Clause:
		f, op, val, ok := clauseParts(v.Raw)
		if !ok || !(strings.EqualFold(f, "key") || strings.EqualFold(f, "id")) {
			return false
		}
		if op != "=" && !strings.EqualFold(op, "IN") {
			return false
		}
		return scalarOrList(val)
	}
	return false
}
func scalarOrList(s string) bool {
	n, err := syntaxParser.ParseString("", "key IN "+s)
	if err == nil && n.Where != nil && n.Where.First.First.Atom != nil && n.Where.First.First.Atom.Membership != nil {
		for _, v := range n.Where.First.First.Atom.Membership.List.Values {
			if v.Function != nil || v.List != nil || !validScalar(v.Scalar) {
				return false
			}
		}
		return true
	}
	return validScalar(s)
}
func validScalar(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if s[0] == '\'' || s[0] == '"' {
		return len(s) > 1 && s[len(s)-1] == s[0]
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
func clauseParts(s string) (string, string, string, bool) {
	f, op, v, err := parseClause(s)
	return f, op, v, err == nil
}
