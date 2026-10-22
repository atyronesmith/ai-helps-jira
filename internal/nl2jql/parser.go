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

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
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
	"currently", "still", "where",
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
		{Name: "ContainsWords", Pattern: `(?i)\b(?:that\s+)?contain\s+the\s+words\b`},
		{Name: "ChildOfEpic", Pattern: `(?i)\b(?:that\s+are\s+)?child\s+tasks\s+of\s+an\s+epic\b`},
		// The optional conjunction belongs to the ordering frame only here;
		// ordinary Boolean connectors remain separate tokens elsewhere.
		{Name: "OrderFrame", Pattern: `(?i)(?:\band\s+(?:ordered|sorted|order)\s+by\b|\b(?:ordered|sorted|order)\s+by\b|\bin\s+order\s+of\s+when\b|\bby\b)`},
		{Name: "PriorityEither", Pattern: `(?i)\bpriority\s+is\s+either\b`},
		{Name: "StatusIs", Pattern: `(?i)\bstatus\s+is\b`},
		{Name: "Labeled", Pattern: `(?i)\blabeled\b`},
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
		{Name: "Major", Pattern: `(?i)\bmajor\b`},
		{Name: "Minor", Pattern: `(?i)\bminor\b`},
		{Name: "High", Pattern: `(?i)\bhigh\b`},
		{Name: "Medium", Pattern: `(?i)\bmedium\b`},
		{Name: "Low", Pattern: `(?i)\blow\b`},
		{Name: "PriorityField", Pattern: `(?i)\bpriority\b`},
		{Name: "StoryPoints", Pattern: `(?i)\bstory[\s_-]+points?\b`},
		{Name: "Then", Pattern: `(?i)\bthen\b`},
		{Name: "ChronologicalDirection", Pattern: `(?i)\b(?:from\s+)?(?:oldest\s+to\s+newest|newest\s+to\s+oldest)\b`},
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
//	OrderBy   = OrderFrame (Word | Filler | PriorityField | StoryPoints | DateField)+ [ Direction ] .
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
	Group         *Query          `parser:"LParen @@ RParen"`
	Negation      *Clause         `parser:"| Not @@"`
	Connector     string          `parser:"| @And | @Or"`
	Date          *DateComparison `parser:"| @@"`
	Ownership     *Ownership      `parser:"| @@"`
	IssueType     *IssueType      `parser:"| @@"`
	Status        *Status         `parser:"| @@"`
	Priority      *Priority       `parser:"| @@"`
	StoryPoints   *StoryPoints    `parser:"| @@"`
	Labeled       *Labeled        `parser:"| @@"`
	StatusIs      *StatusIs       `parser:"| @@"`
	OrderBy       *OrderBy        `parser:"| @@"`
	Chronological string          `parser:"| @ChronologicalDirection"`
	TextSearch    *TextSearch     `parser:"| @@"`
	ChildOfEpic   bool            `parser:"| @ChildOfEpic"`
	Project       *Project        `parser:"| @@"`
	Noise         *Noise          `parser:"| @@"`
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

// Labeled -> labels = <value>. A bare "labeled X" is a fast-path label
// equality. The value is exactly one following word; a following ordering
// tail is a separate clause, so the capture stops at the first word.
type Labeled struct {
	Raw   string `parser:"@Labeled"`
	Value string `parser:"@Word"`
}

// StatusIs -> status = <value>. The explicit "status is X" frame pins a
// specific status value. The value is one following token; recognised status
// vocabulary (open/closed/done/in progress) is accepted alongside an arbitrary
// word, so "status is Open" parses as status = Open.
type StatusIs struct {
	Frame string   `parser:"@StatusIs"`
	Value []string `parser:"( @Word | @Open | @Closed | @Done | @InProgress )"`
}

// Priority accepts field/value pairs, bare values, and one value following the
// scoped "priority is either" frame. A following OR and second value remain
// ordinary same-field alternatives, which compile to IN.
type Priority struct {
	Either bool     `parser:"( @PriorityEither )?"`
	Tokens []string `parser:"( ( @Highest | @Lowest | @Critical | @Major | @Minor | @High | @Medium | @Low ) @PriorityField | @PriorityField ( @Highest | @Lowest | @Critical | @Major | @Minor | @High | @Medium | @Low ) | ( @Highest | @Lowest | @Critical | @Major | @Minor | @High | @Medium | @Low ) )"`
}

