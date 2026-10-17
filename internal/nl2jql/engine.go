// engine.go orchestrates the translation pipeline for the nl2jql package:
//
//	┌──────────────────────────┐    ok      ┌────────────┐
//	│ Normalizer.Normalize     │──────────▶ │ Parse      │──────▶ JQL  (fast path)
//	│ date + fuzzy-name repair │            │ strict AST │
//	└──────────┬───────────────┘            └─────┬──────┘
//	           │                                  │ error
//	           ▼                                  ▼
//	                     FallbackEngine.Generate (LLM) ─▶ JQL  (slow path)
//
// A Translator is immutable after construction and safe for concurrent use.
package nl2jql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	jqlast "github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// Sentinel / internal errors. (ErrEmptyQuery is declared in fallback.go; it
// is reused here so callers have one symbol for "blank input".)
var (
	// errUninitialized guards against using a zero-valued Translator.
	errUninitialized = errors.New("nl2jql: Translator is uninitialized; construct it with NewTranslator")
	// errMissingFallback is defensive only: NewTranslator rejects a nil
	// fallback, so this can occur solely via a hand-built struct literal.
	errMissingFallback = errors.New("nl2jql: fallback engine is missing")
)

// Translator pipes a natural-language query through the deterministic-first
// translation strategy: normalize -> strict parse -> LLM fallback.
type Translator struct {
	normalizer *Normalizer
	fallback   *FallbackEngine

	// projectMap is the normalizer vocabulary: human/typo project name ->
	// canonical key, e.g. {"Support": "SUP"}.
	projectMap map[string]string

	// schema is embedded into the fallback system prompt: canonical
	// field/tenant vocabulary for the LLM.
	schema map[string]string

	self     string
	resolver *fieldmeta.Resolver
	project  string
	log      *slog.Logger
}

// Options carries deployment configuration for NewTranslator. Zero value is
// valid for a pure fast/fallback setup without tenant vocabulary.
type Options struct {
	// Self is the configured effective-user JQL operand; empty means currentUser().
	Self         string
	Fields       []fieldmeta.Field
	FieldAliases map[string]string
	Project      string
	// ProjectMap is handed to Normalizer.Normalize on every call.
	ProjectMap map[string]string

	// Schema is handed to FallbackEngine.Generate on every call.
	Schema map[string]string

	// Logger receives routing events ("fast path used", "routing to
	// fallback"). nil -> slog.Default().
	Logger *slog.Logger
}

