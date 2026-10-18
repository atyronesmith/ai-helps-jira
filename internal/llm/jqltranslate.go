package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// ErrInvalidGeneratedJQL is terminal: a generated query could not be parsed or
// validated locally. Digest callers must not ask another prompt to reinterpret it.
var ErrInvalidGeneratedJQL = errors.New("invalid generated JQL")

// jqlFallbackMaxTokens bounds the single typed intent completion.
const jqlFallbackMaxTokens = 4096

// jqlProviderAdapter adapts this package's Provider (a Complete call that takes a
// token budget) to the narrow text-completion contract nl2jql.FallbackEngine
// expects. This lets the deterministic-first translator reuse the exact same LLM
// client that every other llm feature uses, while keeping nl2jql free of any
// dependency on this package (it defines its own Provider on purpose).
type jqlProviderAdapter struct {
	provider Provider
}

// Generate satisfies nl2jql.Provider.
func (a jqlProviderAdapter) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	countProviderCall(ctx)
	return a.provider.Complete(ctx, systemPrompt, userPrompt, jqlFallbackMaxTokens)
}

// lazyJQLProvider creates a provider only after deterministic rejection.
// Initialization uses the current caller context and never latches cancellation.
type lazyJQLProvider struct {
	cfg *config.Config
}

// Generate satisfies nl2jql.Provider, building the underlying provider on
// first use and surfacing its construction error to the caller.
func (l *lazyJQLProvider) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Construction belongs to this request: a cancelled auth attempt must not
	// poison subsequent calls sharing the fallback engine.
	p, err := NewProviderContext(ctx, l.cfg)
	if err != nil {
		return "", err
	}
	countProviderCall(ctx)
	return p.Complete(ctx, systemPrompt, userPrompt, jqlFallbackMaxTokens)
}

type providerCallCounterKey struct{}
type providerCallCounter struct{ calls atomic.Int64 }

func countProviderCall(ctx context.Context) {
	if c, ok := ctx.Value(providerCallCounterKey{}).(*providerCallCounter); ok {
		c.calls.Add(1)
	}
}

// buildProjectMap seeds the normalizer's fuzzy project-name vocabulary. It is
// layered so a single-project tenant and a large multi-project tenant both get
// human-name coverage without a JIRA round-trip on every query:
//
//  1. The configured default key (cfg.JiraProject) plus any extra keys declared
//     in JIRA_PROJECT_KEYS (space- or comma-separated), each expanded to its
//     own lower-case variant so "the sup project" snaps to "SUP".
//  2. Org-specific human aliases from JIRA_PROJECT_ALIASES, a JSON object
//     mapping a free-text name to a project key, e.g. {"Support":"SUP"}.
//     Keeping these in the environment (never hardcoded, per the repo's
//     Sensitive-Data rule) is what lets "Support" resolve to "SUP". Each
//     alias's target is resolved to the canonical key casing and its variants
//     are registered too.
//
// Returns nil when nothing is configured. To also fold the tenant's full project
// list (key + display name) fetched from JIRA /rest/api/3/project, layer it in
// with buildProjectMapWithProjects (or ResolveProjectMap, which reads a fresh
// cached list so the deterministic hot path stays network-free after the first
// warm).
func buildProjectMap(cfg *config.Config) map[string]string {
	m := map[string]string{}

	// Canonical casing lookup so an alias target written as "sup" still binds
	// to the configured "SUP", and duplicate keys never fight each other.
	keys := projectKeys(cfg)
	canonical := make(map[string]string, len(keys))
	for _, key := range keys {
		addProjectVariants(m, key)
		canonical[strings.ToUpper(key)] = key
	}

	for alias, target := range projectAliases() {
		alias = strings.TrimSpace(alias)
		target = strings.TrimSpace(target)
		if alias == "" || target == "" {
			continue
		}
		if canonical, ok := canonical[strings.ToUpper(target)]; ok {
			target = canonical
		}
		addProjectVariants(m, target)
		m[alias] = target
		if lower := strings.ToLower(alias); lower != alias {
			m[lower] = target
		}
	}

	if len(m) == 0 {
		return nil
	}
	return m
}

