package nlfeedback

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// ReplaySpec is deliberately separate from review text. A reviewer supplies
// assertions and a canned provider response; corrections are never prompts,
// generated plans, or commands. All JQL in this package is parsed locally only.
type ReplaySpec struct {
	Provider ProviderFixture `json:"provider"`
	Expect   Expectation     `json:"expect"`
}

// ProviderFixture replaces the provider boundary, including failures. There is
// no network-enabled mode and no fallback to ambient provider configuration.
type ProviderFixture struct {
	Response      string `json:"response,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
}

type Expectation struct {
	JQL             string `json:"jql,omitempty"`
	ErrorCategory   string `json:"error_category,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Route           string `json:"route,omitempty"`
	ProviderCalls   int    `json:"provider_calls"`
	NormalizedInput string `json:"normalized_input,omitempty"`
}

type Metric struct {
	Passed    int `json:"passed"`
	Evaluated int `json:"evaluated"`
}

type ReplayResult struct {
	ID            string           `json:"id"`
	Passed        bool             `json:"passed"`
	Unscored      bool             `json:"unscored,omitempty"`
	ErrorCategory string           `json:"error_category,omitempty"`
	Problem       string           `json:"problem,omitempty"`
	Translation   *llm.QueryResult `json:"translation,omitempty"`
	Checks        map[string]bool  `json:"checks"`
}

type ReplayReport struct {
	Total                 int               `json:"total"`
	Passed                int               `json:"passed"`
	Unscored              int               `json:"unscored"`
	TranslationFailures   int               `json:"translation_failures"`
	SemanticCorrect       int               `json:"semantic_correct"`
	Deterministic         int               `json:"deterministic_successes"`
	ProviderCalls         int               `json:"provider_calls"`
	DurationNanos         int64             `json:"duration_nanos"`
	AcceptanceRate        float64           `json:"acceptance_rate"`
	ExpectationPassRate   float64           `json:"expectation_pass_rate"`
	DeterministicCoverage float64           `json:"deterministic_coverage"`
	Metrics               map[string]Metric `json:"metrics"`
	Results               []ReplayResult    `json:"results"`
}

// Replay evaluates every attempt, including failures and unreviewed records.
// Acceptance means a successful translation matching all semantic constraints,
// divided by ALL records. An expected rejection can pass its assertions without
// being counted as a successful accepted translation.
func Replay(ctx context.Context, records []Record) ReplayReport {
	records = latestSnapshots(records)
	report := ReplayReport{Total: len(records), Metrics: map[string]Metric{}}
	for _, record := range records {
		result := replayOne(ctx, record)
		report.Results = append(report.Results, result)
		if result.Unscored {
			report.Unscored++
		}
		if result.Passed {
			report.Passed++
		}
		if result.ErrorCategory != "" {
			report.TranslationFailures++
		}
		if result.Checks["semantic"] {
			report.SemanticCorrect++
		}
		if tr := result.Translation; tr != nil {
			report.ProviderCalls += tr.ProviderCalls
			report.DurationNanos += tr.DurationNanos
			if result.ErrorCategory == "" && tr.JQL != "" && tr.Route == llm.RouteFastPath {
				report.Deterministic++
			}
		}
		for name, passed := range result.Checks {
			m := report.Metrics[name]
			m.Evaluated++
			if passed {
				m.Passed++
			}
			report.Metrics[name] = m
		}
	}
	if report.Total > 0 {
		report.AcceptanceRate = float64(report.SemanticCorrect) / float64(report.Total)
		report.ExpectationPassRate = float64(report.Passed) / float64(report.Total)
		report.DeterministicCoverage = float64(report.Deterministic) / float64(report.Total)
	}
	return report
}

