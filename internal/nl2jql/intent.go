package nl2jql

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// ErrAmbiguousIntent requires clarification. Neither fallback tier may guess
// at a meaning after this error, even if a provider happens to be available.
var ErrAmbiguousIntent = errors.New("nl2jql: ambiguous intent; please clarify")

type QueryMode string

const (
	ModeList           QueryMode = "list"
	ModeCount          QueryMode = "count"
	ModeRecommendation QueryMode = "recommendation"
)

type IntentKind string

const (
	IntentComparison IntentKind = "comparison"
	IntentAll        IntentKind = "and"
	IntentAny        IntentKind = "or"
	IntentNot        IntentKind = "not"
	IntentOwner      IntentKind = "owner"
	IntentHistory    IntentKind = "history"
	IntentDate       IntentKind = "date"
)

// Intent describes requested meaning independently of JQL syntax. Values in
// comparisons use the canonical Jira vocabulary; ownership remains explicit.
type Intent struct {
	Kind                   IntentKind
	Field, Operator, Value string
	Children               []*Intent
	Owner                  *OwnerIntent
	History                *HistoryIntent
	Date                   *DateIntent
}
type OwnerIntent struct {
	Self bool
	Name string
}
type DateBoundKind string

const (
	BoundRelative DateBoundKind = "relative_days"
	BoundFunction DateBoundKind = "calendar_function"
	BoundLiteral  DateBoundKind = "calendar_date"
)

type DateBound struct {
	Kind     DateBoundKind
	Days     int
	Function string
	Offset   int
	Date     time.Time
}

func (b DateBound) JQL() string {
	switch b.Kind {
	case BoundRelative:
		return fmt.Sprintf("-%dd", b.Days)
	case BoundLiteral:
		return jql.Quote(b.Date.Format("2006-01-02"))
	default:
		if b.Offset == 0 {
			return b.Function + "()"
		}
		return b.Function + "(" + jql.Quote(fmt.Sprintf("%+d", b.Offset)) + ")"
	}
}

type DateIntent struct {
	Field, Operator string
	Bound           DateBound
}
type DateWindow struct {
	Field                          string
	Lower, Upper                   *DateBound
	LowerInclusive, UpperInclusive bool
}

var dateFunctionBound = regexp.MustCompile(`(?i)^(startOfDay|startOfWeek|startOfMonth|endOfDay|endOfWeek|endOfMonth|now)\((?:"([+-]?[0-9]+)")?\)$`)

func parseDateBound(raw string) (DateBound, error) {
	if m := dateFunctionBound.FindStringSubmatch(raw); m != nil {
		fn := map[string]string{"startofday": "startOfDay", "startofweek": "startOfWeek", "startofmonth": "startOfMonth", "endofday": "endOfDay", "endofweek": "endOfWeek", "endofmonth": "endOfMonth", "now": "now"}[strings.ToLower(m[1])]
		offset := 0
		var err error
		if m[2] != "" {
			offset, err = strconv.Atoi(m[2])
			if err != nil {
				return DateBound{}, err
			}
		}
		return DateBound{Kind: BoundFunction, Function: fn, Offset: offset}, nil
	}
	if strings.HasPrefix(raw, "-") {
		days, err := strconv.Atoi(raw[1 : len(raw)-1])
		if err != nil {
			return DateBound{}, err
		}
		if strings.HasSuffix(raw, "w") {
			if days > 100000 {
				return DateBound{}, ErrAmbiguousIntent
			}
			days *= 7
		}
		return DateBound{Kind: BoundRelative, Days: days}, nil
	}
	date, err := time.Parse("2006-01-02", strings.Trim(raw, `"`))
	if err != nil {
		return DateBound{}, err
	}
	return DateBound{Kind: BoundLiteral, Date: date}, nil
}

type HistoryIntent struct {
	Status     string
	Actor      OwnerIntent
	Start, End string
}
type QueryPlan struct {
	Filter *Intent
	Dates  []DateWindow
	Sort   []jql.Sort
	Mode   QueryMode
	// Self is a JQL operand supplied by configuration (currentUser() or a
	// canonically quoted override), never inferred by replacing query text.
	Self string
}

