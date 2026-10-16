package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

// jqlFallbackMaxTokens bounds the LLM completion used by the nl2jql
// FallbackEngine. The fallback returns a single-line JSON object wrapping the JQL, so a modest
// budget is plenty; it stays below the JSON path's 1024 to keep a runaway
// completion from bloating the response.
const jqlFallbackMaxTokens = 512

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
	return a.provider.Complete(ctx, systemPrompt, userPrompt, jqlFallbackMaxTokens)
}

// lazyJQLProvider is an nl2jql.Provider that defers construction of the
// underlying LLM Provider until the fallback path actually needs it. The fast
// path never touches the provider, so deferring construction means common
// fast-path queries pay no provider-setup cost -- notably the Vertex Google
// auth that NewProvider performs -- and that cost lands only on the queries
// the deterministic parser rejects. Construction happens once, guarded by
// sync.Once, so it is safe for concurrent use.
//
// That same guard also means a construction error is latched for the provider's
// lifetime and never retried: if NewProvider fails on the first fallback, every
// later fallback on this instance gets the cached error too. That is acceptable
// because lazyJQLProvider is only the deterministic pipeline's last resort -- when
// it errors, translateCascade falls through to generateJQLLLM, which builds its own
// fresh provider, so a transient construction failure is not fatal to the query.
type lazyJQLProvider struct {
	cfg  *config.Config
	once sync.Once
	p    Provider
	err  error
}

// Generate satisfies nl2jql.Provider, building the underlying provider on
// first use and surfacing its construction error to the caller.
func (l *lazyJQLProvider) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	l.once.Do(func() {
		l.p, l.err = NewProvider(l.cfg)
	})
	if l.err != nil {
		return "", l.err
	}
	return l.p.Complete(ctx, systemPrompt, userPrompt, jqlFallbackMaxTokens)
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
// vocabulary the LLM fallback needs. The FallbackEngine renders these keys
// (sorted, deterministic) into its strict one-line-JQL prompt.
func buildSchema(cfg *config.Config) map[string]string {
	schema := map[string]string{
		"current user": cfg.Assignee(),
	}
	if key := cfg.JiraProject; key != "" {
		schema["default project"] = "project = " + key
	}
	return schema
}

// newTranslator wires the deterministic-first translation pipeline for a tenant
// config: a default Normalizer and a FallbackEngine backed by the shared LLM
// client. The fallback is mandatory because a deterministic rejection with no
// fallback is unrecoverable. The provider is constructed lazily (see
// lazyJQLProvider), so this never calls NewProvider: a missing LLM config does
// not fail fast-path queries, and the provider is only built when a query
// actually reaches the fallback, where the caller still falls back to generateJQLLLM.
func newTranslator(cfg *config.Config) (*nl2jql.Translator, error) {
	return newTranslatorWithProjects(cfg, nil)
}

// newTranslatorWithProjects is newTranslator layered with a fetched project list:
// each project's key + display name (and lower-case variants) is folded into the
// normalizer's fuzzy vocabulary so human names like "Support" resolve to "SUP"
// without JIRA_PROJECT_ALIASES. A nil/empty projects slice yields the same map as
// newTranslator.
//
// The FallbackEngine is backed by a lazyJQLProvider, so the (potentially
// expensive) NewProvider -- notably Vertex Google auth -- runs only when a
// query the deterministic parser rejects actually needs the LLM; that is the
// payoff of the deterministic-first design, the common fast-path query pays no
// provider-setup cost. This per-request constructor is what the free GenerateJQL*
// functions use -- fine for the CLI, which lives one query. A long-lived caller
// (the MCP server) instead holds a TranslatorCache, which memoizes the engine by
// provider signature so the same provider-setup cost is paid at most once; the
// ProjectMap still changes per request because the cache rebuilds the translator
// from each request's fetched project list.
func newTranslatorWithProjects(cfg *config.Config, projects []jira.Project) (*nl2jql.Translator, error) {
	fallback, err := nl2jql.NewFallbackEngine(&lazyJQLProvider{cfg: cfg})
	if err != nil {
		return nil, err
	}
	return nl2jql.NewTranslator(nl2jql.NewNormalizer(), fallback, nl2jql.Options{
		ProjectMap: buildProjectMapWithProjects(cfg, projects),
		Schema:     buildSchema(cfg),
		Logger:     slog.Default(),
	})
}