// buildProjectMapWithProjects layers a fetched project list on top of the
// env-based map from buildProjectMap. Each project contributes its key plus its
// display name (and the lower-case variant of each), all binding to the
// canonical key casing, so a human name like "Support" resolves to "SUP" with
// no JIRA_PROJECT_ALIASES entry. It is a pure function of its arguments: the
// network fetch lives in ResolveProjectMap / the cache, never here. A nil or
// empty projects slice returns the env-only map unchanged (nil when empty).
func buildProjectMapWithProjects(cfg *config.Config, projects []jira.Project) map[string]string {
	m := buildProjectMap(cfg)
	if len(projects) == 0 {
		return m
	}
	if m == nil {
		m = map[string]string{}
	}

	// Resolve an alias target to the configured key's canonical casing so a
	// display name written as "sup" still binds to a configured "SUP".
	canonical := make(map[string]string, len(projectKeys(cfg)))
	for _, key := range projectKeys(cfg) {
		canonical[strings.ToUpper(key)] = key
	}

	for _, p := range projects {
		key := p.Key
		if key == "" {
			continue
		}
		if c, ok := canonical[strings.ToUpper(key)]; ok {
			key = c
		}
		addProjectVariants(m, key)

		name := strings.TrimSpace(p.Name)
		if name == "" || name == key {
			continue
		}
		m[name] = key
		if lower := strings.ToLower(name); lower != name {
			m[lower] = key
		}
	}

	if len(m) == 0 {
		return nil
	}
	return m
}

// ResolveProjectMap returns the fuzzy project vocabulary for a tenant config,
// folding the tenant's full project list (key + display name) from a cache so
// human names like "Support" resolve to "SUP" without JIRA_PROJECT_ALIASES. It
// prefers a fresh cached list and touches the network only on a cold or stale
// cache (via fetcher, which fetches and upserts), so the deterministic hot path
// is network-free after the first warm. Any missing input (nil cache or
// fetcher) or a fetch failure degrades to the env-only vocabulary so a warm-up
// never fails a query.
func ResolveProjectMap(cfg *config.Config, c *cache.Cache, fetcher cache.ProjectFetcher, ttl time.Duration) map[string]string {
	if c == nil || fetcher == nil {
		return buildProjectMap(cfg)
	}
	// Wrap the eager fetcher in a lazy builder so FetchProjects only reaches it on
	// a cold or stale cache; the caller already holds a built fetcher, so the
	// builder simply returns it.
	projects, _, err := c.FetchProjects(func() (cache.ProjectFetcher, error) {
		return fetcher, nil
	}, ttl)
	if err != nil {
		slog.Warn("project list warm-up failed; folding in cached or env-only vocabulary", "error", err)
	}
	return buildProjectMapWithProjects(cfg, projects)
}

