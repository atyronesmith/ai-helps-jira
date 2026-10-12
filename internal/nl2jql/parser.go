// Package nl2jql converts a restricted subset of English agile questions
// into Jira Query Language (JQL).
//
// The pipeline is strictly two-phased:
//
//  1. Parse  – the input is tokenized and parsed into a typed AST with
//     github.com/alecthomas/participle/v2. Every token must belong to the
//     closed vocabulary below (issue types, statuses, ownership phrases,
//     project keys), or to the explicit filler list. Unknown words cause a
//     parse error instead of being guessed.
//
//  2. Render – Query.ToJQL traverses the AST and emits one JQL predicate per
//     clause, joined with AND in a fixed field order (project, issuetype,
//     status, assignee). Identical clause sets therefore always render to
//     byte-identical JQL regardless of the order the user wrote them in.
package nl2jql

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// ---------------------------------------------------------------------------
// Lexer
// ---------------------------------------------------------------------------

// fillerWords is the single source of truth for semantically inert words.
// Both the lexer rule and the post-parse cleaner derive from this list, so
// they can never drift apart.
var fillerWords = []string{
	"show", "find", "list", "get", "give", "fetch", "bring", "see",
	"need", "want", "please", "thank", "thanks",
	"all", "every", "any", "the", "a", "an", "only", "just",
	"currently", "still",
	"issue", "issues", "ticket", "tickets", "item", "items",
	"of", "for", "to", "and", "or",
	"that", "those", "these", "it", "them", "us", "you",
	"is", "are", "was", "were", "be", "been", "being",
	"have", "has", "had", "do", "does", "did",
	"on", "with", "related",
}

// fillerSet lets the renderer strip filler words that the grammar tolerated
// inside multi-token captures (e.g. "in the Support").
var fillerSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(fillerWords))
	for _, w := range fillerWords {
		m[w] = struct{}{}
	}
	return m
}()

// jqlLexer defines the closed natural-language vocabulary.
//
// Method calls must match exactly one of the user's known token types:
// the participle grammar references them by name (@InProgress, @Bugs, ...).
//
// Ordering matters: the stateful lexer picks the first rule that matches, so
// multi-word phrases MUST precede their single-word prefixes ("InProgress"
// before "In", "AssignedTo" before anything else), and the generic "Word"
// catch-all must come last.
//
// Keyword rules use (?i) for case-insensitive matching while preserving the
// original case of captured text (important for project keys and assignee names).
var jqlLexer = lexer.MustStateful(lexer.Rules{
	"Root": []lexer.Rule{
		// Layout tokens, elided via participle.Elide below.
		{Name: "Whitespace", Pattern: `[ \t\r\n]+`},
		{Name: "Punct", Pattern: `[.,!?;:'’]+`},

		// Multi-word phrases win over their single-word prefixes.
		// "[-\s_]+" also accepts "in_progress" / "assigned-to".
		{Name: "InProgress", Pattern: `(?i)\bin[-\s_]+progress\b`},
		{Name: "AssignedTo", Pattern: `(?i)\bassigned[-\s_]+to\b`},

		// Ownership vocabulary.
		{Name: "My", Pattern: `(?i)\b(?:my|mine|myself)\b`},
		{Name: "Me", Pattern: `(?i)\bme\b`},

		// Status vocabulary.
		{Name: "Open", Pattern: `(?i)\bopen\b`},
		{Name: "Done", Pattern: `(?i)\bdone\b`},

		// Issue-type vocabulary (singular forms accepted: bug, epic, task).
		{Name: "Bugs", Pattern: `(?i)\bbugs?\b`},
		{Name: "Epics", Pattern: `(?i)\bepics?\b`},
		{Name: "Tasks", Pattern: `(?i)\btasks?\b`},
		{Name: "Stories", Pattern: `(?i)\bstor(?:y|ies)\b`},

		// Project preposition.
		{Name: "In", Pattern: `(?i)\bin\b`},

		// Recognised noise words, kept as first-class tokens instead of
		// being elided: a bare "me" must remain attachable to "assigned
		// to", so it is ruled out here and handled by the Noise clause.
		{Name: "Filler", Pattern: `(?i)\b(?:` + strings.Join(fillerWords, "|") + `)\b`},

		// Catch-all: project keys, assignee names, ...
		{Name: "Word", Pattern: `[A-Za-z][A-Za-z0-9_-]*`},
	},
})