func replayOne(ctx context.Context, record Record) ReplayResult {
	r := ReplayResult{ID: record.ID, Checks: map[string]bool{}}
	if record.Fixture == nil || record.Fixture.Now.IsZero() {
		r.Unscored, r.Problem = true, "missing reproducible clock/metadata fixture"
		return r
	}
	provider := &fixtureProvider{}
	if record.Replay != nil {
		provider.fixture = record.Replay.Provider
	}
	// Missing provider fixtures fail closed when fallback is attempted. Unscored
	// records still run deterministically, exposing failures and coverage.
	tr, err := llm.GenerateJQLWithFixture(ctx, &config.Config{
		JiraProject: record.DefaultProject, JiraUser: record.EffectiveUser,
	}, record.Input, *record.Fixture, provider)
	r.Translation = tr
	if err != nil {
		r.ErrorCategory = safeErrorCategory(err)
	}
	if record.Replay == nil {
		r.Unscored, r.Problem = true, "missing reviewed expectations"
		return r
	}
	expect := record.Replay.Expect
	if problem := validateExpectation(expect, record.Replay.Provider); problem != "" {
		r.Problem = problem
		return r
	}
	r.Checks["provider_calls"] = provider.calls == expect.ProviderCalls
	if expect.Route != "" {
		r.Checks["route"] = tr != nil && tr.Route == expect.Route
	}
	if expect.NormalizedInput != "" {
		r.Checks["normalized_input"] = tr != nil && tr.NormalizedInput == expect.NormalizedInput
	}
	if expect.ErrorCategory != "" {
		r.Checks["expected_failure"] = err != nil && r.ErrorCategory == expect.ErrorCategory
	} else {
		r.Checks["translation_success"] = err == nil && tr != nil
		r.Checks["mode"] = tr != nil && string(tr.Mode) == expect.Mode
		actual := ""
		if tr != nil && err == nil {
			actual = tr.JQL
		}
		checks := compareJQL(actual, expect.JQL, record.DefaultProject)
		for name, passed := range checks {
			r.Checks[name] = passed
		}
		r.Checks["semantic"] = r.Checks["semantic"] && r.Checks["mode"]
	}
	r.Passed = true
	for _, passed := range r.Checks {
		r.Passed = r.Passed && passed
	}
	return r
}

func validateExpectation(e Expectation, p ProviderFixture) string {
	if e.ProviderCalls < 0 || e.ProviderCalls > 1 {
		return "provider_calls must be zero or one"
	}
	if e.Route != "" && e.Route != llm.RouteFastPath && e.Route != llm.RouteFallback {
		return "unknown expected route"
	}
	if p.ErrorCategory != "" && (!knownCategory(p.ErrorCategory) || p.Response != "") {
		return "invalid provider error fixture"
	}
	if e.ErrorCategory != "" {
		if !knownCategory(e.ErrorCategory) || e.JQL != "" || e.Mode != "" {
			return "failure expectation must contain a known category and no JQL/mode"
		}
		return ""
	}
	q, err := jql.Parse(e.JQL)
	if err != nil || q.Where == nil {
		return "success expectation requires a nonempty parsed JQL predicate"
	}
	if e.Mode != "list" && e.Mode != "count" && e.Mode != "recommendation" {
		return "success expectation requires an explicit query mode"
	}
	return ""
}

type fixtureProvider struct {
	fixture ProviderFixture
	calls   int
}

func (p *fixtureProvider) Generate(ctx context.Context, _, _ string) (string, error) {
	p.calls++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.fixture.ErrorCategory != "" {
		return "", translationerror.New(translationerror.Kind(p.fixture.ErrorCategory), errors.New("fixture failure"))
	}
	if p.fixture.Response == "" {
		return "", translationerror.New(translationerror.Unsupported, errors.New("provider fixture unavailable"))
	}
	return p.fixture.Response, nil
}

func knownCategory(category string) bool {
	switch translationerror.Kind(category) {
	case translationerror.Unsupported, translationerror.InvalidOutput, translationerror.Authentication,
		translationerror.Connectivity, translationerror.Timeout, translationerror.Cancellation:
		return true
	}
	return false
}

