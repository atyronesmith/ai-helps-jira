// normalizer.go provides a lenient pre-processing layer that sits in front of
// the strict JQL grammar. It rewrites the two classes of messiness that strict
// grammars choke on:
//
//  1. Natural-language date phrases  -> strict JQL relative dates
//     "created in the last 3 days"   -> "created >= -3d"
//     "updated last Tuesday"         -> "updated >= -7d"   (clock-dependent)
//     "created today"                -> "created >= startOfDay()"
//     "resolved over the last 2 sprints" -> "resolved >= -28d"
//
//  2. Misspelled project names       -> canonical project keys
//     "in Spport"                    -> "in SUP"
//
// Design rules:
//   - Pass order matters: date phrases are rewritten FIRST, because both the
//     date prepositions ("in the last 3 days") and project phrases ("in
//     Support") start with "in".
//   - Nothing returns an error: anything the Normalizer does not recognise is
//     passed through untouched, so a later strict parser is free to reject it.
//   - Output is deterministic: fuzzy ties are broken alphabetically over the
//     project map keys.
//
// Standard integration with the grammar from the previous step:
//
//	n := nl2jql.NewNormalizer()
//	jql, err := nl2jql.Parse(n.Normalize(rawQuery, projectMap))
package nl2jql

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const (
	// defaultSprintLengthDays: the classic two-week agile sprint.
	defaultSprintLengthDays = 14
	// defaultMaxEditDistance: the base Levenshtein budget for fuzzy snapping.
	defaultMaxEditDistance = 2
	// minFuzzyRunes: candidates shorter than this are only exact-matched;
	// fuzzy matching three characters or less produces nonsense snaps.
	minFuzzyRunes = 3
	// approxDaysPerMonth: JQL relative dates have no month unit, so months
	// are reduced to days.
	approxDaysPerMonth = 30
	// daysPerWeek: Go's time package has no week duration constant.
	daysPerWeek = 7
)

// Normalizer rewrites messy human input into grammar-friendly text.
// The zero value is valid: unset fields fall back to the defaults above.
type Normalizer struct {
	// SprintLengthDays is how many days one sprint spans (<= 0 -> 14).
	SprintLengthDays int

	// MaxEditDistance is the maximum Levenshtein distance, in runes, between
	// a typed project name and a known key (<= 0 -> 2). The effective budget
	// is additionally capped at half the candidate's length.
	MaxEditDistance int

	// Now supplies the clock for weekday-relative phrases ("last Tuesday").
	// Inject it in tests. nil -> time.Now.
	Now func() time.Time
}

// NewNormalizer returns a Normalizer configured with sensible defaults.
func NewNormalizer() *Normalizer {
	return &Normalizer{
		SprintLengthDays: defaultSprintLengthDays,
		MaxEditDistance:  defaultMaxEditDistance,
		Now:              time.Now,
	}
}

// Nil-safe accessors let callers use &Normalizer{} or even a nil detector.
func (n *Normalizer) sprintLen() int {
	if n != nil && n.SprintLengthDays > 0 {
		return n.SprintLengthDays
	}
	return defaultSprintLengthDays
}

func (n *Normalizer) maxEdit() int {
	if n != nil && n.MaxEditDistance > 0 {
		return n.MaxEditDistance
	}
	return defaultMaxEditDistance
}

