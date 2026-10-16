package jql

import (
	"fmt"
	"strings"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

var jqlLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Whitespace", Pattern: `\s+`},
	{Name: "String", Pattern: `"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`},
	{Name: "Keyword", Pattern: `(?i)\b(?:ORDER|BY|AND|OR|NOT|IN|IS|WAS|CHANGED|FROM|TO|BEFORE|AFTER|ON|DURING|EMPTY|NULL)\b`},
	{Name: "Custom", Pattern: `(?i)cf\[[0-9]+\]`},
	{Name: "Operator", Pattern: `!=|!~|<=|>=|=|<|>|~`},
	{Name: "Relative", Pattern: `[+-][0-9]+[dwmyhM]\b`},
	{Name: "Number", Pattern: `[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)`},
	{Name: "Ident", Pattern: `[A-Za-z_][A-Za-z0-9_.-]*`},
	{Name: "Punct", Pattern: `[(),]`},
})

type syntaxQuery struct {
	Where *syntaxOr    `parser:"@@?"`
	Order *syntaxOrder `parser:"@@?"`
}
type syntaxOr struct {
	First *syntaxAnd   `parser:"@@"`
	Rest  []*syntaxAnd `parser:"( \"OR\" @@ )*"`
}
type syntaxAnd struct {
	First *syntaxUnary   `parser:"@@"`
	Rest  []*syntaxUnary `parser:"( \"AND\" @@ )*"`
}
type syntaxUnary struct {
	Not   *syntaxUnary  `parser:" \"NOT\" @@"`
	Group *syntaxOr     `parser:"| \"(\" @@ \")\""`
	Atom  *syntaxClause `parser:"| @@"`
}
type syntaxClause struct {
	Field      string            `parser:"@(Ident | String | Custom)"`
	Compare    *syntaxComparison `parser:"( @@"`
	Membership *syntaxMembership `parser:"| @@"`
	Is         *syntaxIs         `parser:"| @@"`
	Was        *syntaxWas        `parser:"| @@"`
	Changed    *syntaxChanged    `parser:"| @@ )"`
}
type syntaxComparison struct {
	Op    string         `parser:"@Operator"`
	Value *syntaxOperand `parser:"@@"`
}
type syntaxMembership struct {
	Not  bool        `parser:"@\"NOT\"?"`
	List *syntaxList `parser:"\"IN\" @@"`
}
type syntaxIs struct {
	Not   bool   `parser:"\"IS\" @\"NOT\"?"`
	Value string `parser:"@( \"EMPTY\" | \"NULL\" )"`
}
type syntaxWas struct {
	Not       bool              `parser:"\"WAS\" @\"NOT\"?"`
	In        bool              `parser:"@\"IN\"?"`
	Value     *syntaxOperand    `parser:"@@"`
	Modifiers []*syntaxModifier `parser:"@@*"`
}
type syntaxChanged struct {
	Modifiers []*syntaxModifier `parser:"\"CHANGED\" @@*"`
}
type syntaxModifier struct {
	During *syntaxList    `parser:"\"DURING\" @@"`
	Kind   string         `parser:"| @( \"FROM\" | \"TO\" | \"BY\" | \"BEFORE\" | \"AFTER\" | \"ON\" )"`
	Value  *syntaxOperand `parser:"@@"`
}
type syntaxOperand struct {
	Function *syntaxFunction `parser:"@@"`
	List     *syntaxList     `parser:"| @@"`
	Scalar   string          `parser:"| @( String | Ident | Number | Relative | \"EMPTY\" | \"NULL\" )"`
}
type syntaxFunction struct {
	Name string           `parser:"@Ident"`
	Args []*syntaxOperand `parser:"\"(\" ( @@ ( \",\" @@ )* )? \")\""`
}
type syntaxList struct {
	Values []*syntaxOperand `parser:"\"(\" @@ ( \",\" @@ )* \")\""`
}
type syntaxOrder struct {
	Fields []*syntaxSort `parser:"\"ORDER\" \"BY\" @@ ( \",\" @@ )*"`
}
type syntaxSort struct {
	Field     string `parser:"@( Ident | String | Custom )"`
	Direction string `parser:"@Ident?"`
}

var syntaxParser = participle.MustBuild[syntaxQuery](participle.Lexer(jqlLexer), participle.Elide("Whitespace"), participle.CaseInsensitive("Keyword"), participle.UseLookahead(3))

