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
	"strings"
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

	log *slog.Logger
}

// Options carries deployment configuration for NewTranslator. Zero value is
// valid for a pure fast/fallback setup without tenant vocabulary.
type Options struct {
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
		fallback:   fallback,
		// Defensive copies: the pipeline must not observe external mutation.
		projectMap: copyStringMap(opts.ProjectMap),
		schema:     copyStringMap(opts.Schema),
		log:        logger,
	}, nil
}

// Route labels identify which translation tier produced a Result, so logs and the
// web dashboard can tell the tiers apart. nl2jql is the leaf package that owns
// Result.Route and has no dependency on llm, so it declares the whole vocabulary
// here as the single source of truth even though it stamps only the first two;
// the llm package re-exports all three so consumers import the labels from one
// place and the two packages cannot drift.
const (
	// RouteFastPath and RouteFallback are stamped by this engine's Translate.
	RouteFastPath = "fast_path" // deterministic parser handled the query (no LLM call)
	RouteFallback = "fallback"  // strict, schema-scoped LLM fallback generated the JQL

	// RouteJSON is stamped by llm.generateJQLLLM (the JSON-prompt last resort), not
	// by this deterministic engine. It lives here so the route vocabulary stays a
	// single source of truth instead of splitting across two packages that could drift.
	RouteJSON = "json_fallback"
)

// Result is the outcome of a translation: the generated JQL and the Route label
// that produced it -- RouteFastPath for the deterministic parser, RouteFallback
// for the LLM fallback.
type Result struct {
	JQL   string
	Route string
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
	normalized := t.normalizer.Normalize(q, t.projectMap)

	// ------------------------------------------------------------------
	// 2) Fast path: the strict deterministic parser.
	// ------------------------------------------------------------------
	jql, parseErr := Parse(normalized)
	if parseErr == nil {
		log.Info("jql translation: fast path used (deterministic parser)",
			slog.String("route", RouteFastPath),
			slog.String("query", q),
			slog.String("normalized", normalized),
			slog.String("jql", jql),
		)
		return Result{JQL: jql, Route: RouteFastPath}, nil
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

	jql, err := t.fallback.Generate(ctx, normalized, t.schema)
	if err != nil {
		// Preserve causality: the caller can still see ErrUnmappable etc.
		// via errors.Is on the wrapped error.
		return Result{}, fmt.Errorf("%sfallback engine failed: %w", op, err)
	}

	log.Info("jql translation: fallback path used (LLM)",
		slog.String("route", RouteFallback),
		slog.String("query", q),
		slog.String("jql", jql),
	)
	return Result{JQL: jql, Route: RouteFallback}, nil
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