func (n *Normalizer) clock() func() time.Time {
	if n != nil && n.Now != nil {
		return n.Now
	}
	return time.Now
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// Normalize rewrites the input and returns the normalised query. It never
// fails; unrecognisable fragments pass through unchanged so that the strict
// parser downstream can report them precisely.
func (n *Normalizer) Normalize(input string, projectMap map[string]string) string {
	// Collapse whitespace runs: every downstream regex assumes single spaces.
	s := strings.Join(strings.Fields(input), " ")
	s = normalizeRecommendationQuestion(s)
	s = normalizeDueThisWeekQuestion(s)

	// Dates first: "in the last 3 days" must be consumed before the project
	// pass starts matching "in <something>".
	s = n.normalizeDates(s)
	s = n.normalizeProjects(s, projectMap)
	s = normalizeOpenStatusPhrases(s)
	s = normalizeFirstPersonOwnership(s)
	return s
}

// normalizeDueThisWeekQuestion handles the narrowly defined first-person
// request for Jira assigned to the caller and due in the current week.
// Rewriting the whole anchored question preserves that intent without making
// generic words such as "due" or "this week" semantically ambiguous.
var dueThisWeekQuestion = regexp.MustCompile(`(?i)^list jiras? that are due this week[?!.]*$`)

func normalizeDueThisWeekQuestion(s string) string {
	if dueThisWeekQuestion.MatchString(s) {
		return "my dueweek"
	}
	return s
}

// recommendationQuestion removes one tightly-scoped recommendation frame
// while keeping its explicit Jira filter. The recommendation itself belongs
// to the caller after Jira data is retrieved; the query should retrieve only
// the requested open work. Keep this anchored so other semantic wording is
// never discarded as generic filler.
var recommendationQuestion = regexp.MustCompile(`(?i)^which of (my) (open) ((?:jira )?issues|jiras?) should i work on next[?!.]*$`)

func normalizeRecommendationQuestion(s string) string {
	if m := recommendationQuestion.FindStringSubmatch(s); m != nil {
		return strings.Join([]string{m[1], m[2], m[3]}, " ")
	}
	return s
}

// normalizeFirstPersonOwnership maps possessive questions about the caller's
// work to the parser's existing "my" ownership clause. The intervening
// count-question words remain harmless filler for the grammar.
var firstPersonOwnership = regexp.MustCompile(`(?i)\b(?:do\s+)?i\s+have\b`)

func normalizeFirstPersonOwnership(s string) string {
	return firstPersonOwnership.ReplaceAllString(s, "my")
}

// openStatusPhrases maps common ways of asking for unfinished Jira work to
// the parser's canonical "open" token. In particular, a negative terminal
// status is broader than a list of status names: statusCategory != Done also
// excludes tenant-specific terminal statuses such as Canceled.
var openStatusPhrases = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\banything\s+still\s+open\b`),
	regexp.MustCompile(`(?i)\bunfinished\s+work\b`),
	regexp.MustCompile(`(?i)\bactive\s+issues\b`),
	regexp.MustCompile(`(?i)\bnot\s+(?:done|closed|resolved|cancell?ed)(?:\s+or\s+(?:done|closed|resolved|cancell?ed))*\b`),
}

func normalizeOpenStatusPhrases(s string) string {
	for _, phrase := range openStatusPhrases {
		s = phrase.ReplaceAllString(s, "open")
	}
	return s
}

// ---------------------------------------------------------------------------
// Pass 1: date phrases -> relative JQL dates
// ---------------------------------------------------------------------------

// dateFieldAlts are the timestamp-ish fields we bind date phrases to. The
// alternatives are ordered longest-first so "due date" beats bare "due".
const dateFieldAlts = `created|updated|resolved|due\s*date|due`

var (
	// "created in the last 3 days", "resolved over the past 2 sprints",
	// "updated within the last couple of weeks", ...
	relRangeRe = regexp.MustCompile(
		`(?i)\b(?P<field>` + dateFieldAlts + `)\b[,\s]+` +
			`(?:was[,\s]+|is[,\s]+|has[,\s]+been[,\s]+)?` +
			`(?:in[,\s]+|within[,\s]+|over[,\s]+|during[,\s]+|from[,\s]+|for[,\s]+)?` +
			`(?:the[,\s]+)?(?:last|past)[,\s]+` +
			`(?P<count>\d+|several|(?:(?:a|an)[,\s]+)?(?:couple|few)(?:[,\s]+of)?)[,\s]+` +
			`(?P<unit>sprints?|days?|weeks?|months?)\b`)

	// "created last Tuesday", "updated on last friday", ...
	relWeekdayRe = regexp.MustCompile(
		`(?i)\b(?P<field>` + dateFieldAlts + `)\b[,\s]+` +
			`(?:was[,\s]+|is[,\s]+)?(?:on[,\s]+)?` +
			`last[,\s]+(?P<day>monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)

	// "created today", "updated yesterday", "created this week", ...
	relSimpleRe = regexp.MustCompile(
		`(?i)\b(?P<field>` + dateFieldAlts + `)\b[,\s]+` +
			`(?:was[,\s]+|is[,\s]+)?(?:on[,\s]+)?` +
			`(?P<when>today|yesterday|this\s+week|last\s+week)\b`)
)