// OrderBy captures one field and an optional direction. An omitted direction
// defaults to DESC for numeric/duration fields, priority, and updated, otherwise
// ASC. Field aliases resolve through orderFieldJQL; arbitrary names are rejected.
type OrderBy struct {
	Frame     string      `parser:"@OrderFrame"`
	Field     []string    `parser:"( @Word | @Filler | @PriorityField | @StoryPoints | @DateField )+"`
	Direction string      `parser:"( @Direction | @ChronologicalDirection )?"`
	More      []*SortTerm `parser:"( ( Comma Then? | Then ) @@ )*"`
}

type SortTerm struct {
	Field     []string `parser:"( @Word | @Filler | @PriorityField | @StoryPoints | @DateField )+"`
	Direction string   `parser:"( @Direction | @ChronologicalDirection )?"`
}

// TextSearch captures a required list of literal terms after a recognized
// contains-the-words frame. OR and commas only separate terms inside this list.
type TextSearch struct {
	Frame string            `parser:"@ContainsWords"`
	Terms []*TextSearchTerm `parser:"@@ ( ( Comma Or? | Or ) @@ )*"`
}

type TextSearchTerm struct {
	Value string `parser:"@Email | @Word | @Filler | @DateField | @DateFunction | @DateLiteral | @RelativeDate | @InProgress | @AssignedTo | @My | @Me | @Not | @Highest | @Lowest | @Critical | @Major | @Minor | @High | @Medium | @Low | @PriorityField | @StoryPoints | @Then | @ChronologicalDirection | @Direction | @Open | @Closed | @Done | @Bugs | @Epics | @Tasks | @Stories | @In"`
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
	"major":    "Major",
	"minor":    "Minor",
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
	"story points":       `"Story Points"`,
	"summary text":       "summary",
	"original estimate":  "originalEstimate",
	"originalestimate":   "originalEstimate",
	"logged time":        "timespent",
	"time spent":         "timespent",
	"timespent":          "timespent",
	"remaining":          "remainingEstimate",
	"remaining estimate": "remainingEstimate",
	"amount remaining":   "remainingEstimate",
	"remainingestimate":  "remainingEstimate",
	"priority":           "priority",
	"created":            "created",
	"creation date":      "created",
	"date created":       "created",
	"updated":            "updated",
	"updated date":       "updated",
	"update date":        "updated",
	"last updated":       "updated",
	"due":                "due",
	"due date":           "due",
	"deadline":           "due",
	"resolved":           "resolved",
	"resolution date":    "resolved",
	"status":             "status",
	"assignee":           "assignee",
	"reporter":           "reporter",
	"key":                "key",
	"issue key":          "key",
	"work item key":      "key",
	"jira key":           "key",
	"summary":            "summary",
	"title":              "summary",
	"issuetype":          "issuetype",
	"issue type":         "issuetype",
	"work item type":     "issuetype",
	"type":               "issuetype",
	"rank":               "Rank",
	"ranking":            "Rank",
	"component":          "component",
	"components":         "component",
	"fix version":        "fixVersion",
	"release":            "fixVersion",
	"version":            "fixVersion",
	"labels":             "labels",
	"label":              "labels",
	"project":            "project",
}

func (o *OrderBy) sort() (jql.Sort, error) {
	field := strings.ToLower(clean(strings.Join(o.Field, " ")))
	jqlField, ok := orderFieldJQL[field]
	if !ok {
		return jql.Sort{}, fmt.Errorf("nl2jql: unsupported ORDER BY field %q", field)
	}
	direction := strings.ToLower(strings.Join(strings.Fields(o.Direction), " "))
	switch direction {
	case "":
		direction = "ASC"
		if jqlField == "priority" || jqlField == "updated" {
			direction = "DESC"
		} else if field == "story points" {
			direction = "DESC"
		} else if meta, found := fieldmeta.Core(jqlField); found && (meta.Type == "number" || meta.Type == "integer" || meta.Type == "duration") {
			direction = "DESC"
		}
	case "oldest to newest", "from oldest to newest", "newest to oldest", "from newest to oldest":
		meta, found := fieldmeta.Core(jqlField)
		if !found || meta.Type != "date" {
			return jql.Sort{}, fmt.Errorf("%w: chronological ordering requires a date field", ErrAmbiguousIntent)
		}
		if strings.Contains(direction, "oldest to newest") {
			direction = "ASC"
		} else {
			direction = "DESC"
		}
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

func (l *Labeled) predicate() pred {
	value := clean(l.Value)
	if value == "" {
		return pred{}
	}
	return eqPred("labels", quoteIfNeeded(value))
}

func (s *StatusIs) predicate() pred {
	value := clean(strings.Join(s.Value, " "))
	if value == "" {
		return pred{}
	}
	v, ok := statusJQL[normaliseStatus(value)]
	if !ok {
		v = value
	}
	return eqPred("status", quoteIfNeeded(v))
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