func safeErrorCategory(err error) string {
	kind := string(translationerror.KindOf(err))
	if knownCategory(kind) {
		return kind
	}
	return "unknown"
}

// compareJQL compares parsed Boolean trees. It permits commutative term order,
// but deliberately does not guess tenant synonyms or algebraic equivalence.
// Projections give diagnostic measurements; full-tree equality is the semantic
// and constraint-completeness gate, so losing a predicate cannot pass replay.
func compareJQL(actual, expected, project string) map[string]bool {
	checks := map[string]bool{}
	a, ae := jql.Parse(actual)
	e, ee := jql.Parse(expected)
	valid := ae == nil && a.Where != nil && jql.Validate(actual, jql.Options{DefaultProject: project}) == nil
	checks["structural_validity"] = valid
	for _, name := range []string{"semantic", "constraint_completeness", "boolean_grouping", "ownership", "dates", "scope", "ordering"} {
		checks[name] = false
	}
	if !valid || ee != nil || e.Where == nil {
		return checks
	}
	checks["ordering"] = sortFingerprint(a.OrderBy) == sortFingerprint(e.OrderBy)
	checks["semantic"] = fingerprint(a.Where, nil, false) == fingerprint(e.Where, nil, false) && checks["ordering"]
	checks["constraint_completeness"] = checks["semantic"]
	checks["boolean_grouping"] = fingerprint(a.Where, nil, true) == fingerprint(e.Where, nil, true)
	for name, fields := range map[string][]string{
		"ownership": {"assignee", "reporter", "creator", "status"},
		"dates":     {"created", "updated", "resolved", "resolutiondate", "due", "duedate", "status"},
		"scope":     {"project", "key", "id"},
	} {
		keep := func(raw string) bool {
			field := clauseField(raw)
			for _, f := range fields {
				if field == f {
					// Ordinary status comparisons have no owner/date meaning.
					return f != "status" || strings.Contains(strings.ToUpper(raw), " CHANGED ") || strings.Contains(strings.ToUpper(raw), " WAS ")
				}
			}
			return false
		}
		checks[name] = fingerprint(a.Where, keep, false) == fingerprint(e.Where, keep, false)
	}
	return checks
}

func clauseField(raw string) string {
	// Parsed clause rendering always separates the field from its operator.
	if strings.HasPrefix(raw, `"`) || strings.HasPrefix(raw, `'`) {
		quote := raw[0]
		for i := 1; i < len(raw); i++ {
			if raw[i] == '\\' {
				i++
			} else if raw[i] == quote {
				return strings.ToLower(raw[1:i])
			}
		}
	}
	field, _, _ := strings.Cut(raw, " ")
	return strings.ToLower(field)
}

func fingerprint(expr jql.Expr, keep func(string) bool, shape bool) string {
	switch x := expr.(type) {
	case jql.Clause:
		if keep != nil && !keep(x.Raw) {
			return ""
		}
		if shape {
			return "atom:" + clauseField(x.Raw)
		}
		return "atom:" + x.Raw
	case jql.And:
		return groupFingerprint("and", x.Terms, keep, shape)
	case jql.Or:
		return groupFingerprint("or", x.Terms, keep, shape)
	case jql.Not:
		child := fingerprint(x.Term, keep, shape)
		if child != "" {
			return "not(" + child + ")"
		}
	}
	return ""
}

func groupFingerprint(op string, terms []jql.Expr, keep func(string) bool, shape bool) string {
	var children []string
	for _, term := range terms {
		if child := fingerprint(term, keep, shape); child != "" {
			children = append(children, child)
		}
	}
	if len(children) == 0 {
		return ""
	}
	if len(children) == 1 {
		return children[0]
	}
	sort.Strings(children)
	encoded, _ := json.Marshal(children)
	return op + string(encoded)
}

func sortFingerprint(sorts []jql.Sort) string {
	encoded, _ := json.Marshal(sorts)
	return string(encoded)
}