// GenerateJQL is the env-only entry point: it translates without folding a
// fetched project list, equivalent to GenerateJQLWithProjects with a nil list.
// It stays the default path so callers without a cache -- and the deterministic
// hot path by default -- remain free of any project-list fetch.
func GenerateJQL(ctx context.Context, cfg *config.Config, naturalQuery string) (*QueryResult, error) {
	return GenerateJQLWithProjects(ctx, cfg, naturalQuery, nil)
}

// GenerateJQLWithProjects translates a natural-language query into JQL,
// preferring the
// deterministic nl2jql fast path and paying for an LLM call only when the parser
// (and its strict LLM fallback) cannot handle the query.
//
// Routing, in order:
//  1. nl2jql fast path — normalize + strict parse, no LLM. On success the time
//     window is baked into the JQL itself, so Days stays 0 (Route: fast_path).
//  2. nl2jql FallbackEngine — a strict, schema-scoped LLM call (one-line JQL),
//     reached only when the parser rejects. Also yields Days 0 (Route: fallback).
//  3. generateJQLLLM — the original JSON-prompt LLM path, the last-resort
//     safety net and the only tier that still extracts a comment-window "Days"
//     value, preserved for the digest call sites (Route: json_fallback).
//
// A context is threaded through so MCP cancellation and request timeouts
// propagate into the LLM fallback tiers; call sites without a context of their
// own (the CLI) pass context.Background().
func GenerateJQLWithProjects(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	return translateCascade(ctx, cfg, naturalQuery, projects, newTranslatorWithProjects)
}

// translateCascade runs the deterministic-first JQL pipeline with an injectable
// translator constructor and, on any deterministic failure, the JSON-prompt LLM
// last resort. The free GenerateJQLWithProjects passes newTranslatorWithProjects;
// TranslatorCache.GenerateJQL passes its signature-cached translatorFor, so the
// two share identical routing while differing only in whether the (expensive)
// FallbackEngine is rebuilt per request or memoized by provider signature.
func translateCascade(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project, build func(*config.Config, []jira.Project) (*nl2jql.Translator, error)) (*QueryResult, error) {
	slog.Info("generating JQL", "query", naturalQuery, "projects", len(projects))

	var parseError string
	if translator, err := build(cfg, projects); err != nil {
		slog.Warn("nl2jql translator unavailable; falling back to LLM JSON path", "error", err)
	} else if res, err := translator.Translate(ctx, naturalQuery); err != nil {
		parseError = res.ParseError
		slog.Warn("nl2jql translation failed; falling back to LLM JSON path", "error", err)
	} else {
		res.JQL = ensureProjectScope(res.JQL, cfg)
		slog.Info("generated JQL via nl2jql", "jql", res.JQL, "route", res.Route)
		return &QueryResult{
			Input:           res.Input,
			NormalizedInput: res.NormalizedInput,
			ParseError:      res.ParseError,
			JQL:             res.JQL,
			Route:           res.Route,
		}, nil
	}

	result, err := generateJQLLLM(ctx, cfg, naturalQuery)
	if err != nil {
		return nil, err
	}
	result.ParseError = parseError
	return result, nil
}

// projectPredicateRe matches any JQL predicate on the project field so
// ensureProjectScope can tell "the user already named a project" apart from a
// bare query. It covers the equality and membership operators; the leading word
// boundary keeps look-alikes such as "subproject" or "projectname" from matching.
var projectPredicateRe = regexp.MustCompile(`(?i)\bproject\s*(?:!=|=|\bin\b|not\s+in\b)`)