// weekdayByName validates weekday tokens against the stdlib calendar.
var weekdayByName = map[string]time.Weekday{
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
	"sunday":    time.Sunday,
}

// normalizeDates rewrites every recognised date phrase as
// "<field> >= <relative-date>".
func (n *Normalizer) normalizeDates(s string) string {
	sprintLen := n.sprintLen()
	now := n.clock()()

	// 1. Counted ranges: collapse to flat day offsets.
	s = rewriteMatches(relRangeRe, s, func(g map[string]string) (string, bool) {
		count := parseCount(g["count"])
		days := unitToDays(g["unit"], sprintLen)
		if count <= 0 || days <= 0 {
			return "", false // unknown count/unit: leave the text alone
		}
		return fmt.Sprintf("%s >= -%dd", jqlField(g["field"]), count*days), true
	})

	// 2. "last <weekday>": count back to the most recent previous
	//    occurrence of that weekday. Said on the weekday itself, "last
	//    Tuesday" points exactly one week back (=> -7d).
	s = rewriteMatches(relWeekdayRe, s, func(g map[string]string) (string, bool) {
		wd, ok := weekdayByName[strings.ToLower(g["day"])]
		if !ok {
			return "", false
		}
		return fmt.Sprintf("%s >= -%dd", jqlField(g["field"]), daysSinceWeekday(now, wd)), true
	})

	// 3. Fixed phrases.
	s = rewriteMatches(relSimpleRe, s, func(g map[string]string) (string, bool) {
		field := jqlField(g["field"])
		switch strings.ToLower(strings.Join(strings.Fields(g["when"]), " ")) {
		case "today":
			return fmt.Sprintf("%s >= startOfDay()", field), true
		case "yesterday":
			return fmt.Sprintf("%s >= -1d", field), true
		case "this week":
			return fmt.Sprintf("%s >= startOfWeek()", field), true
		case "last week":
			return fmt.Sprintf("%s >= -7d", field), true
		}
		return "", false
	})

	return s
}

// daysSinceWeekday returns how many days to subtract from now to reach the
// most recent previous occurrence of wd (1..7). On the weekday itself it
// returns 7.
func daysSinceWeekday(now time.Time, wd time.Weekday) int {
	d := int(now.Weekday()) - int(wd)
	if d <= 0 {
		d += 7
	}
	return d
}

// parseCount understands digits plus the common vague counters.
func parseCount(s string) int {
	t := strings.ToLower(strings.Join(strings.Fields(s), " "))
	if i, err := strconv.Atoi(t); err == nil {
		return i
	}
	if strings.Contains(t, "couple") {
		return 2
	}
	if strings.Contains(t, "few") || strings.Contains(t, "several") {
		return 3
	}
	return 0
}

// unitToDays reduces plural units to a per-unit day count. JQL relative dates
// only know d/w, so everything is expressed in days for a uniform output.
func unitToDays(unit string, sprintLen int) int {
	switch strings.ToLower(strings.TrimSuffix(unit, "s")) {
	case "day":
		return 1
	case "week":
		return daysPerWeek
	case "sprint":
		return sprintLen
	case "month":
		return approxDaysPerMonth
	}
	return 0
}