func (p *QueryPlan) Compile() (*jql.Query, error) {
	if p == nil {
		return nil, ErrNoConstraints
	}
	self := p.Self
	if self == "" {
		self = "currentUser()"
	}
	expr, err := compileIntent(p.Filter, self)
	if err != nil {
		return nil, err
	}
	return &jql.Query{Where: expr, OrderBy: p.Sort}, nil
}
func compileIntent(i *Intent, self string) (jql.Expr, error) {
	if i == nil {
		return nil, nil
	}
	switch i.Kind {
	case IntentDate:
		return jql.Compare(i.Date.Field, i.Date.Operator, i.Date.Bound.JQL()), nil
	case IntentComparison:
		return jql.Compare(i.Field, i.Operator, i.Value), nil
	case IntentOwner:
		owner := self
		if !i.Owner.Self {
			owner = jql.QuoteIdent(i.Owner.Name)
		}
		return jql.Compare("assignee", "=", owner), nil
	case IntentHistory:
		actor := self
		if !i.History.Actor.Self {
			actor = jql.Quote(i.History.Actor.Name)
		}
		// DURING supplies the inclusive lower bound; BEFORE excludes the next
		// period's midnight on the same history event. Do not round an upper
		// bound to 23:59, which could lose events in the final minute.
		return jql.Atom("status CHANGED TO " + jql.QuoteIdent(i.History.Status) + " BY " + actor + " DURING (" + jql.Quote(i.History.Start) + ", " + jql.Quote(i.History.End) + ") BEFORE " + jql.Quote(i.History.End))
	case IntentAll, IntentAny, IntentNot:
		if i.Kind == IntentAny && len(i.Children) > 1 {
			owners := []string{}
			allOwners := true
			for _, c := range i.Children {
				if c.Kind != IntentOwner {
					allOwners = false
					break
				}
				value := self
				if !c.Owner.Self {
					value = jql.QuoteIdent(c.Owner.Name)
				}
				owners = append(owners, value)
			}
			if allOwners {
				return jql.Compare("assignee", "IN", "("+strings.Join(owners, ", ")+")"), nil
			}
		}
		children := make([]jql.Expr, 0, len(i.Children))
		for _, child := range i.Children {
			e, err := compileIntent(child, self)
			if err != nil {
				return nil, err
			}
			children = append(children, e)
		}
		if i.Kind == IntentAll {
			return jql.All(children...), nil
		}
		if i.Kind == IntentAny {
			return jql.Any(children...), nil
		}
		if len(children) != 1 {
			return nil, fmt.Errorf("invalid negation")
		}
		return jql.Negate(children[0]), nil
	}
	return nil, fmt.Errorf("unsupported intent %q", i.Kind)
}
func comparison(field, op, value string) *Intent {
	return &Intent{Kind: IntentComparison, Field: field, Operator: op, Value: value}
}
func joinIntent(kind IntentKind, children ...*Intent) *Intent {
	filtered := make([]*Intent, 0, len(children))
	for _, c := range children {
		if c != nil {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 && kind != IntentNot {
		return filtered[0]
	}
	return &Intent{Kind: kind, Children: filtered}
}

// ParsePlan exposes the typed meaning used by production translation and dry
// runs. now supplies calendar context; date functions execute in Jira's user
// timezone. A caller using named weekdays/years should align this clock with it.
func ParsePlan(input, self string, now time.Time) (*QueryPlan, error) {
	if err := checkAmbiguity(input); err != nil {
		return nil, err
	}
	if plan, ok, err := parseHistoryPlan(input, self, now); ok || err != nil {
		return plan, err
	}
	ast, err := jqlParser.ParseString("", strings.Join(strings.Fields(input), " "))
	if err != nil {
		return nil, fmt.Errorf("nl2jql: cannot parse %q: %w", input, err)
	}
	plan, err := ast.Plan(self, now)
	if err == nil {
		plan.Mode = queryMode(input)
	}
	return plan, err
}

type intentItem struct {
	expr             *Intent
	field, connector string
	grouped          bool
}

func (q *Query) Plan(self string, now time.Time) (*QueryPlan, error) { return q.plan(self, now, false) }
func (q *Query) plan(self string, now time.Time, nested bool) (*QueryPlan, error) {
	if q == nil {
		return nil, ErrNoConstraints
	}
	plan := &QueryPlan{Self: self, Mode: ModeList}
	var items []intentItem
	pending := ""

	var add func(*Clause) (*Intent, string, error)
	add = func(c *Clause) (*Intent, string, error) {
		switch {
		case c.Group != nil:
			sub, err := c.Group.plan(self, now, true)
			if err != nil {
				return nil, "", err
			}
			if len(sub.Sort) > 0 {
				return nil, "", fmt.Errorf("%w: ordering inside a Boolean group", ErrAmbiguousIntent)
			}
			plan.Dates = append(plan.Dates, sub.Dates...)
			return sub.Filter, "", nil
		case c.Negation != nil:
			i, _, err := add(c.Negation)
			if err != nil {
				return nil, "", err
			}
			if i == nil {
				return nil, "", fmt.Errorf("%w: negation without a predicate", ErrAmbiguousIntent)
			}
			return joinIntent(IntentNot, i), "", nil
		case c.Ownership != nil:
			o := c.Ownership
			owner := OwnerIntent{Self: o.Assignment == nil || o.Assignment.Self}
			if !owner.Self {
				owner.Name = clean(strings.Join(o.Assignment.Owner, " "))
				if owner.Name == "" {
					return nil, "", ErrAmbiguousIntent
				}
			}
			return &Intent{Kind: IntentOwner, Owner: &owner}, "assignee", nil
		case c.Date != nil:
			d := c.Date
			bound, err := parseDateBound(d.Bound)
			if err != nil {
				return nil, "", fmt.Errorf("%w: invalid date bound", ErrAmbiguousIntent)
			}
			field := strings.ToLower(d.Field)
			window := DateWindow{Field: field}
			if d.Operator == ">=" || d.Operator == ">" {
				window.Lower = &bound
				window.LowerInclusive = d.Operator == ">="
			} else if d.Operator == "<" || d.Operator == "<=" {
				window.Upper = &bound
				window.UpperInclusive = d.Operator == "<="
			}
			plan.Dates = append(plan.Dates, window)
			return &Intent{Kind: IntentDate, Date: &DateIntent{Field: field, Operator: d.Operator, Bound: bound}}, "date", nil
		}
		var p pred
		switch {
		case c.Project != nil:
			key := strings.ToLower(clean(strings.Join(c.Project.Key, " ")))
			if key == "current project" || key == "default project" {
				return nil, "", fmt.Errorf("%w: use an explicit project key or omit the project phrase", ErrAmbiguousIntent)
			}
			p = c.Project.predicate()
		case c.IssueType != nil:
			p = c.IssueType.predicate()
		case c.Status != nil:
			if normaliseStatus(c.Status.Raw) == "open" {
				return comparison("statusCategory", "!=", "Done"), "status", nil
			}
			p = c.Status.predicate()
		case c.Priority != nil:
			p = c.Priority.predicate()
		case c.StoryPoints != nil:
			return comparison(jql.Quote("Story Points"), c.StoryPoints.Operator, c.StoryPoints.Value), "points", nil
		default:
			return nil, "", nil
		}
		if p.Field == "" {
			return nil, "", ErrAmbiguousIntent
		}
		return comparison(p.Field, "=", p.Value), p.Field, nil
	}
	for _, c := range q.Clauses {
		if c == nil || c.Noise != nil {
			continue
		}
		if c.Connector != "" {
			if pending != "" || len(items) == 0 {
				return nil, fmt.Errorf("%w: dangling Boolean operator", ErrAmbiguousIntent)
			}
			pending = strings.ToUpper(c.Connector)
			continue
		}
		if c.OrderBy != nil {
			if len(plan.Sort) > 0 {
				return nil, fmt.Errorf("%w: multiple ordering clauses", ErrAmbiguousIntent)
			}
			terms := []*SortTerm{{Field: c.OrderBy.Field, Direction: c.OrderBy.Direction}}
			terms = append(terms, c.OrderBy.More...)
			for _, term := range terms {
				o := &OrderBy{Field: term.Field, Direction: term.Direction}
				sortValue, err := o.sort()
				if err != nil {
					return nil, err
				}
				plan.Sort = append(plan.Sort, sortValue)
			}
			continue
		}

		i, field, err := add(c)
		if err != nil {
			return nil, err
		}
		if i != nil {
			items = append(items, intentItem{i, field, pending, c.Group != nil || c.Negation != nil})
			pending = ""
		}
	}
	if pending != "" {
		return nil, fmt.Errorf("%w: dangling Boolean operator", ErrAmbiguousIntent)
	}
	// Cross-field alternatives need an explicit group. Inside a group, regular
	// AND-before-OR precedence gives a composable, unsurprising interpretation.
	crossOR := false
	for idx, item := range items {
		if item.connector == "OR" && (item.field == "" || item.field == "date" || item.field == "points" || item.field != items[idx-1].field) {
			if !nested && !(item.grouped && items[idx-1].grouped) {
				return nil, fmt.Errorf("%w: group mixed OR alternatives with parentheses", ErrAmbiguousIntent)
			}
			crossOR = true
		}
	}
	if crossOR && !nested {
		for _, item := range items {
			if !item.grouped {
				return nil, fmt.Errorf("%w: put common filters outside a single group of alternatives", ErrAmbiguousIntent)
			}
		}
	}
	if crossOR {
		var arms []*Intent
		var branch []intentItem
		for _, item := range items {
			if item.connector == "OR" {
				expr, err := flatIntent(branch)
				if err != nil {
					return nil, err
				}
				arms = append(arms, expr)
				branch = nil
			}
			branch = append(branch, item)
		}
		expr, err := flatIntent(branch)
		if err != nil {
			return nil, err
		}
		arms = append(arms, expr)
		plan.Filter = joinIntent(IntentAny, arms...)
	} else {
		var err error
		plan.Filter, err = flatIntent(items)
		if err != nil {
			return nil, err
		}
	}

	if plan.Filter == nil && len(plan.Sort) == 0 {
		return nil, ErrNoConstraints
	}
	if _, err := statusPossibilities(plan.Filter); err != nil {
		return nil, err
	}
	return plan, nil
}
func intentEqual(a, b *Intent) bool {
	if a.Kind != b.Kind || a.Field != b.Field || a.Operator != b.Operator || a.Value != b.Value {
		return false
	}
	if a.Owner != nil && b.Owner != nil {
		return *a.Owner == *b.Owner
	}
	return a.Owner == nil && b.Owner == nil
}

// Validate each Boolean branch as well as flat enumerations. Parentheses must
// never become a way to bypass contradiction detection.
func statusPossibilities(i *Intent) (uint, error) {
	const all uint = 31
	if i == nil {
		return all, nil
	}
	switch i.Kind {
	case IntentComparison:
		if i.Field == "statusCategory" && i.Operator == "!=" && i.Value == "Done" {
			return 7, nil
		}
		if i.Field == "status" && i.Operator == "=" {
			switch i.Value {
			case "Done":
				return 8, nil
			case "Closed":
				return 16, nil
			case `"In Progress"`:
				return 2, nil
			}
		}
	case IntentAll, IntentAny:
		result := all
		if i.Kind == IntentAny {
			result = 0
		}
		for _, c := range i.Children {
			mask, err := statusPossibilities(c)
			if err != nil {
				return 0, err
			}
			if i.Kind == IntentAll {
				result &= mask
			} else {
				result |= mask
			}
		}
		if result == 0 && i.Kind == IntentAll {
			return 0, fmt.Errorf("%w: contradictory statuses in a Boolean branch", ErrAmbiguousIntent)
		}
		return result, nil
	}
	return all, nil
}

func flatIntent(items []intentItem) (*Intent, error) {
	// Same-field enumerations remain alternatives. Status conjunctions are
	// rejected, including implicit adjacency; repeating the same status is safe.
	last := map[string]int{}
	for index, item := range items {
		if prev, ok := last[item.field]; ok && index > prev+1 && item.field != "" && item.field != "date" && item.field != "points" && !intentEqual(items[prev].expr, item.expr) {
			return nil, fmt.Errorf("%w: group the associations between fields explicitly", ErrAmbiguousIntent)
		}
		last[item.field] = index
	}
	buckets := map[string][]*Intent{}
	var groups []*Intent
	for _, item := range items {
		field := item.field
		if field == "" || field == "date" || field == "points" {
			groups = append(groups, item.expr)
			continue
		}
		bucket := buckets[field]
		duplicate := false
		for _, old := range bucket {
			if intentEqual(old, item.expr) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if field == "assignee" && len(bucket) > 0 && item.connector != "OR" {
			return nil, fmt.Errorf("%w: specify alternative assignees with OR", ErrAmbiguousIntent)
		}
		if field == "status" && len(bucket) > 0 && item.connector != "OR" {
			return nil, fmt.Errorf("%w: an issue cannot be both requested statuses; use OR", ErrAmbiguousIntent)
		}
		buckets[field] = append(bucket, item.expr)
	}
	var terms []*Intent
	for _, field := range []string{"project", "issuetype", "status", "assignee", "priority"} {
		bucket := buckets[field]
		if len(bucket) == 0 {
			continue
		}
		// IN consolidation only for equalities, never for category comparisons.
		equalities := true
		values := []string{}
		for _, i := range bucket {
			if i.Kind != IntentComparison || i.Operator != "=" {
				equalities = false
			}
			values = append(values, i.Value)
		}
		if len(bucket) > 1 && equalities {
			terms = append(terms, comparison(bucket[0].Field, "IN", "("+strings.Join(values, ", ")+")"))
		} else {
			terms = append(terms, joinIntent(IntentAny, bucket...))
		}
	}
	terms = append(terms, groups...)
	return joinIntent(IntentAll, terms...), nil
}
