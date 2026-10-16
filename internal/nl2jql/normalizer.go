// The normalizer repairs project aliases and maps recognized calendar phrases
// to grammar tokens. Date comparisons become typed intent nodes in ParsePlan;
// unsupported/vague durations are never converted to approximate day counts.
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
	// defaultMaxEditDistance: the base Levenshtein budget for fuzzy snapping.
	defaultMaxEditDistance = 2
	// minFuzzyRunes: candidates shorter than this are only exact-matched;
	// fuzzy matching three characters or less produces nonsense snaps.
	minFuzzyRunes = 3
	// daysPerWeek: Go's time package has no week duration constant.
	daysPerWeek = 7
)

// Normalizer rewrites messy human input into grammar-friendly text.
// The zero value is valid: unset fields fall back to the defaults above.
type Normalizer struct {
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
		MaxEditDistance: defaultMaxEditDistance,
		Now:             time.Now,
	}
}

// Nil-safe accessors let callers use &Normalizer{} or even a nil detector.
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

	// Dates first: "in the last 3 days" must be consumed before the project
	// pass starts matching "in <something>".
	s = n.normalizeDates(s)
	s = n.normalizeProjects(s, projectMap)
	s = normalizeOpenStatusPhrases(s)
	s = normalizeFirstPersonOwnership(s)
	return s
}

// Recommendation framing records a query mode, while retaining every filter
// within the frame. The host assistant owns recommendations over retrieved data.
var recommendationFrame = regexp.MustCompile(`(?i)^which of (.+) should i work on next[?!.]*$`)

func normalizeRecommendationQuestion(s string) string {
	if m := recommendationFrame.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}
func queryMode(s string) QueryMode {
	lower := strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(lower, "how many ") || strings.HasPrefix(lower, "count ") {
		return ModeCount
	}
	if recommendationFrame.MatchString(s) {
		return ModeRecommendation
	}
	return ModeList
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
	regexp.MustCompile(`(?i)\bnot\s+(?:done|closed|resolved|cancell?ed)\b`),
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
	// Explicit rolling durations in days/weeks; vague counts, months and
	// real sprint periods require clarification instead of approximation.
	relRangeRe = regexp.MustCompile(
		`(?i)\b(?P<field>` + dateFieldAlts + `)\b[,\s]+` +
			`(?:was[,\s]+|is[,\s]+|has[,\s]+been[,\s]+)?` +
			`(?:in[,\s]+|within[,\s]+|over[,\s]+|during[,\s]+|from[,\s]+|for[,\s]+)?` +
			`(?:the[,\s]+)?(?:last|past)[,\s]+` +
			`(?P<count>\d+)[,\s]+` +
			`(?P<unit>days?|weeks?)\b`)

	// "created last Tuesday", "updated on last friday", ...
	relWeekdayRe = regexp.MustCompile(
		`(?i)\b(?P<field>` + dateFieldAlts + `)\b[,\s]+` +
			`(?:was[,\s]+|is[,\s]+)?(?:on[,\s]+)?` +
			`last[,\s]+(?P<day>monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)

	// "created today", "updated yesterday", "created this week", ...
	relSimpleRe = regexp.MustCompile(
		`(?i)\b(?P<field>` + dateFieldAlts + `)\b[,\s]+` +
			`(?:was[,\s]+|is[,\s]+)?(?:on[,\s]+)?` +
			`(?P<when>today|yesterday|this\s+week|last\s+week|this\s+month|last\s+month)\b`)
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
	now := n.clock()()

	// 1. Counted ranges: collapse to flat day offsets.
	s = rewriteMatches(relRangeRe, s, func(g map[string]string) (string, bool) {
		count, err := strconv.Atoi(g["count"])
		if err != nil || count > 100000 {
			return "", false
		}
		days := 1
		if strings.HasPrefix(strings.ToLower(g["unit"]), "week") {
			days = 7
		}
		if count <= 0 || days <= 0 {
			return "", false // unknown count/unit: leave the text alone
		}
		return fmt.Sprintf("%s >= -%dd AND %s <= now()", jqlField(g["field"]), count*days, jqlField(g["field"])), true
	})

	// 2. "last <weekday>": count back to the most recent previous
	//    occurrence of that weekday. Said on the weekday itself, "last
	//    Tuesday" points exactly one week back (=> -7d).
	s = rewriteMatches(relWeekdayRe, s, func(g map[string]string) (string, bool) {
		wd, ok := weekdayByName[strings.ToLower(g["day"])]
		if !ok {
			return "", false
		}
		days := daysSinceWeekday(now, wd)
		return fmt.Sprintf(`%s >= startOfDay("-%d") AND %s < startOfDay("-%d")`, jqlField(g["field"]), days, jqlField(g["field"]), days-1), true
	})

	// 3. Fixed phrases.
	s = rewriteMatches(relSimpleRe, s, func(g map[string]string) (string, bool) {
		field := jqlField(g["field"])
		switch strings.ToLower(strings.Join(strings.Fields(g["when"]), " ")) {
		case "today":
			return fmt.Sprintf(`%s >= startOfDay() AND %s < startOfDay("+1")`, field, field), true
		case "yesterday":
			return fmt.Sprintf(`%s >= startOfDay("-1") AND %s < startOfDay()`, field, field), true
		case "this week":
			return fmt.Sprintf(`%s >= startOfWeek() AND %s < startOfWeek("+1")`, field, field), true
		case "this month":
			return fmt.Sprintf(`%s >= startOfMonth() AND %s < startOfMonth("+1")`, field, field), true
		case "last month":
			return fmt.Sprintf(`%s >= startOfMonth("-1") AND %s < startOfMonth()`, field, field), true
		case "last week":
			return fmt.Sprintf(`%s >= startOfWeek("-1") AND %s < startOfWeek()`, field, field), true
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