// projectKeys returns the ordered, de-duplicated set of project keys a tenant
// cares about: the configured default first, then any extras declared via
// JIRA_PROJECT_KEYS (space- or comma-separated). De-duplication is
// case-insensitive so "SUP" and "sup" are not registered twice.
func projectKeys(cfg *config.Config) []string {
	raw := []string{cfg.JiraProject}
	raw = append(raw, splitKeyList(config.GetEnvOrSecret("JIRA_PROJECT_KEYS"))...)

	seen := make(map[string]struct{}, len(raw))
	keys := make([]string, 0, len(raw))
	for _, key := range raw {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, dup := seen[strings.ToUpper(key)]; dup {
			continue
		}
		seen[strings.ToUpper(key)] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// splitKeyList splits a project-keys list on commas and runs of whitespace,
// dropping empty fields so "ENG, PLT  extra" yields ["ENG","PLT","extra"].
func splitKeyList(raw string) []string {
	var out []string
	for _, chunk := range strings.Split(raw, ",") {
		out = append(out, strings.Fields(chunk)...)
	}
	return out
}

// projectAliases reads the tenant's human-name aliases from JIRA_PROJECT_ALIASES,
// a JSON object mapping a free-text name (e.g. "Support", "customer support") to
// a project key (e.g. "SUP"). A malformed blob is logged and skipped so one bad
// character can't break every query.
func projectAliases() map[string]string {
	raw := config.GetEnvOrSecret("JIRA_PROJECT_ALIASES")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var aliases map[string]string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		slog.Warn("JIRA_PROJECT_ALIASES is not valid JSON; ignoring", "error", err)
		return nil
	}
	return aliases
}

// addProjectVariants registers a project key and its lower-case form, both
// pointing at the key. The normalizer compares case-insensitively, so the
// lower-case entry mainly keeps the vocabulary explicit and readable.
func addProjectVariants(m map[string]string, key string) {
	m[key] = key
	if lower := strings.ToLower(key); lower != key {
		m[lower] = key
	}
}

// buildSchema seeds the FallbackEngine's system prompt with the minimal tenant
// vocabulary the LLM fallback needs. The fallback renders these keys in deterministic order in its typed intent prompt.
func buildSchema(cfg *config.Config) map[string]string {
	schema := map[string]string{
		"current user": cfg.Assignee(),
	}
	if key := cfg.JiraProject; key != "" {
		schema["default project"] = (&jql.Query{Where: jql.Compare("project", "=", jql.QuoteIdent(key))}).Render()
	}
	return schema
}

// newTranslator constructs a translator without tenant metadata.
func newTranslator(cfg *config.Config) (*nl2jql.Translator, error) {
	return newTranslatorWithProjects(cfg, nil)
}

// newTranslatorWithProjects folds cached project vocabulary into translation.
func newTranslatorWithProjects(cfg *config.Config, projects []jira.Project) (*nl2jql.Translator, error) {
	return newTranslatorWithMetadata(cfg, projects, TranslationMetadata{})
}
func newTranslatorWithMetadata(cfg *config.Config, projects []jira.Project, metadata TranslationMetadata) (*nl2jql.Translator, error) {
	aliases, err := fieldAliases()
	if err != nil {
		return nil, err
	}
	fallback, err := nl2jql.NewFallbackEngine(&lazyJQLProvider{cfg: cfg})
	if err != nil {
		return nil, err
	}
	return translatorWithInputs(cfg, fallback, nl2jql.NewNormalizer(), metadata, buildProjectMapWithProjects(cfg, projects), aliases, slog.Default())
}

func translatorWithInputs(cfg *config.Config, fallback *nl2jql.FallbackEngine, normalizer *nl2jql.Normalizer, metadata TranslationMetadata, projectMap, aliases map[string]string, logger *slog.Logger) (*nl2jql.Translator, error) {
	return nl2jql.NewTranslator(normalizer, fallback, nl2jql.Options{
		ProjectMap: projectMap, Schema: schemaWithFields(cfg, metadata.Fields, aliases),
		Fields: metadata.Fields, FieldAliases: aliases,
		Project: cfg.JiraProject, Self: cfg.Assignee(), Logger: logger,
	})
}

// GenerateJQL is the env-only entry point: it translates without folding a
// fetched project list, equivalent to GenerateJQLWithProjects with a nil list.
// It stays the default path so callers without a cache -- and the deterministic
// hot path by default -- remain free of any project-list fetch.
func GenerateJQL(ctx context.Context, cfg *config.Config, naturalQuery string) (*QueryResult, error) {
	return GenerateJQLWithProjects(ctx, cfg, naturalQuery, nil)
}

// GenerateJQLWithProjects uses deterministic intent or one bounded typed fallback.
func GenerateJQLWithProjects(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	return translateCascade(ctx, cfg, naturalQuery, projects, func(cfg *config.Config, projects []jira.Project) (*nl2jql.Translator, error) {
		return buildTranslatorForContext(ctx, cfg, projects, metadataFromContext(ctx), &lazyJQLProvider{cfg: cfg})
	})
}

func buildTranslatorForContext(ctx context.Context, cfg *config.Config, projects []jira.Project, metadata TranslationMetadata, provider nl2jql.Provider) (*nl2jql.Translator, error) {
	if fixture, ok := fixtureFromContext(ctx); ok {
		if fixture.Now.IsZero() {
			return nil, errors.New("translation fixture clock is empty")
		}
		normalizer := nl2jql.NewNormalizer()
		normalizer.Now = func() time.Time { return fixture.Now }
		fallback, err := nl2jql.NewFallbackEngine(provider)
		if err != nil {
			return nil, err
		}
		return translatorWithInputs(cfg, fallback, normalizer, fixture.Metadata, fixture.ProjectMap, fixture.FieldAliases, slog.Default())
	}
	aliases, err := fieldAliases()
	if err != nil {
		return nil, err
	}
	fallback, err := nl2jql.NewFallbackEngine(provider)
	if err != nil {
		return nil, err
	}
	return translatorWithInputs(cfg, fallback, nl2jql.NewNormalizer(), metadata, buildProjectMapWithProjects(cfg, projects), aliases, slog.Default())
}

// translateCascade compiles one translator result and enforces local scope.
func translateCascade(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project, build func(*config.Config, []jira.Project) (*nl2jql.Translator, error)) (*QueryResult, error) {
	started := time.Now()
	counter := &providerCallCounter{}
	ctx = context.WithValue(ctx, providerCallCounterKey{}, counter)
	finish := func(res *QueryResult) {
		if res != nil {
			res.ProviderCalls = int(counter.calls.Load())
			res.DurationNanos = time.Since(started).Nanoseconds()
			now := time.Now()
			if fixture, ok := fixtureFromContext(ctx); ok {
				now = fixture.Now
			}
			stampMetadataAt(ctx, res, now)
		}
	}
	if err := ctx.Err(); err != nil {
		res := &QueryResult{Input: naturalQuery}
		finish(res)
		return res, translationerror.New(translationerror.KindOf(err), err)
	}
	translator, err := build(cfg, projects)
	if err != nil {
		res := &QueryResult{Input: naturalQuery}
		finish(res)
		return res, translationerror.New(translationerror.Unsupported, err)
	}
	res, err := translator.Translate(ctx, naturalQuery)
	if res.Input == "" {
		res.Input = naturalQuery
	}
	if err != nil {
		result := &QueryResult{Input: res.Input, NormalizedInput: res.NormalizedInput, ParseError: res.ParseError, Route: res.Route, Mode: res.Mode}
		finish(result)
		return result, err
	}
	res.JQL, err = finalizeSearchJQL(res.JQL, cfg, res.Mode)
	if err != nil {
		result := &QueryResult{Input: res.Input, NormalizedInput: res.NormalizedInput, ParseError: res.ParseError, Route: res.Route, Mode: res.Mode}
		finish(result)
		return result, translationerror.New(translationerror.InvalidOutput, err)
	}
	result := &QueryResult{Input: res.Input, NormalizedInput: res.NormalizedInput, ParseError: res.ParseError, JQL: res.JQL, Route: res.Route, Mode: res.Mode}
	finish(result)
	return result, nil
}

// ensureProjectScope parses provider output before composition. Fully scoped
// explicit projects and direct identities remain unchanged. A missing project
// is added around the complete expression; partial or negative explicit scopes
// are rejected rather than narrowed to a contradictory default. Sorting remains
// terminal and the execution-boundary validation contract also applies here.
func ensureProjectScope(rawJQL string, cfg *config.Config) (string, error) {
	return finalizeSearchJQL(rawJQL, cfg, "")
}

// Application defaults apply to natural-language result lists. Lower-level
// predicate composition and count translations retain their explicit ordering.
func finalizeSearchJQL(rawJQL string, cfg *config.Config, mode nl2jql.QueryMode) (string, error) {
	if strings.TrimSpace(rawJQL) == "" {
		return "", fmt.Errorf("%w %q: JQL is blank", ErrInvalidGeneratedJQL, rawJQL)
	}
	q, err := jql.Parse(rawJQL)
	if err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrInvalidGeneratedJQL, rawJQL, err)
	}
	if cfg != nil && cfg.JiraProject != "" && !q.HasProjectScope() && !q.DirectIdentityOnly() {
		if q.HasField("project") {
			return "", fmt.Errorf("%w %q: explicit project predicates do not positively scope every branch; clarify the project choices", ErrInvalidGeneratedJQL, rawJQL)
		}
		q.AndClause(jql.Compare("project", "=", jql.QuoteIdent(cfg.JiraProject)))
	}
	if (mode == nl2jql.ModeList || mode == nl2jql.ModeRecommendation) && len(q.OrderBy) == 0 {
		q.OrderBy = []jql.Sort{{Field: "updated", Direction: "DESC"}}
	}
	rendered := q.Render()
	if err := jql.Validate(rendered, jql.Options{DefaultProject: projectForValidation(cfg)}); err != nil {
		return "", fmt.Errorf("%w %q: %w", ErrInvalidGeneratedJQL, rendered, err)
	}
	return rendered, nil
}