// ensureProjectScope preserves the behaviour the old LLM path guaranteed
// (QuerySystemPrompt rule #1): every query is scoped to the tenant's project
// unless the user names a different one. The deterministic nl2jql pipeline only
// emits a `project =` predicate when the user writes "in <project>", so we append
// the configured default when none is present. It is idempotent -- a no-op when a
// project predicate already exists or when no default project is configured -- so it
// never double-scopes. A bare quantifier such as "all" (as in "all bugs") is not a
// project name, so such a query is still scoped to the default; that is the intended
// behaviour, not a clobber. It runs on both translation paths -- the nl2jql pipeline
// above and, inside generateJQLLLM, the JSON-prompt LLM tier -- so project scoping is
// a hard guarantee rather than a matter of the model obeying QuerySystemPrompt rule #1.
//
// Appending the predicate is not a string concatenation, because JQL has no
// SQL-style precedence climbing (FIELDCTO-2807). Two structural rules apply:
//
//   - AND binds tighter than OR, so a where-clause containing a top-level OR must
//     be parenthesised first. Without it, `a OR b` + ` AND project = X` parses as
//     `a OR (b AND project = X)` and the first branch escapes the scope entirely,
//     silently matching every project in the tenant.
//   - ORDER BY must remain the final clause. Appending after it is a syntax error
//     that Jira rejects outright with HTTP 400, so the sort is split off, the
//     predicate inserted, and the sort re-attached.
func ensureProjectScope(rawJQL string, cfg *config.Config) string {
	if cfg == nil || cfg.JiraProject == "" || rawJQL == "" {
		return rawJQL
	}
	// Match against the blanked copy so a literal such as summary ~ "project = X"
	// is not mistaken for real scoping and does not suppress the predicate.
	blanked := jql.BlankQuotedLiterals(rawJQL)
	if projectPredicateRe.MatchString(blanked) {
		return rawJQL
	}

	where, orderBy := rawJQL, ""
	if idx := jql.OrderByIndex(blanked); idx >= 0 {
		where, orderBy = rawJQL[:idx], strings.TrimSpace(rawJQL[idx:])
	}
	where = strings.TrimSpace(where)

	scoped := "project = " + cfg.JiraProject
	if where != "" {
		// Re-blank: offsets shift once the ORDER BY clause is trimmed away.
		if jql.TopLevelKeywordIndex(jql.BlankQuotedLiterals(where), "OR") >= 0 {
			where = "(" + where + ")"
		}
		scoped = where + " AND " + scoped
	}
	if orderBy != "" {
		scoped += " " + orderBy
	}
	return scoped
}

// GenerateJQLForDigest is the digest-flavoured JQL translation. The digest handler
// needs a comment-activity window (QueryResult.Days) that is intentionally kept
// separate from the JQL, but the deterministic nl2jql pipeline returns JQL only.
// We recover the window the way the Phase-2 plan recommends -- "option 2 for the
// common case, fall back to option 1 until (2) is solid":
//
//  1. JQL always comes from the fast-first pipeline (GenerateJQL), so a window-less
//     query such as "my open bugs in Support" yields the same JQL here as in
//     jira_query instead of skipping the deterministic parser.
//  2. option 2 (primary window): when ExtractDays reads "last N days/weeks/..."
//     off the query it overrides whatever Days the pipeline produced, so the window
//     never costs a completion.
//  3. option 1 (fallback): when GenerateJQL errors we defer to the original LLM
//     JSON path (generateJQLLLM), the only tier that still extracts a "days"
//     field. When the regex found no window it supplies that window too; when
//     the regex did find one we keep it and borrow only the JQL, so a
//     detected-but-untranslatable window no longer hard-fails the digest.
func GenerateJQLForDigest(ctx context.Context, cfg *config.Config, naturalQuery string) (*QueryResult, error) {
	return GenerateJQLForDigestWithProjects(ctx, cfg, naturalQuery, nil)
}

// GenerateJQLForDigestWithProjects is GenerateJQLForDigest layered with a fetched
// project list, so human project names resolve via the normalizer's vocabulary.
func GenerateJQLForDigestWithProjects(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	// Option 2 (primary): read the comment-activity window off the query with a
	// cheap regex so it never costs a completion.
	days := ExtractDays(naturalQuery)

	// The JQL always comes from the fast-first pipeline, so a window-less query
	// routes through the deterministic parser exactly like jira_query.
	return digestCascade(ctx, cfg, naturalQuery, days, func() (*QueryResult, error) {
		return GenerateJQLWithProjects(ctx, cfg, naturalQuery, projects)
	})
}