// Parse recognizes boolean precedence, atomic operators, history modifiers and
// one terminal ordering clause. Unknown syntax fails closed. Sort-only queries
// can be scoped by callers; Validate requires a nonempty predicate.
func Parse(raw string) (*Query, error) {
	node, err := syntaxParser.ParseString("", raw)
	if err != nil {
		return nil, fmt.Errorf("invalid JQL %q: %w", raw, err)
	}
	q := &Query{}
	if node.Where != nil {
		q.Where, err = lowerOr(node.Where)
		if err != nil {
			return nil, fmt.Errorf("invalid JQL %q: %w", raw, err)
		}
	}
	if node.Order != nil {
		for _, s := range node.Order.Fields {
			direction := strings.ToUpper(s.Direction)
			if direction != "" && direction != "ASC" && direction != "DESC" {
				return nil, fmt.Errorf("invalid JQL %q: invalid sort direction %q", raw, s.Direction)
			}
			q.OrderBy = append(q.OrderBy, Sort{Field: s.Field, Direction: direction})
		}
	}
	return q, nil
}
func lowerOr(n *syntaxOr) (Expr, error) {
	a, err := lowerAnd(n.First)
	if err != nil {
		return nil, err
	}
	terms := []Expr{a}
	for _, v := range n.Rest {
		e, err := lowerAnd(v)
		if err != nil {
			return nil, err
		}
		terms = append(terms, e)
	}
	return Any(terms...), nil
}
func lowerAnd(n *syntaxAnd) (Expr, error) {
	a, err := lowerUnary(n.First)
	if err != nil {
		return nil, err
	}
	terms := []Expr{a}
	for _, v := range n.Rest {
		e, err := lowerUnary(v)
		if err != nil {
			return nil, err
		}
		terms = append(terms, e)
	}
	return All(terms...), nil
}
func lowerUnary(n *syntaxUnary) (Expr, error) {
	if n.Not != nil {
		e, err := lowerUnary(n.Not)
		if err != nil {
			return nil, err
		}
		return Negate(e), nil
	}
	if n.Group != nil {
		return lowerOr(n.Group)
	}
	raw, err := n.Atom.render()
	if err != nil {
		return nil, err
	}
	return Clause{Raw: raw}, nil
}
func (v *syntaxOperand) render() string {
	if v.Function != nil {
		args := []string{}
		for _, a := range v.Function.Args {
			args = append(args, a.render())
		}
		return v.Function.Name + "(" + strings.Join(args, ", ") + ")"
	}
	if v.List != nil {
		return v.List.render()
	}
	return v.Scalar
}
func (v *syntaxList) render() string {
	args := []string{}
	for _, a := range v.Values {
		args = append(args, a.render())
	}
	return "(" + strings.Join(args, ", ") + ")"
}
func modifiers(ms []*syntaxModifier) (string, error) {
	var parts []string
	seen := map[string]bool{}
	for _, m := range ms {
		key := strings.ToUpper(m.Kind)
		if m.During != nil {
			key = "DURING"
			if len(m.During.Values) != 2 {
				return "", fmt.Errorf("DURING requires two bounds")
			}
			parts = append(parts, "DURING "+m.During.render())
		} else {
			if m.Value == nil || (m.Value.List != nil && key != "BY") {
				return "", fmt.Errorf("history %s requires a scalar operand", key)
			}
			parts = append(parts, key+" "+m.Value.render())
		}
		if seen[key] {
			return "", fmt.Errorf("duplicate history %s", key)
		}
		seen[key] = true
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " " + strings.Join(parts, " "), nil
}
func (c *syntaxClause) render() (string, error) {
	suffix := ""
	switch {
	case c.Compare != nil:
		if c.Compare.Value.List != nil {
			return "", fmt.Errorf("comparison requires a scalar operand")
		}
		suffix = c.Compare.Op + " " + c.Compare.Value.render()
	case c.Membership != nil:
		suffix = "IN " + c.Membership.List.render()
		if c.Membership.Not {
			suffix = "NOT " + suffix
		}
	case c.Is != nil:
		suffix = "IS "
		if c.Is.Not {
			suffix += "NOT "
		}
		suffix += strings.ToUpper(c.Is.Value)
	case c.Was != nil:
		v := c.Was
		if v.In != (v.Value.List != nil) {
			return "", fmt.Errorf("WAS IN requires a list; WAS requires a scalar")
		}
		suffix = "WAS "
		if v.Not {
			suffix += "NOT "
		}
		if v.In {
			suffix += "IN "
		}
		suffix += v.Value.render()
		m, err := modifiers(v.Modifiers)
		if err != nil {
			return "", err
		}
		suffix += m
	case c.Changed != nil:
		m, err := modifiers(c.Changed.Modifiers)
		if err != nil {
			return "", err
		}
		suffix = "CHANGED" + m
	}
	return c.Field + " " + suffix, nil
}

// parseClause uses exactly the same grammar as Parse; atomic helpers never
// maintain a second structural scanner.
func parseClause(raw string) (string, string, string, error) {
	n, err := syntaxParser.ParseString("", raw)
	if err != nil {
		return "", "", "", err
	}
	if n.Order != nil || n.Where == nil || len(n.Where.Rest) > 0 || len(n.Where.First.Rest) > 0 {
		return "", "", "", fmt.Errorf("expected one JQL clause")
	}
	u := n.Where.First.First
	if u.Atom == nil {
		return "", "", "", fmt.Errorf("expected one JQL clause")
	}
	c := u.Atom
	if _, err := c.render(); err != nil {
		return "", "", "", err
	}
	field := c.Field
	if len(field) > 1 && (field[0] == '"' || field[0] == '\'') {
		field = field[1 : len(field)-1]
	}
	switch {
	case c.Compare != nil:
		return field, c.Compare.Op, c.Compare.Value.render(), nil
	case c.Membership != nil:
		op := "IN"
		if c.Membership.Not {
			op = "NOT IN"
		}
		return field, op, c.Membership.List.render(), nil
	case c.Is != nil:
		return field, "IS", c.Is.Value, nil
	case c.Was != nil:
		return field, "WAS", c.Was.Value.render(), nil
	default:
		return field, "CHANGED", "", nil
	}
}