func projectForValidation(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.JiraProject
}

// GenerateJQLForDigest separates comment windows from issue search dates.
func GenerateJQLForDigest(ctx context.Context, cfg *config.Config, naturalQuery string) (*QueryResult, error) {
	return GenerateJQLForDigestWithProjects(ctx, cfg, naturalQuery, nil)
}

// GenerateJQLForDigestWithProjects is GenerateJQLForDigest layered with a fetched
// project list, so human project names resolve via the normalizer's vocabulary.
func GenerateJQLForDigestWithProjects(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	query, days, err := splitDigestQuery(naturalQuery)
	if err != nil {
		return nil, err
	}
	return digestCascade(ctx, cfg, naturalQuery, days, func() (*QueryResult, error) { return GenerateJQLWithProjects(ctx, cfg, query, projects) })
}

// digestCascade attaches a parsed comment window; failures are terminal.
func digestCascade(ctx context.Context, cfg *config.Config, naturalQuery string, days int, fastFirst func() (*QueryResult, error)) (*QueryResult, error) {
	res, err := fastFirst()
	if err != nil {
		return nil, err
	}
	res.Days = days
	res.Input = naturalQuery
	return res, nil
}

// TranslatorCache shares immutable fallback engines by provider configuration.
// Per-request metadata, identity, and project vocabulary are rebuilt each time.
type TranslatorCache struct {
	mu      sync.Mutex
	engines map[string]*nl2jql.FallbackEngine

	// build constructs the FallbackEngine for a configuration. It is a field
	// (not a hard-coded call) so in-package tests can inject a counting builder
	// to assert the cache reuses engines; production wires the default below.
	build func(*config.Config) (*nl2jql.FallbackEngine, error)
}