// ---------------------------------------------------------------------------
// AST
// ---------------------------------------------------------------------------
//
// The grammar (in EBNF shorthand) is:
//
//	Query     = Clause* .
//	Clause    = Ownership | IssueType | Status | Project | Noise .
//	Ownership = My | AssignedTo Assignment .
//	Assignment = Me | My | (Word | Filler)+ .
//	IssueType = Bugs | Epics | Tasks | Stories .
//	Status    = Open | Done | InProgress .
//	Project   = In (Word | Filler)+ .
//	Noise     = Filler .
//
// Every alternative in Clause starts with a mutually disjoint token set, so
// the parser can dispatch with one token of lookahead and never backtracks.

// Query is the root AST node: a sequence of independent clauses.
type Query struct {
	Clauses []*Clause `parser:"( @@ )*"`
}

// Clause maps to exactly one JQL predicate (or zero, for Noise).
type Clause struct {
	Ownership *Ownership `parser:"@@"`
	IssueType *IssueType `parser:"| @@"`
	Status    *Status    `parser:"| @@"`
	Project   *Project   `parser:"| @@"`
	Noise     *Noise     `parser:"| @@"`
}

// Ownership -> assignee = currentUser() | assignee = <name>.
type Ownership struct {
	// Self is true for "my" / "mine" / "myself".
	Self bool `parser:"@My"`
	// Assignment is set for the "assigned to ..." phrasing.
	Assignment *Assignment `parser:"| AssignedTo @@"`
}

// Assignment captures the target of "assigned to".
type Assignment struct {
	// Self is true for "assigned to me" / "assigned to myself".
	Self bool `parser:"@Me | @My"`
	// Owner is the raw token run for multi-word names ("John Smith");
	// spaces are preserved and filler words are stripped during rendering.
	Owner []string `parser:"| ( @Word | @Filler )+"`
}

// IssueType -> issuetype = Bug | Epic | Task | Story.
type IssueType struct {
	Raw string `parser:"@Bugs | @Epics | @Tasks | @Stories"`
}

// Status -> status NOT IN ("Done", "Closed", "Resolved") | "In Progress" | Done.
type Status struct {
	Raw string `parser:"@Open | @Done | @InProgress"`
}

// Project -> project = <Key>.
type Project struct {
	// In consumes the preposition; it carries no meaning beyond grammar.
	In string `parser:"@In"`
	// Key accumulates the project's name tokens; Fillers are only accepted so
	// that "in the Support" still parses and are stripped later.
	Key []string `parser:"( @Word | @Filler )+"`
}

// Noise consumes filler tokens that start no other clause ("show", "me",
// "all", ...). A bare "me" that is not the target of "assigned to" is inert.
// It never generates a predicate.
type Noise struct {
	Filler string `parser:"@Filler | @Me"`
}

// ErrNoConstraints is returned when parsing succeeded but the query did not
// contain a single word that maps to a JQL predicate.
var ErrNoConstraints = errors.New("nl2jql: query contains no JQL-constraining words")

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// issueTypeJQL maps the lowercased vocabulary token to Jira issue type names.
var issueTypeJQL = map[string]string{
	"bug": "Bug", "bugs": "Bug",
	"epic": "Epic", "epics": "Epic",
	"task": "Task", "tasks": "Task",
	"story": "Story", "stories": "Story",
}