// digestCascade layers the comment-activity window (option 2) onto a fast-first
// JQL source and falls back to the JSON-prompt LLM last resort (option 1) when
// that source errors, keeping the regex-derived window even then.
// GenerateJQLForDigestWithProjects passes a closure over GenerateJQLWithProjects;
// TranslatorCache.GenerateJQLForDigest passes one over its own cached GenerateJQL,
// so the two share the same window logic.
func digestCascade(ctx context.Context, cfg *config.Config, naturalQuery string, days int, fastFirst func() (*QueryResult, error)) (*QueryResult, error) {
	res, err := fastFirst()
	if err == nil {
		// Override the window when the regex parsed one; otherwise keep the
		// fast path's (0) window.
		if days > 0 {
			res.Days = days
		}
		return res, nil
	}

	// Option 1 (fallback): the fast path, the strict FallbackEngine, and its own
	// JSON last resort all failed. generateJQLLLM is the sole tier that still
	// extracts a "days" field, so it is always worth one last-resort retry.
	// When the regex found no window the JSON path supplies it; when the regex
	// did find one we keep that window and borrow only the JQL, so a
	// detected-but-untranslatable window no longer hard-fails the digest.
	res, err = generateJQLLLM(ctx, cfg, naturalQuery)
	if err != nil {
		return nil, err
	}
	if days > 0 {
		res.Days = days
	}
	return res, nil
}

// TranslatorCache memoizes the expensive half of the nl2jql pipeline -- the
// FallbackEngine and its lazily constructed LLM provider -- so a long-lived
// caller (the MCP server) pays the provider-setup cost (notably the Vertex
// Google auth in NewProvider) at most once per provider configuration instead
// of on every query. The deterministic fast path still pays nothing: the
// provider is built lazily and only when a query the parser rejects reaches the
// fallback, and by then the engine is already cached for that configuration.
//
// The cache is keyed by provider signature (see providerSignature), a function
// of exactly the inputs NewProvider reads -- the LLM_* / OLLAMA_* environment
// plus the config's Vertex project and region. The project list is deliberately
// NOT part of the key: it feeds only the normalizer's ProjectMap, which the
// per-request translator rebuilds from the current request's projects, so a
// shared engine stays correct as the fetched list changes. FallbackEngine is
// immutable and safe for concurrent use, so sharing one copy is safe.
//
// The free GenerateJQL* functions build a fresh engine per request -- fine for
// the CLI, which lives one query -- but a long-lived server should hold one
// *TranslatorCache for its lifetime and route through its GenerateJQL /
// GenerateJQLForDigest methods.
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
	return translateCascade(ctx, cfg, naturalQuery, projects, c.translatorFor)
}

// GenerateJQLForDigest is GenerateJQL's digest variant with the comment-activity
// window, also memoizing the FallbackEngine by provider signature.
func (c *TranslatorCache) GenerateJQLForDigest(ctx context.Context, cfg *config.Config, naturalQuery string, projects []jira.Project) (*QueryResult, error) {
	days := ExtractDays(naturalQuery)
	return digestCascade(ctx, cfg, naturalQuery, days, func() (*QueryResult, error) {
		return c.GenerateJQL(ctx, cfg, naturalQuery, projects)
	})
}

// translatorFor builds a per-request Translator on top of the signature-cached
// engine. The ProjectMap and schema are rebuilt from the current request's
// projects and config, so the deterministic hot path always sees a fresh
// vocabulary even though the (expensive) engine is shared.
func (c *TranslatorCache) translatorFor(cfg *config.Config, projects []jira.Project) (*nl2jql.Translator, error) {
	fallback, err := c.engine(cfg)
	if err != nil {
		return nil, err
	}
	return nl2jql.NewTranslator(nl2jql.NewNormalizer(), fallback, nl2jql.Options{
		ProjectMap: buildProjectMapWithProjects(cfg, projects),
		Schema:     buildSchema(cfg),
		Logger:     slog.Default(),
	})
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