// NewTranslatorCache returns a ready-to-use cache that builds each engine from
// the shared, lazily-constructing LLM provider.
func NewTranslatorCache() *TranslatorCache {
	return &TranslatorCache{
		engines: make(map[string]*nl2jql.FallbackEngine),
		build: func(cfg *config.Config) (*nl2jql.FallbackEngine, error) {
			return nl2jql.NewFallbackEngine(&lazyJQLProvider{cfg: cfg})
		},
	}
}

// GenerateJQL is the cache-backed fast-first pipeline: it memoizes the
// FallbackEngine by provider signature instead of rebuilding it per request,
// otherwise identical to GenerateJQLWithProjects.
func (c *TranslatorCache) GenerateJQL(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	return translateCascade(ctx, cfg, naturalQuery, projects, func(cfg *config.Config, projects []jira.Project) (*nl2jql.Translator, error) {
		return c.translatorForContext(ctx, cfg, projects, metadataFromContext(ctx))
	})
}

// GenerateJQLForDigest is GenerateJQL's digest variant with the comment-activity
// window, also memoizing the FallbackEngine by provider signature.
func (c *TranslatorCache) GenerateJQLForDigest(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	query, days, err := splitDigestQuery(naturalQuery)
	if err != nil {
		return nil, err
	}
	return digestCascade(ctx, cfg, naturalQuery, days, func() (*QueryResult, error) { return c.GenerateJQL(ctx, cfg, query, projects) })
}