// openStatusJQL is the JQL the fast path emits for the "open" vocabulary
// token. It mirrors QuerySystemPrompt rule #6 so the deterministic route and
// the JSON LLM route agree: "open" means "not one of the closed statuses"
// rather than a literal "Open" status, which only matches tenants that name a
// status "Open". Without this, "open bugs" resolves differently depending on
// which route a query takes -- the biggest behavioral regression risk for
// queries that hit the fast path.
const openStatusJQL = `status NOT IN ("Done", "Closed", "Resolved")`

// statusJQL maps the normalised vocabulary token to Jira status names. "open"
// is deliberately absent: Status.predicate handles it via openStatusJQL so the
// fast path never emits a literal "status = Open".
var statusJQL = map[string]string{
	"done":        "Done",
	"in progress": "In Progress",
}

// clean strips filler words that the grammar tolerated inside a multi-token
// capture ("the Support" -> "Support").
func clean(s string) string {
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, drop := fillerSet[strings.ToLower(f)]; drop {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// quoteIfNeeded quotes a JQL value only when the syntax requires it
// (embedded whitespace or quotes). It wraps internal/jql.QuoteIdent so this
// logic has one implementation in the tree rather than two (FIELDCTO-2812).
func quoteIfNeeded(s string) string {
	return jql.QuoteIdent(s)
}

// normaliseStatus lowercases the raw token and folds "-" / "_"
// separators so that "IN-PROGRESS" canonicalises to "In Progress".
func normaliseStatus(raw string) string {
	s := strings.ToLower(raw)
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// pred is one rendered JQL predicate. Field and Value are set for simple
// equality predicates (`field = value`), which is what lets ToJQL collapse
// several values for the same field into a single IN (...) membership test.
// Predicates that are not simple equalities carry Raw alone and are always
// emitted verbatim -- the "open" status expansion is the only one today.
//
// The zero value means "no predicate"; callers skip it.
type pred struct {
	Field string
	Value string
	Raw   string
}

// eqPred builds a collapsible `field = value` predicate. value must already be
// quoted by the caller where JQL requires it.
func eqPred(field, value string) pred {
	return pred{Field: field, Value: value, Raw: field + " = " + value}
}

// rawPred builds a predicate that must be emitted exactly as given and can
// never participate in an IN (...) collapse.
func rawPred(raw string) pred { return pred{Raw: raw} }

func (o *Ownership) predicate() pred {
	// "assigned to ..."
	if o.Assignment != nil {
		if o.Assignment.Self {
			return eqPred("assignee", "currentUser()")
		}
		owner := clean(strings.Join(o.Assignment.Owner, " "))
		if owner == "" {
			return pred{} // only fillers captured; skip the predicate
		}
		return eqPred("assignee", quoteIfNeeded(owner))
	}
	// "my" / "mine" / "myself"
	return eqPred("assignee", "currentUser()")
}

func (t *IssueType) predicate() pred {
	if v, ok := issueTypeJQL[strings.ToLower(t.Raw)]; ok {
		return eqPred("issuetype", v)
	}
	return eqPred("issuetype", quoteIfNeeded(t.Raw)) // defensive default
}

func (s *Status) predicate() pred {
	// "open" means "not one of the closed statuses", not a literal "Open"
	// status, so the fast path agrees with QuerySystemPrompt rule #6. It is a
	// NOT IN expansion rather than an equality, so it never collapses.
	if normaliseStatus(s.Raw) == "open" {
		return rawPred(openStatusJQL)
	}
	v, ok := statusJQL[normaliseStatus(s.Raw)]
	if !ok {
		v = s.Raw
	}
	return eqPred("status", quoteIfNeeded(v))
}

func (p *Project) predicate() pred {
	key := clean(strings.Join(p.Key, " "))
	if key == "" {
		return pred{} // "in the ..." with nothing after it
	}
	return eqPred("project", quoteIfNeeded(key))
}

// renderBucket renders every predicate collected for a single field.
//
// Two or more equality predicates for the same field collapse into one
// IN (...) membership test. AND-ing them instead -- which is what this used to
// do -- produces `issuetype = Bug AND issuetype = Story`: valid JQL that can
// never match anything, so "bugs and stories" silently returned zero rows with
// no indication the query was self-contradictory (FIELDCTO-2808).
//
// A single value still renders as `field = value`, so output for the common
// case is byte-identical to before. Non-equality predicates pass through
// untouched and hold their original position relative to the collapsed test.
func renderBucket(bucket []pred) []string {
	out := make([]string, 0, len(bucket))
	values := make([]string, 0, len(bucket))
	field, slot := "", -1

	for _, p := range bucket {
		if p.Field == "" {
			out = append(out, p.Raw)
			continue
		}
		if slot < 0 {
			slot = len(out)
			out = append(out, "") // reserved; filled once all values are known
		}
		field = p.Field
		values = append(values, p.Value)
	}

	switch {
	case slot < 0: // no equality predicates in this bucket
	case len(values) == 1:
		out[slot] = field + " = " + values[0]
	default:
		out[slot] = field + " IN (" + strings.Join(values, ", ") + ")"
	}
	return out
}

// ToJQL renders the AST as a JQL expression.
//
// Determinism guarantees, regardless of the order the user wrote the
// clauses in:
//
//   - predicates are always emitted in the fixed field order
//     project -> issuetype -> status -> assignee;
//   - exact duplicate predicates are removed ("my ... assigned to me");
//   - multiple distinct values for the same field collapse into a single
//     IN (...) membership test, so "bugs and stories" asks for either rather
//     than for both at once, which nothing can satisfy.
func (q *Query) ToJQL() (string, error) {
	if q == nil {
		return "", ErrNoConstraints
	}

	seen := map[string]struct{}{}
	add := func(bucket *[]pred, p pred) {
		if p.Raw == "" {
			return
		}
		if _, dup := seen[p.Raw]; dup {
			return
		}
		seen[p.Raw] = struct{}{}
		*bucket = append(*bucket, p)
	}

	var project, issueType, status, assignee []pred
	for _, clause := range q.Clauses {
		if clause == nil {
			continue
		}
		switch {
		case clause.Project != nil:
			add(&project, clause.Project.predicate())
		case clause.IssueType != nil:
			add(&issueType, clause.IssueType.predicate())
		case clause.Status != nil:
			add(&status, clause.Status.predicate())
		case clause.Ownership != nil:
			add(&assignee, clause.Ownership.predicate())
		case clause.Noise != nil:
			// pure noise: contributes nothing.
		}
	}

	parts := make([]string, 0, len(project)+len(issueType)+len(status)+len(assignee))
	// Fixed field order: rendering is independent of input clause order.
	for _, bucket := range [][]pred{project, issueType, status, assignee} {
		parts = append(parts, renderBucket(bucket)...)
	}
	if len(parts) == 0 {
		return "", ErrNoConstraints
	}
	return strings.Join(parts, " AND "), nil
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// jqlParser is instantiated exactly once. participle parsers are immutable
// after construction and safe for concurrent use, so Parse never rebuilds it.
var jqlParser = participle.MustBuild[Query](
	participle.Lexer(jqlLexer),
	participle.Elide("Whitespace", "Punct"),
)

// Parse converts a natural-language agile query to its JQL equivalent.
//
// It instantiates (via the package-level singleton) the participle parser,
// parses the input into the AST, and walks the AST with ToJQL. It returns an
// error wrapping participle errors for unparseable input, or
// ErrNoConstraints if nothing constraint-mapping was found.
func Parse(query string) (string, error) {
	// Normalise whitespace before parsing; the lexer treats any run of
	// \s+ inside phrases as a single separator anyway.
	input := strings.Join(strings.Fields(query), " ")
	if input == "" {
		return "", fmt.Errorf("nl2jql: %w", ErrNoConstraints)
	}

	ast, err := jqlParser.ParseString("", input)
	if err != nil {
		return "", fmt.Errorf("nl2jql: cannot parse %q: %w", query, err)
	}

	jql, err := ast.ToJQL()
	if err != nil {
		return "", fmt.Errorf("nl2jql: %q: %w", query, err)
	}
	return jql, nil
}