// NewTranslator wires the pipeline. normalizer may be nil (a default
// NewNormalizer is used); fallback must not be nil, because a deterministic
// rejection with no fallback is unrecoverable.
func NewTranslator(normalizer *Normalizer, fallback *FallbackEngine, opts Options) (*Translator, error) {
	if normalizer == nil {
		normalizer = NewNormalizer()
	}
	if fallback == nil {
		return nil, errors.New("nl2jql: fallback engine must not be nil")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Translator{
		normalizer: normalizer,
		self:       opts.Self,
		resolver:   fieldmeta.New(opts.Fields, opts.FieldAliases),
		project:    opts.Project,
		fallback:   fallback,
		// Defensive copies: the pipeline must not observe external mutation.
		projectMap: copyStringMap(opts.ProjectMap),
		schema:     copyStringMap(opts.Schema),
		log:        logger,
	}, nil
}

// Route labels identify which translation tier produced a Result, so logs and the
// web dashboard can tell the tiers apart. nl2jql is the leaf package that owns
// Result.Route and has no dependency on llm, so it declares the route vocabulary here as the single source of truth;
// the llm package re-exports these routes so consumers import the labels from one
// place and the two packages cannot drift.
const (
	// RouteFastPath and RouteFallback are stamped by this engine's Translate.
	RouteFastPath = "fast_path" // deterministic parser handled the query (no LLM call)
	RouteFallback = "fallback"  // typed LLM intent compiled locally to JQL
)

// Result is the outcome of a translation: the generated JQL and the Route label
// that produced it -- RouteFastPath for the deterministic parser, RouteFallback
// for the LLM fallback.
type Result struct {
	Input           string
	NormalizedInput string
	ParseError      string
	JQL             string
	Route           string
	Mode            QueryMode
}

// Translate converts a natural-language Jira question into JQL.
//
// It follows the mandated routing order:
//  1. Normalizer.Normalize — repair messy dates/project names;
//  2. Parse (fast path) — deterministic grammar. On success the JQL is
//     returned and the fast path is logged;
//  3. on parse error the failure is caught and logged, and
//     FallbackEngine.Generate compiles the query via the LLM.
func (t *Translator) Translate(ctx context.Context, query string) (Result, error) {
	const op = "nl2jql: translate: "

	if t == nil || t.normalizer == nil {
		return Result{}, fmt.Errorf("%s%w", op, errUninitialized)
	}
	log := t.logger()

	q := strings.TrimSpace(query)
	if q == "" {
		return Result{}, fmt.Errorf("%s%w", op, ErrEmptyQuery)
	}
	// Fail fast before doing any work on an already-dead context.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// ------------------------------------------------------------------
	// 1) Normalization. Normalize never errors; unknown fragments pass
	//    through untouched and are somebody else's problem.
	// ------------------------------------------------------------------
	if err := checkAmbiguity(q); err != nil {
		return Result{Input: query, NormalizedInput: q}, translationerror.New(translationerror.Unsupported, err)
	}
	normalized := t.normalizer.Normalize(q, t.projectMap)

	// ------------------------------------------------------------------
	// 2) Fast path: the strict deterministic parser.
	// ------------------------------------------------------------------
	plan, parseErr := ParsePlan(normalized, t.self, t.normalizer.clock()())
	var jql string
	if parseErr == nil {
		plan.Mode = queryMode(q)
		err := resolvePlanFields(plan, t.resolver, t.project)
		var tree *jqlast.Query
		if err == nil {
			tree, err = plan.Compile()
		}
		if err != nil {
			return Result{Input: query, NormalizedInput: normalized}, translationerror.New(translationerror.Unsupported, err)
		}
		parseErr = err
		if err == nil {
			jql = tree.Render()
		}
	}
	if errors.Is(parseErr, ErrAmbiguousIntent) {
		return Result{Input: query, NormalizedInput: normalized, ParseError: parseErr.Error()}, translationerror.New(translationerror.Unsupported, parseErr)
	}
	if parseErr == nil {
		log.Info("jql translation: fast path used (deterministic parser)",
			slog.String("route", RouteFastPath),
			slog.String("query", q),
			slog.String("normalized", normalized),
			slog.String("jql", jql),
		)
		return Result{Input: query, NormalizedInput: normalized, JQL: jql, Route: RouteFastPath, Mode: queryMode(q)}, nil
	}

	// ------------------------------------------------------------------
	// 3) Slow path: catch the parse error, log it, route to the LLM.
	// ------------------------------------------------------------------
	log.Warn("jql translation: deterministic parser rejected the query; routing to LLM fallback",
		slog.String("route", RouteFallback),
		slog.String("query", q),
		slog.String("normalized", normalized),
		slog.Any("parse_error", parseErr),
	)

	if t.fallback == nil {
		return Result{}, fmt.Errorf("%s%w: deterministic parse failed: %v", op, errMissingFallback, parseErr)
	}

	jql, err := t.fallback.generate(ctx, normalized, t.schema, t.resolver, t.project, t.self)
	if err != nil {
		// Preserve causality: the caller can still see ErrUnmappable etc.
		// via errors.Is on the wrapped error.
		return Result{Input: query, NormalizedInput: normalized, ParseError: parseErr.Error()}, fmt.Errorf("%sfallback engine failed: %w", op, err)
	}

	log.Info("jql translation: fallback path used (LLM)",
		slog.String("route", RouteFallback),
		slog.String("query", q),
		slog.String("jql", jql),
	)
	return Result{Input: query, NormalizedInput: normalized, ParseError: parseErr.Error(), JQL: jql, Route: RouteFallback, Mode: queryMode(q)}, nil
}

// logger is nil-safe so even a zero-valued Translator degrades gracefully.
func (t *Translator) logger() *slog.Logger {
	if t == nil || t.log == nil {
		return slog.Default()
	}
	return t.log
}

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

var ambiguousPeriod = regexp.MustCompile(`(?i)\b(?:last|past)\s+(?:[0-9]+\s+(?:months?|sprints?)|(?:a |an )?(?:few|several|couple)(?: of)?\s+(?:days?|weeks?|months?|sprints?))\b`)

var ambiguousNegation = regexp.MustCompile(`(?i)\bnot\s+(?:done|closed|resolved|cancell?ed)\s+or\s+(?:done|closed|resolved|cancell?ed)\b`)
var ambiguousTense = regexp.MustCompile(`(?i)\b(?:was|were|previously|had)\b`)

func checkAmbiguity(q string) error {
	for _, match := range relRangeRe.FindAllStringSubmatch(q, -1) {
		count, err := strconv.Atoi(match[relRangeRe.SubexpIndex("count")])
		if err != nil || count <= 0 || count > 100000 {
			return fmt.Errorf("%w: use a positive bounded day/week duration", ErrAmbiguousIntent)
		}
	}
	if allProjects.MatchString(q) {
		return fmt.Errorf("%w: name the projects to include explicitly", ErrAmbiguousIntent)
	}

	if ambiguousPeriod.MatchString(q) {
		return fmt.Errorf("%w: use an exact day/week duration or a named calendar period", ErrAmbiguousIntent)
	}
	if ambiguousNegation.MatchString(q) {
		return fmt.Errorf("%w: group negative status alternatives explicitly", ErrAmbiguousIntent)
	}
	if ambiguousTense.MatchString(q) {
		return fmt.Errorf("%w: specify current status, resolution date, or a status transition", ErrAmbiguousIntent)
	}
	return nil
}

var allProjects = regexp.MustCompile(`(?i)\b(?:all|across)\s+projects\b`)