// translatorFor builds a per-request Translator on top of the signature-cached
// engine. The ProjectMap and schema are rebuilt from the current request's
// projects and config, so the deterministic hot path always sees a fresh
// vocabulary even though the (expensive) engine is shared.
func (c *TranslatorCache) translatorFor(cfg *config.Config, projects []jira.Project) (*nl2jql.Translator, error) {
	return c.translatorForMetadata(cfg, projects, TranslationMetadata{})
}
func (c *TranslatorCache) translatorForMetadata(cfg *config.Config, projects []jira.Project, metadata TranslationMetadata) (*nl2jql.Translator, error) {
	aliases, err := fieldAliases()
	if err != nil {
		return nil, err
	}
	fallback, err := c.engine(cfg)
	if err != nil {
		return nil, err
	}
	return translatorWithInputs(cfg, fallback, nl2jql.NewNormalizer(), metadata, buildProjectMapWithProjects(cfg, projects), aliases, slog.Default())
}
func (c *TranslatorCache) translatorForContext(ctx context.Context, cfg *config.Config, projects []jira.Project, metadata TranslationMetadata) (*nl2jql.Translator, error) {
	if fixture, ok := fixtureFromContext(ctx); ok {
		if fixture.Now.IsZero() {
			return nil, errors.New("translation fixture clock is empty")
		}
		fallback, err := c.engine(cfg)
		if err != nil {
			return nil, err
		}
		normalizer := nl2jql.NewNormalizer()
		normalizer.Now = func() time.Time { return fixture.Now }
		return translatorWithInputs(cfg, fallback, normalizer, fixture.Metadata, fixture.ProjectMap, fixture.FieldAliases, slog.Default())
	}
	return c.translatorForMetadata(cfg, projects, metadata)
}

// engine returns the FallbackEngine memoized for cfg's provider signature,
// building it on first use under the mutex. A build error is not stored, so a
// transient provider failure is retried on the next request rather than cached.
func (c *TranslatorCache) engine(cfg *config.Config) (*nl2jql.FallbackEngine, error) {
	key := providerSignature(cfg)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.engines[key]; ok {
		return e, nil
	}
	e, err := c.build(cfg)
	if err != nil {
		return nil, err
	}
	c.engines[key] = e
	return e, nil
}

// providerSignature folds the exact inputs NewProvider reads into a single cache
// key, so two configurations that would build the same provider share one engine
// and two that differ build separate ones. A nil config yields a fixed empty key
// (its provider build fails anyway, so the error is simply not cached).
func providerSignature(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.Join([]string{
		config.GetEnvOrSecret("LLM_PROVIDER"),
		config.GetEnvOrSecret("LLM_BASE_URL"),
		config.GetEnvOrSecret("LLM_API_KEY"),
		config.GetEnvOrSecret("OLLAMA_BASE_URL"),
		config.GetEnvOrSecret("LLM_MODEL"),
		cfg.VertexProjectID,
		cfg.VertexRegion,
	}, "\x00")
}
