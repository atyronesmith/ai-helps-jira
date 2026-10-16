// Package nl2jql converts a restricted subset of English agile questions
// into Jira Query Language (JQL).
//
// A closed participle grammar captures natural-language clauses and explicit
// Boolean groups. Query.Plan builds typed ownership/date/comparison intent;
// Compile constructs the shared JQL tree with terminal ordering. Unknown words
// remain errors, and ambiguous combinations require clarification.
package nl2jql

import (
	"errors"
	"fmt"
	"strings"
	"time"

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
	"need", "want", "please", "thank", "thanks", "how", "many",
	"count", "all", "every", "any", "the", "a", "an", "only", "just",
	"currently", "still",
	"issue", "issues", "ticket", "tickets", "item", "items", "jira", "jiras",
	"of", "for", "to",
	"that", "those", "these", "it", "them", "us", "you",
	"is", "are", "was", "were", "be", "been", "being",
	"have", "has", "had", "do", "does", "did",
	"on", "with", "related", "work",
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
		// Explicit email assignees are the one punctuation-bearing value the
		// deterministic grammar accepts. Keep this narrow; other punctuation
		// still follows the existing elision/rejection behavior.
		{Name: "Email", Pattern: `(?i)[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}`},
		{Name: "DateFunction", Pattern: `(?i)(?:startOfDay|startOfWeek|startOfMonth|endOfDay|endOfWeek|endOfMonth|now)\((?:"[+-]?[0-9]+")?\)`},
		{Name: "DateLiteral", Pattern: `"[0-9]{4}-[0-9]{2}-[0-9]{2}"`},
		{Name: "RelativeDate", Pattern: `-[0-9]+[dw]\b`},
		{Name: "And", Pattern: `(?i)\band\b`},
		{Name: "Or", Pattern: `(?i)\bor\b`},
		{Name: "Not", Pattern: `(?i)\bnot\b`},
		{Name: "LParen", Pattern: `\(`},
		{Name: "RParen", Pattern: `\)`},
		{Name: "Comma", Pattern: `,`},
		{Name: "DateField", Pattern: `(?i)\b(?:created|updated|resolved|due)\b`},
		{Name: "Number", Pattern: `(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)`},
		{Name: "Comparison", Pattern: `>=|<=|>|<|=`},
		{Name: "Punct", Pattern: `[.!?;:'’]+`},

		// Multi-word phrases win over their single-word prefixes.
		// "[-\s_]+" also accepts "in_progress" / "assigned-to".
		{Name: "InProgress", Pattern: `(?i)\bin[-\s_]+progress\b`},
		{Name: "AssignedTo", Pattern: `(?i)\bassigned[-\s_]+to\b`},

		// Ownership vocabulary.
		{Name: "My", Pattern: `(?i)\b(?:my|mine|myself)\b`},
		{Name: "Me", Pattern: `(?i)\bme\b`},

		// Long priority values precede their prefixes; word boundaries keep
		// Highest distinct from High and Lowest distinct from Low.
		{Name: "Highest", Pattern: `(?i)\bhighest\b`},
		{Name: "Lowest", Pattern: `(?i)\blowest\b`},
		{Name: "Critical", Pattern: `(?i)\bcritical\b`},
		{Name: "High", Pattern: `(?i)\bhigh\b`},
		{Name: "Medium", Pattern: `(?i)\bmedium\b`},
		{Name: "Low", Pattern: `(?i)\blow\b`},
		{Name: "PriorityField", Pattern: `(?i)\bpriority\b`},
		{Name: "StoryPoints", Pattern: `(?i)\bstory[\s_-]+points?\b`},
		{Name: "OrderFrame", Pattern: `(?i)\b(?:ordered|sorted|order)\s+by\b`},
		{Name: "Direction", Pattern: `(?i)\b(?:ascending|asc|descending|desc)\b`},

		// Status vocabulary.
		{Name: "Open", Pattern: `(?i)\bopen\b`},
		{Name: "Closed", Pattern: `(?i)\bclosed\b`},
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
//	Clause    = Ownership | IssueType | Status | Project | OrderBy | Noise .
//	Ownership = My | AssignedTo Assignment .
//	Assignment = Me | My | (Word | Filler)+ .
//	IssueType = Bugs | Epics | Tasks | Stories .
//	Status    = Open | Done | InProgress .
//	Project   = In (Word | Filler)+ .
//	OrderBy   = OrderFrame (Word | Filler | PriorityField | StoryPoints)+ Direction .
//	Noise     = Filler .
//
// Every alternative in Clause starts with a mutually disjoint token set, so
// the parser can dispatch with one token of lookahead and never backtracks.

// Query is the English AST: clauses, connectors and nested groups.
type Query struct {
	Clauses []*Clause `parser:"( @@ )*"`
}

// Clause maps to exactly one JQL predicate (or zero, for Noise).
type Clause struct {
	Group       *Query          `parser:"LParen @@ RParen"`
	Negation    *Clause         `parser:"| Not @@"`
	Connector   string          `parser:"| @And | @Or"`
	Date        *DateComparison `parser:"| @@"`
	Ownership   *Ownership      `parser:"| @@"`
	IssueType   *IssueType      `parser:"| @@"`
	Status      *Status         `parser:"| @@"`
	Priority    *Priority       `parser:"| @@"`
	StoryPoints *StoryPoints    `parser:"| @@"`
	OrderBy     *OrderBy        `parser:"| @@"`
	Project     *Project        `parser:"| @@"`
	Noise       *Noise          `parser:"| @@"`
}

// DateComparison binds a supported date field to a typed bound. Calendar
// windows are represented by two comparisons; they never enter the LLM as JQL prose.
type DateComparison struct {
	Field    string `parser:"@DateField"`
	Operator string `parser:"@Comparison"`
	Bound    string `parser:"( @DateFunction | @RelativeDate | @DateLiteral )"`
}

// StoryPoints is a numeric comparison against a named Jira field.
type StoryPoints struct {
	Field    string `parser:"@StoryPoints"`
	Operator string `parser:"@Comparison"`
	Value    string `parser:"@Number"`
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
	Owner []string `parser:"| ( @Email | @Word | @Filler )+"`
}

// IssueType -> issuetype = Bug | Epic | Task | Story.
type IssueType struct {
	Raw string `parser:"@Bugs | @Epics | @Tasks | @Stories"`
}

// Status -> statusCategory != Done | "In Progress" | Done.
type Status struct {
	Raw string `parser:"@Open | @Done | @Closed | @InProgress"`
}

// Priority accepts both "priority Critical" and "Critical priority".
type Priority struct {
	Tokens []string `parser:"( @Highest | @Lowest | @Critical | @High | @Medium | @Low ) @PriorityField | @PriorityField ( @Highest | @Lowest | @Critical | @High | @Medium | @Low )"`
}

// OrderBy captures one field and an explicit direction. Field aliases are
// resolved through orderFieldJQL below; arbitrary field names are rejected.
type OrderBy struct {
	Frame     string      `parser:"@OrderFrame"`
	Field     []string    `parser:"( @Word | @Filler | @PriorityField | @StoryPoints | @DateField )+"`
	Direction string      `parser:"@Direction"`
	More      []*SortTerm `parser:"( Comma @@ )*"`
}

type SortTerm struct {
	Field     []string `parser:"( @Word | @Filler | @PriorityField | @StoryPoints | @DateField )+"`
	Direction string   `parser:"@Direction"`
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
	Filler string `parser:"@Filler | @Me | @Comma"`
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

// statusJQL maps literal status vocabulary. Open work is a category intent.
var statusJQL = map[string]string{
	"done":        "Done",
	"closed":      "Closed",
	"in progress": "In Progress",
}

var priorityJQL = map[string]string{
	"critical": "Critical",
	"highest":  "Highest",
	"high":     "High",
	"medium":   "Medium",
	"low":      "Low",
	"lowest":   "Lowest",
}

// orderFieldJQL is intentionally conservative. It includes standard Jira
// fields and the explicitly supported Story Points/Rank aliases; arbitrary
// tenant-specific custom field names are excluded.
var orderFieldJQL = map[string]string{
	"story points":    `"Story Points"`,
	"priority":        "priority",
	"created":         "created",
	"creation date":   "created",
	"date created":    "created",
	"updated":         "updated",
	"update date":     "updated",
	"last updated":    "updated",
	"due":             "due",
	"due date":        "due",
	"deadline":        "due",
	"resolved":        "resolved",
	"resolution date": "resolved",
	"status":          "status",
	"assignee":        "assignee",
	"reporter":        "reporter",
	"key":             "key",
	"issue key":       "key",
	"work item key":   "key",
	"jira key":        "key",
	"summary":         "summary",
	"title":           "summary",
	"issuetype":       "issuetype",
	"issue type":      "issuetype",
	"work item type":  "issuetype",
	"type":            "issuetype",
	"rank":            "Rank",
	"ranking":         "Rank",
	"component":       "component",
	"components":      "component",
	"fix version":     "fixVersion",
	"release":         "fixVersion",
	"version":         "fixVersion",
	"labels":          "labels",
	"label":           "labels",
	"project":         "project",
}

func (o *OrderBy) sort() (jql.Sort, error) {
	field := strings.ToLower(clean(strings.Join(o.Field, " ")))
	jqlField, ok := orderFieldJQL[field]
	if !ok {
		return jql.Sort{}, fmt.Errorf("nl2jql: unsupported ORDER BY field %q", field)
	}
	direction := strings.ToLower(o.Direction)
	switch direction {
	case "ascending", "asc":
		direction = "ASC"
	case "descending", "desc":
		direction = "DESC"
	default:
		return jql.Sort{}, fmt.Errorf("nl2jql: unsupported ORDER BY direction %q", o.Direction)
	}
	return jql.Sort{Field: jqlField, Direction: direction}, nil
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

// pred captures a canonical equality operand before intent grouping.
type pred struct{ Field, Value string }

func eqPred(field, value string) pred { return pred{Field: field, Value: value} }

func (t *IssueType) predicate() pred {
	if v, ok := issueTypeJQL[strings.ToLower(t.Raw)]; ok {
		return eqPred("issuetype", v)
	}
	return eqPred("issuetype", quoteIfNeeded(t.Raw)) // defensive default
}

func (s *Status) predicate() pred {
	v, ok := statusJQL[normaliseStatus(s.Raw)]
	if !ok {
		v = s.Raw
	}
	return eqPred("status", quoteIfNeeded(v))
}

func (p *Priority) predicate() pred {
	for _, raw := range p.Tokens {
		if value, ok := priorityJQL[strings.ToLower(raw)]; ok {
			return eqPred("priority", quoteIfNeeded(value))
		}
	}
	return pred{}
}

func (p *Project) predicate() pred {
	key := clean(strings.Join(p.Key, " "))
	if key == "" {
		return pred{} // "in the ..." with nothing after it
	}
	return eqPred("project", quoteIfNeeded(key))
}

// ToJQL compiles the natural-language intent plan to the shared JQL tree.
func (q *Query) ToJQL() (string, error) {
	plan, err := q.Plan("currentUser()", time.Now())
	if err != nil {
		return "", err
	}
	tree, err := plan.Compile()
	if err != nil {
		return "", err
	}
	return tree.Render(), nil
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

	plan, err := ParsePlan(input, "currentUser()", time.Now())
	if err != nil {
		return "", err
	}
	tree, err := plan.Compile()
	if err != nil {
		return "", err
	}
	return tree.Render(), nil
}