// jqlField canonicalises the free-text field name. "due" / "due date" both
// map to Jira's canonical JQL field; dueDate is an accepted Jira alias.
func jqlField(raw string) string {
	switch normalizeName(raw) {
	case "due", "due date", "duedate":
		return "due"
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// rewriteMatches replaces each regex match for which f reports ok; all other
// text, including non-matching spans, is preserved verbatim.
func rewriteMatches(re *regexp.Regexp, s string, f func(groups map[string]string) (string, bool)) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	names := re.SubexpNames()
	var b strings.Builder
	last := 0
	for _, m := range matches {
		groups := make(map[string]string, len(names))
		for g := 1; g < len(names); g++ {
			if start := m[2*g]; start >= 0 {
				groups[names[g]] = s[start:m[2*g+1]]
			}
		}
		repl, ok := f(groups)
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(repl)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// ---------------------------------------------------------------------------
// Pass 2: fuzzy project name snapping
// ---------------------------------------------------------------------------

// projectIntroRe locates candidate project mentions after "in|from|for" with
// an optional "project" keyword, plus up to two trailing word tokens so that
// multi-word keys ("Eng Platform") can be tried first.
var projectIntroRe = regexp.MustCompile(
	`(?i)\b(?:in|from|for)\s+(?:project\s+)?` +
		`(?P<name>[A-Za-z][A-Za-z0-9_\-]*)(?:\s+[A-Za-z][A-Za-z0-9_\-]*){0,2}`)

// stopWords are words that can syntactically follow "in" but are never
// project names: statuses ("in progress", "in review") and idioms ("in
// general"). Any candidate containing one is rejected outright.
var stopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "my": {}, "me": {}, "mine": {},
	"all": {}, "any": {}, "some": {}, "no": {}, "not": {},
	"last": {}, "next": {}, "this": {}, "that": {}, "these": {}, "those": {},
	"progress": {}, "review": {}, "testing": {}, "qa": {},
	"done": {}, "open": {}, "closed": {}, "todo": {}, "backlog": {}, "blocked": {},
	"general": {}, "particular": {}, "addition": {}, "front": {}, "order": {},
	"case": {}, "terms": {}, "favor": {}, "favour": {}, "place": {},
}

// normalizeProjects snaps misspelled project mentions onto known keys.
func (n *Normalizer) normalizeProjects(s string, projects map[string]string) string {
	if len(projects) == 0 {
		return s
	}
	maxEd := n.maxEdit()
	nameIdx := projectIntroRe.SubexpIndex("name")
	matches := projectIntroRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}

	var b strings.Builder
	last := 0
	for _, m := range matches {
		nameStart, _ := m[2*nameIdx], m[2*nameIdx+1]
		if nameStart < 0 {
			continue
		}
		matchEnd := m[1]
		var (
			replKey string
			replEnd int
			found   bool
		)
		// Try the widest candidate ("Spport created"), then progressively
		// drop trailing tokens ("Spport") until something snaps. This keeps
		// multi-word keys working without eating surrounding verbs.
		for cand := s[nameStart:matchEnd]; cand != ""; cand = dropLastToken(cand) {
			if key, ok := snapProject(cand, projects, maxEd); ok {
				replKey, replEnd, found = key, nameStart+len(cand), true
				break
			}
		}
		if !found {
			continue
		}
		b.WriteString(s[last:nameStart])
		b.WriteString(replKey)
		last = replEnd
	}
	b.WriteString(s[last:])
	return b.String()
}

// snapProject resolves a free-text candidate to a project key. Comparison is
// case-insensitive and whitespace-collapsed. Exact matches win at any length;
// typo matches must fit within maxEdit and never exceed half the candidate's
// rune length. Returns _, false when nothing is confident.
func snapProject(candidate string, projects map[string]string, maxEdit int) (string, bool) {
	cand := normalizeName(candidate)
	if cand == "" {
		return "", false
	}
	for _, tok := range strings.Fields(cand) {
		if _, stop := stopWords[tok]; stop {
			return "", false // never snap statuses / boilerplate phrases
		}
	}
	keys := make([]string, 0, len(projects))
	for k := range projects {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic tie-breaking across map iterations

	for _, k := range keys {
		if normalizeName(k) == cand {
			return projects[k], true
		}
	}
	candRunes := len([]rune(cand))
	if candRunes < minFuzzyRunes {
		return "", false
	}
	limit := maxEdit
	if half := candRunes / 2; half < limit {
		limit = half
	}
	best, bestDist := "", maxEdit+1
	for _, k := range keys {
		if d := levenshtein(cand, normalizeName(k)); d < bestDist {
			best, bestDist = k, d
		}
	}
	if bestDist <= limit {
		return projects[best], true
	}
	return "", false
}

// normalizeName lowercases and collapses internal whitespace for comparison.
func normalizeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// dropLastToken strips the trailing whitespace-delimited token; "" when none.
func dropLastToken(s string) string {
	i := strings.LastIndexAny(s, " \t")
	if i <= 0 {
		return ""
	}
	return strings.TrimRight(s[:i], " \t")
}

// levenshtein is the classic two-row dynamic programme, rune-based so
// comparisons stay correct outside ASCII.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = minInt(cur[j-1]+1, minInt(prev[j]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
