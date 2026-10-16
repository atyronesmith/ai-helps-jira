package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

// fakeJQLProvider is a counting nl2jql.Provider used to assert which tier a
// query routes through. It records how often the LLM fallback was reached.
type fakeJQLProvider struct {
	calls int32
	jql   string
	fail  error
}

func (f *fakeJQLProvider) Generate(_ context.Context, _, _ string) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.jql, f.fail
}

// fakeLLMProvider implements this package's Provider for the adapter test.
type fakeLLMProvider struct {
	sys, user string
	maxTokens int
	calls     int32
}

func (f *fakeLLMProvider) Complete(_ context.Context, system, prompt string, maxTokens int) (string, error) {
	atomic.AddInt32(&f.calls, 1)
	f.sys, f.user, f.maxTokens = system, prompt, maxTokens
	return "status = Open", nil
}

func newTestTranslator(t *testing.T, p nl2jql.Provider) *nl2jql.Translator {
	t.Helper()
	fb, err := nl2jql.NewFallbackEngine(p)
	if err != nil {
		t.Fatalf("NewFallbackEngine: %v", err)
	}
	tr, err := nl2jql.NewTranslator(nl2jql.NewNormalizer(), fb, nl2jql.Options{})
	if err != nil {
		t.Fatalf("NewTranslator: %v", err)
	}
	return tr
}

// The adapter must bridge llm.Provider.Complete (with a token budget) onto the
// narrow nl2jql.Provider.Generate contract, passing the fallback token budget.
func TestJQLProviderAdapterDelegates(t *testing.T) {
	fp := &fakeLLMProvider{}
	ad := jqlProviderAdapter{provider: fp}

	got, err := ad.Generate(context.Background(), "system-prompt", "user query")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "status = Open" {
		t.Errorf("content = %q, want %q", got, "status = Open")
	}
	if n := atomic.LoadInt32(&fp.calls); n != 1 {
		t.Errorf("Complete called %d times, want 1", n)
	}
	if fp.sys != "system-prompt" || fp.user != "user query" {
		t.Errorf("args = (%q, %q), want (system-prompt, user query)", fp.sys, fp.user)
	}
	if fp.maxTokens != jqlFallbackMaxTokens {
		t.Errorf("maxTokens = %d, want %d", fp.maxTokens, jqlFallbackMaxTokens)
	}
}

// TestBuildProjectMap covers the baseline (configured key + lower-case variant)
// and the "nothing configured -> nil" contract.
func TestBuildProjectMap(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	m := buildProjectMap(&config.Config{JiraProject: "SUP"})
	if m["SUP"] != "SUP" {
		t.Errorf("expected SUP->SUP, got %v", m)
	}
	if m["sup"] != "SUP" {
		t.Errorf("expected lower-case variant sup->SUP, got %v", m)
	}

	// No project key anywhere -> nil map (preserves the original contract).
	if got := buildProjectMap(&config.Config{}); got != nil {
		t.Errorf("empty project should yield nil map, got %v", got)
	}
}

// JIRA_PROJECT_ALIASES teaches the resolver org-specific human names. This is the
// mechanism behind "Support" -> SUP without hardcoding any tenant data.
func TestBuildProjectMapAliases(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", `{"Support":"SUP","Customer Support":"SUP"}`)

	m := buildProjectMap(&config.Config{JiraProject: "SUP"})
	for _, alias := range []string{"Support", "support", "Customer Support", "customer support"} {
		if m[alias] != "SUP" {
			t.Errorf("alias %q -> %q, want SUP", alias, m[alias])
		}
	}
	// The alias target's own variants must still resolve.
	if m["sup"] != "SUP" {
		t.Errorf("expected sup->SUP, got %v", m)
	}
}

// JIRA_PROJECT_KEYS gives multi-project tenants full coverage without a JIRA
// round-trip, and de-duplicates case-insensitively.
func TestBuildProjectMapExtraKeys(t *testing.T) {
	t.Setenv("JIRA_PROJECT_ALIASES", "")
	t.Setenv("JIRA_PROJECT_KEYS", "ENG, PLT  extra  sup")

	m := buildProjectMap(&config.Config{JiraProject: "SUP"})
	for _, key := range []string{"SUP", "ENG", "PLT", "extra"} {
		if m[key] != key {
			t.Errorf("expected %s->%s, got %v", key, key, m)
		}
	}
	if m["eng"] != "ENG" {
		t.Errorf("expected eng->ENG variant, got %v", m)
	}
	// "sup" (a lower-case duplicate of SUP) must not create a second binding.
	if m["sup"] != "SUP" {
		t.Errorf("expected sup->SUP after case-insensitive dedup, got %v", m)
	}
}

// A malformed alias blob must not break resolution: it is skipped and the
// configured key still resolves.
func TestBuildProjectMapMalformedAliases(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", `{not valid json`)

	m := buildProjectMap(&config.Config{JiraProject: "SUP"})
	if m["SUP"] != "SUP" {
		t.Errorf("expected SUP->SUP after malformed aliases, got %v", m)
	}
}

func TestBuildSchema(t *testing.T) {
	s := buildSchema(&config.Config{JiraProject: "SUP"})
	if s["default project"] != "project = SUP" {
		t.Errorf("default project = %q, want %q", s["default project"], "project = SUP")
	}
	if s["current user"] != "currentUser()" {
		t.Errorf("current user = %q, want currentUser()", s["current user"])
	}

	// An explicit JiraUser is quoted in the JQL assignee expression.
	s2 := buildSchema(&config.Config{JiraProject: "SUP", JiraUser: "jsmith"})
	if s2["current user"] != `"jsmith"` {
		t.Errorf("current user = %q, want \"jsmith\"", s2["current user"])
	}
}

// A parseable query must never reach the LLM provider.
func TestFastPathSkipsProvider(t *testing.T) {
	fb := &fakeJQLProvider{fail: errors.New("provider must not be reached on the fast path")}
	tr := newTestTranslator(t, fb)

	res, err := tr.Translate(context.Background(), "my open bugs in Support")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`
	if res.JQL != want {
		t.Errorf("fast path JQL = %q, want %q", res.JQL, want)
	}
	if res.Route != "fast_path" {
		t.Errorf("route = %q, want fast_path", res.Route)
	}
	if res.Input != "my open bugs in Support" {
		t.Errorf("input = %q, want exact original input", res.Input)
	}
	if res.NormalizedInput == "" {
		t.Error("normalized input is empty on nl2jql route")
	}
	if n := atomic.LoadInt32(&fb.calls); n != 0 {
		t.Errorf("provider was reached %d times on the fast path, want 0", n)
	}
}

// An unparseable query must fall through to the LLM fallback, which returns JQL
// and leaves the comment-window Days untouched (0).
func TestFallbackUsedOnParseFailure(t *testing.T) {
	const wantJQL = "status = Open AND priority = High"
	fb := &fakeJQLProvider{jql: wantJQL}
	tr := newTestTranslator(t, fb)

	res, err := tr.Translate(context.Background(), "external bugs")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.JQL != wantJQL {
		t.Errorf("fallback JQL = %q, want %q", res.JQL, wantJQL)
	}
	if res.Route != "fallback" {
		t.Errorf("route = %q, want fallback", res.Route)
	}
	if res.Input != "external bugs" || res.NormalizedInput == "" || res.ParseError == "" {
		t.Errorf("fallback diagnostics = input %q normalized %q parse error %q", res.Input, res.NormalizedInput, res.ParseError)
	}
	if n := atomic.LoadInt32(&fb.calls); n != 1 {
		t.Errorf("provider reached %d times, want 1", n)
	}
}

// withOpenAIEnv points the shared LLM provider at a local test server.
func withOpenAIEnv(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("LLM_BASE_URL", serverURL)
	t.Setenv("LLM_MODEL", "test-model")
	t.Setenv("LLM_API_KEY", "test")
}

// openAIJSON wraps a raw model completion in the shape OpenAIProvider expects.
func openAIJSON(content string) string {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	})
	return string(body)
}

// GenerateJQL on a parseable query must use the fast path and never call the LLM.
func TestGenerateJQLFastPath(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, openAIJSON(`status = Open`))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "SUP"}, "my open bugs in Support")
	if err != nil {
		t.Fatalf("GenerateJQL: %v", err)
	}
	want := `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`
	if res.JQL != want {
		t.Errorf("JQL = %q, want %q", res.JQL, want)
	}
	if res.Days != 0 {
		t.Errorf("Days = %d, want 0 (fast path bakes the window into JQL)", res.Days)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("LLM reached %d times on the fast path, want 0", n)
	}
}

func TestGenerateJQLOrderByFastPathAndScope(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, openAIJSON(`status = Open`))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	tests := []struct {
		query string
		want  string
	}{
		{
			query: "list all my open work items ordered by story points ascending.",
			want:  `statusCategory != Done AND assignee = currentUser() AND project = PROJ ORDER BY "Story Points" ASC`,
		},
		{
			query: "my bugs sorted by priority desc",
			want:  `issuetype = Bug AND assignee = currentUser() AND project = PROJ ORDER BY priority DESC`,
		},
		{
			query: "open tasks order by creation date ascending",
			want:  `issuetype = Task AND statusCategory != Done AND project = PROJ ORDER BY created ASC`,
		},
		{
			query: "my open bugs ordered by priority descending, created ascending",
			want:  `issuetype = Bug AND statusCategory != Done AND assignee = currentUser() AND project = PROJ ORDER BY priority DESC, created ASC`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "PROJ"}, tc.query)
			if err != nil {
				t.Fatalf("GenerateJQL: %v", err)
			}
			if res.Route != RouteFastPath {
				t.Errorf("route = %q, want %q", res.Route, RouteFastPath)
			}
			if res.JQL != tc.want {
				t.Errorf("JQL = %q, want %q", res.JQL, tc.want)
			}
		})
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("LLM reached %d times on the ORDER BY fast path, want 0", n)
	}
}

func TestGenerateJQLPhase3DateAndBooleanSemantics(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")
	cases := []struct {
		name  string
		query string
		want  string
		mode  nl2jql.QueryMode
	}{
		{"due this week has bounded dates without implicit owner or sort", "List jiras that are due this week?", `due >= startOfWeek() AND due < startOfWeek("+1") AND project = PROJ`, nl2jql.ModeList},
		{"closed history uses CHANGED with exclusive month boundary", "How many jiras did I close in June 2026?", `status CHANGED TO Closed BY currentUser() DURING ("2026-06-01 00:00", "2026-07-01 00:00") BEFORE "2026-07-01 00:00" AND project = PROJ`, nl2jql.ModeCount},
		{"grouped mixed-field alternatives preserve Boolean grouping", "my (open bugs or done tasks)", `assignee = currentUser() AND (issuetype = Bug AND statusCategory != Done OR issuetype = Task AND status = Done) AND project = PROJ`, nl2jql.ModeList},
		{"same-field status alternatives remain OR", "my open or done bugs", `issuetype = Bug AND (statusCategory != Done OR status = Done) AND assignee = currentUser() AND project = PROJ`, nl2jql.ModeList},
		{"multiple sort keys survive rendering", "my bugs ordered by priority descending, created ascending", `issuetype = Bug AND assignee = currentUser() AND project = PROJ ORDER BY priority DESC, created ASC`, nl2jql.ModeList},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "PROJ"}, tc.query)
			if err != nil {
				t.Fatalf("GenerateJQL: %v", err)
			}
			if res.Route != RouteFastPath {
				t.Errorf("route = %q, want %q", res.Route, RouteFastPath)
			}
			if res.JQL != tc.want {
				t.Errorf("JQL = %q, want %q", res.JQL, tc.want)
			}
			if res.Mode != tc.mode {
				t.Errorf("mode = %q, want %q", res.Mode, tc.mode)
			}
		})
	}
}

func TestGenerateJQLRejectsMixedUngroupedORWithoutProvider(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")
	_, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "PROJ"}, "my open bugs or done tasks")
	if !errors.Is(err, nl2jql.ErrAmbiguousIntent) {
		t.Fatalf("error = %v, want ErrAmbiguousIntent", err)
	}
}

func TestProductionTranslatorsResolveConfiguredSelf(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")
	for _, cached := range []bool{false, true} {
		name := "GenerateJQL"
		if cached {
			name = "TranslatorCache.GenerateJQL"
		}
		t.Run(name, func(t *testing.T) {
			var providerCalls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&providerCalls, 1)
				_, _ = io.WriteString(w, openAIJSON(`status = Open`))
			}))
			defer srv.Close()
			withOpenAIEnv(t, srv.URL)

			var cache *TranslatorCache
			if cached {
				cache = NewTranslatorCache()
			}
			translate := func(cfg *config.Config, query string) (*QueryResult, error) {
				if cache != nil {
					return cache.GenerateJQL(context.Background(), cfg, query, nil)
				}
				return GenerateJQL(context.Background(), cfg, query)
			}

			cases := []struct {
				name, query, user string
				want              []string
				absent            []string
			}{
				{"my resolves to configured user", "my issues", "configured@example.com", []string{`assignee = "configured@example.com"`}, []string{"currentUser()"}},
				{"assigned to me resolves to configured user", "issues assigned to me", "configured@example.com", []string{`assignee = "configured@example.com"`}, []string{"currentUser()"}},
				{"history actor resolves to configured user", "issues closed by me in June 2026", "configured@example.com", []string{`CHANGED TO Closed BY "configured@example.com"`, `DURING ("2026-06-01 00:00", "2026-07-01 00:00") BEFORE "2026-07-01 00:00"`}, []string{"currentUser()"}},
				{"explicit assignee is preserved", "all issues assigned to jsmith@example.com that are not closed", "configured@example.com", []string{`assignee = "jsmith@example.com"`}, []string{`assignee = "configured@example.com"`}},
				{"default self remains currentUser", "my issues", "", []string{"assignee = currentUser()"}, []string{`assignee = "configured@example.com"`}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					res, err := translate(&config.Config{JiraProject: "PROJ", JiraUser: tc.user}, tc.query)
					if err != nil {
						t.Fatalf("translate: %v", err)
					}
					if res.Route != RouteFastPath {
						t.Fatalf("route = %q, want %q", res.Route, RouteFastPath)
					}
					for _, want := range tc.want {
						if !strings.Contains(res.JQL, want) {
							t.Errorf("JQL %q does not contain %q", res.JQL, want)
						}
					}
					for _, absent := range tc.absent {
						if strings.Contains(res.JQL, absent) {
							t.Errorf("JQL %q unexpectedly contains %q", res.JQL, absent)
						}
					}
				})
			}
			if got := atomic.LoadInt32(&providerCalls); got != 0 {
				t.Errorf("provider called %d times for deterministic self/assignee queries, want 0", got)
			}
		})
	}
}

func TestGenerateJQLInvalidOrderByUsesFallback(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, openAIJSON(`status = Open`))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	for _, query := range []string{
		"my issues ordered by sprint ascending",
		"my issues ordered by priority upwards",
	} {
		t.Run(query, func(t *testing.T) {
			res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "PROJ"}, query)
			if err != nil {
				t.Fatalf("GenerateJQL: %v", err)
			}
			if res.Route != RouteFallback {
				t.Errorf("route = %q, want %q", res.Route, RouteFallback)
			}
			if res.ParseError == "" {
				t.Error("ParseError is empty, want parser diagnostic")
			}
		})
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("LLM reached %d times for invalid ORDER BY queries, want 2", n)
	}
}

// GenerateJQL on an unparseable query must route to the strict FallbackEngine.
func TestGenerateJQLFallback(t *testing.T) {
	const wantJQL = "status = Open AND priority = High AND project = SUP"
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, openAIJSON(wantJQL))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs")
	if err != nil {
		t.Fatalf("GenerateJQL: %v", err)
	}
	if res.JQL != wantJQL {
		t.Errorf("JQL = %q, want %q", res.JQL, wantJQL)
	}
	if res.Days != 0 {
		t.Errorf("Days = %d, want 0 (FallbackEngine emits no comment window)", res.Days)
	}
	if res.Route != RouteFallback {
		t.Errorf("route = %q, want %q (nl2jql strict LLM fallback)", res.Route, RouteFallback)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("LLM reached %d times, want 1", n)
	}
}

// When the strict fallback refuses (EMPTY sentinel), GenerateJQL must fall back
// to the original LLM JSON path, which is the only tier that extracts Days.
func TestGenerateJQLJSONFallbackExtractsDays(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var content string
		if n == 1 {
			// First hit is the FallbackEngine; refuse it so we cascade to
			// generateJQLLLM on the second hit.
			content = "EMPTY"
		} else {
			content = `{"jql":"status = Open","days":7}`
		}
		_, _ = io.WriteString(w, openAIJSON(content))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs")
	if err != nil {
		t.Fatalf("GenerateJQL: %v", err)
	}
	if res.JQL != "status = Open AND project = SUP" {
		t.Errorf("JQL = %q, want %q (JSON tier hard-scopes the default project)", res.JQL, "status = Open AND project = SUP")
	}
	if res.Days != 7 {
		t.Errorf("Days = %d, want 7 (preserved by the JSON fallback)", res.Days)
	}
	if res.Route != RouteJSON {
		t.Errorf("route = %q, want %q (JSON-prompt LLM path)", res.Route, RouteJSON)
	}
	if res.Input != "external bugs" || res.NormalizedInput != "" || res.ParseError == "" {
		t.Errorf("JSON fallback diagnostics = input %q normalized %q parse error %q", res.Input, res.NormalizedInput, res.ParseError)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("LLM reached %d times, want 2 (strict fallback + JSON fallback)", n)
	}
}

// ensureProjectScope (gap A) appends the tenant default only when no project
// predicate is present, and never double-scopes or clobbers an explicit one.
func TestEnsureProjectScope(t *testing.T) {
	cases := []struct {
		name    string
		jql     string
		project string
		want    string
	}{
		{"no predicate appends default", "status = Open", "SUP", "status = Open AND project = SUP"},
		{"existing predicate is untouched", "project = ENG AND status = Open", "SUP", "project = ENG AND status = Open"},
		{"membership predicate is untouched", "project in (SUP, ENG)", "SUP", "project IN (SUP, ENG)"},
		{"negative project requires clarification", "project not in (SUP)", "SUP", ""},
		{"partially scoped OR requires clarification", "project = OTHER OR status = Open", "SUP", ""},
		{"blank jql is invalid", "", "SUP", ""},
		{"no default project is a no-op", "status = Open", "", "status = Open"},
		{"subproject look-alike is not a project predicate", "subproject = X", "SUP", "subproject = X AND project = SUP"},

		// FIELDCTO-2807: AND binds tighter than OR in JQL, so a where-clause with a
		// top-level OR must be parenthesised or the leading branches escape the scope.
		{
			"top-level OR is parenthesised before scoping",
			"issuetype = Bug OR issuetype = Story", "SUP",
			"(issuetype = Bug OR issuetype = Story) AND project = SUP",
		},
		{
			"closed-month history stays a single CHANGED clause before scoping",
			`status CHANGED TO Closed BY currentUser() DURING ("2026-06-01 00:00", "2026-07-01 00:00") BEFORE "2026-07-01 00:00"`, "FIELDCTO",
			`status CHANGED TO Closed BY currentUser() DURING ("2026-06-01 00:00", "2026-07-01 00:00") BEFORE "2026-07-01 00:00" AND project = FIELDCTO`,
		},
		{
			"OR already inside parentheses is not double-wrapped",
			`status = Open AND (priority = High OR priority = Highest)`, "SUP",
			`status = Open AND (priority = High OR priority = Highest) AND project = SUP`,
		},
		{
			"OR inside a quoted literal is not structural",
			`summary ~ "crash or hang"`, "SUP",
			`summary ~ "crash or hang" AND project = SUP`,
		},
		{
			"word containing or is not an OR operator",
			"reporter = currentUser()", "SUP",
			"reporter = currentUser() AND project = SUP",
		},

		// FIELDCTO-2807: ORDER BY must stay the final clause; appending after it is
		// a syntax error Jira rejects with HTTP 400.
		{
			"due-this-week range keeps default scope without an implicit owner or sort",
			`due >= startOfWeek() AND due < startOfWeek("+1")`, "FIELDCTO",
			`due >= startOfWeek() AND due < startOfWeek("+1") AND project = FIELDCTO`,
		},
		{
			"predicate is inserted before ORDER BY",
			"status = Closed ORDER BY created DESC", "SUP",
			"status = Closed AND project = SUP ORDER BY created DESC",
		},
		{
			"OR and ORDER BY combined",
			"issuetype = Bug OR issuetype = Story ORDER BY priority ASC", "SUP",
			"(issuetype = Bug OR issuetype = Story) AND project = SUP ORDER BY priority ASC",
		},
		{
			"ORDER BY inside a quoted literal is not the clause boundary",
			`summary ~ "order by clause" AND status = Open`, "SUP",
			`summary ~ "order by clause" AND status = Open AND project = SUP`,
		},
		{
			"irregular whitespace between ORDER and BY is still recognised",
			"status = Open ORDER   BY created DESC", "SUP",
			"status = Open AND project = SUP ORDER BY created DESC",
		},
		{
			"sort-only query with no where-clause",
			"ORDER BY created DESC", "SUP",
			"project = SUP ORDER BY created DESC",
		},

		// A project predicate hidden inside a string literal must not suppress scoping.
		{
			"project predicate inside a quoted literal does not count as scoping",
			`summary ~ "project = ENG"`, "SUP",
			`summary ~ "project = ENG" AND project = SUP`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ensureProjectScope(tc.jql, &config.Config{JiraProject: tc.project})
			if tc.want == "" {
				if !errors.Is(err, ErrInvalidGeneratedJQL) {
					t.Fatalf("expected terminal JQL scope/parse error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureProjectScope: %v", err)
			}
			if got != tc.want {
				t.Errorf("ensureProjectScope(%q, project=%q) = %q, want %q", tc.jql, tc.project, got, tc.want)
			}
		})
	}
}

func TestTranslateCascadeDoesNotRetryAmbiguousIntent(t *testing.T) {
	p := &fakeJQLProvider{jql: `project = SUP`}
	_, err := translateCascade(context.Background(), &config.Config{JiraProject: "SUP"}, "issues from the last few months", nil,
		func(*config.Config, []jira.Project) (*nl2jql.Translator, error) { return newTestTranslator(t, p), nil })
	if !errors.Is(err, nl2jql.ErrAmbiguousIntent) {
		t.Fatalf("error = %v, want ErrAmbiguousIntent", err)
	}
	if got := atomic.LoadInt32(&p.calls); got != 0 {
		t.Errorf("fallback provider called %d times for ambiguous input, want 0", got)
	}
}

func TestEnsureProjectScopeReportsMalformedJQL(t *testing.T) {
	const malformed = `status = Open AND (`
	_, err := ensureProjectScope(malformed, &config.Config{JiraProject: "SUP"})
	if err == nil || !strings.Contains(err.Error(), malformed) {
		t.Fatalf("error = %v, want parse error containing offending JQL %q", err, malformed)
	}
}

func TestEnsureProjectScopeQuotesConfiguredProjectAndValidatesSortOnly(t *testing.T) {
	got, err := ensureProjectScope("status = Open", &config.Config{JiraProject: "PROJ OR project = OTHER"})
	if err != nil {
		t.Fatalf("ensureProjectScope hostile project: %v", err)
	}
	want := `status = Open AND project = "PROJ OR project = OTHER"`
	if got != want {
		t.Fatalf("JQL = %q, want safely quoted project value %q", got, want)
	}
	if _, err := ensureProjectScope("ORDER BY created DESC", &config.Config{}); err == nil {
		t.Fatal("sort-only JQL without a default project passed validation")
	}
}

// TestGenerateJQLJSONFallbackScopesProject locks in that the JSON-prompt LLM
// tier -- reached when the strict FallbackEngine refuses -- hard-scopes the
// tenant's default project even when the model returns JQL with no project
// predicate, i.e. scoping no longer depends on QuerySystemPrompt rule #1.
func TestGenerateJQLJSONFallbackScopesProject(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var content string
		if n == 1 {
			content = "EMPTY" // strict fallback refuses, cascading to the JSON path
		} else {
			content = `{"jql":"status = Open"}`
		}
		_, _ = io.WriteString(w, openAIJSON(content))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs")
	if err != nil {
		t.Fatalf("GenerateJQL: %v", err)
	}
	if res.JQL != "status = Open AND project = SUP" {
		t.Errorf("JQL = %q, want %q (default project appended even when the LLM omits it)", res.JQL, "status = Open AND project = SUP")
	}
	if res.Route != RouteJSON {
		t.Errorf("route = %q, want %q", res.Route, RouteJSON)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("LLM reached %d times, want 2 (strict fallback + JSON fallback)", n)
	}
}

// GenerateJQLForDigest (gap B, option 2): a window the regex reads off the query
// wins over whatever "days" the LLM JSON path would have produced.
func TestGenerateJQLForDigestExtractsDays(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var content string
		if n == 1 {
			content = "EMPTY" // strict fallback refuses, cascading to the JSON path
		} else {
			content = `{"jql":"status = Open","days":30}`
		}
		_, _ = io.WriteString(w, openAIJSON(content))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQLForDigest(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs in the last 3 days")
	if err != nil {
		t.Fatalf("GenerateJQLForDigest: %v", err)
	}
	if res.Days != 3 {
		t.Errorf("Days = %d, want 3 (ExtractDays wins over the JSON path's 30)", res.Days)
	}
	if res.Route != RouteJSON {
		t.Errorf("route = %q, want %q (digest JSON-prompt LLM path)", res.Route, RouteJSON)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("LLM reached %d times, want 2 (strict fallback + JSON fallback)", n)
	}
}

// GenerateJQLForDigest (gap B consistency): a window-less query the fast path
// can parse now routes through GenerateJQL's fast-first pipeline instead of
// skipping it, so it yields the same JQL as jira_query and never touches the LLM.
func TestGenerateJQLForDigestWindowlessFastPath(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, openAIJSON(`status = Open`))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQLForDigest(context.Background(), &config.Config{JiraProject: "SUP"}, "my open bugs in Support")
	if err != nil {
		t.Fatalf("GenerateJQLForDigest: %v", err)
	}
	want := `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`
	if res.JQL != want {
		t.Errorf("JQL = %q, want %q", res.JQL, want)
	}
	if res.Route != "fast_path" {
		t.Errorf("route = %q, want fast_path", res.Route)
	}
	if res.Days != 0 {
		t.Errorf("Days = %d, want 0 (fast path bakes no separate window)", res.Days)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("LLM reached %d times, want 0 (window-less fast path)", n)
	}
}

// GenerateJQLForDigest (gap B, option 1): when the regex finds no window AND the
// strict fallback refuses, the window is recovered by the JSON LLM path, reached
// through GenerateJQL's own cascade rather than a direct call.
func TestGenerateJQLForDigestWindowlessJSONFallback(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var content string
		if n == 1 {
			content = "EMPTY" // strict fallback refuses, cascading to the JSON path
		} else {
			content = `{"jql":"status = Open","days":7}`
		}
		_, _ = io.WriteString(w, openAIJSON(content))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	res, err := GenerateJQLForDigest(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs")
	if err != nil {
		t.Fatalf("GenerateJQLForDigest: %v", err)
	}
	if res.JQL != "status = Open AND project = SUP" {
		t.Errorf("JQL = %q, want %q (JSON tier hard-scopes the default project)", res.JQL, "status = Open AND project = SUP")
	}
	if res.Days != 7 {
		t.Errorf("Days = %d, want 7 (extracted by the JSON fallback)", res.Days)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("LLM reached %d times, want 2 (strict fallback + JSON fallback)", n)
	}
}

// TestGenerateJQLForDigestWindowRetriesJSON locks in the Issue-#2 fix: when the
// regex reads a window off the query but every tier of GenerateJQLWithProjects
// fails (fast path rejects, the strict FallbackEngine refuses, and its own JSON
// last resort errors), GenerateJQLForDigestWithProjects must not hard-fail. It
// retries generateJQLLLM for the JQL and keeps the regex-derived window.
func TestGenerateJQLForDigestWindowRetriesJSON(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var content string
		switch n {
		case 1:
			// strict FallbackEngine refuses, cascading to the JSON path
			content = "EMPTY"
		case 2:
			// JSON path inside GenerateJQLWithProjects fails (not JSON), so the
			// windowed call must now fall through to the last-resort retry below.
			content = "I cannot translate that"
		default:
			// last-resort retry: a real JSON answer; its days (99) must be
			// overridden by the regex window (3), not the model's.
			content = `{"jql":"status = Open","days":99}`
		}
		_, _ = io.WriteString(w, openAIJSON(content))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)

	// "external" is not in the vocabulary so the fast path rejects; "in the last 3
	// days" is what ExtractDays reads as the window (3).
	res, err := GenerateJQLForDigest(context.Background(), &config.Config{JiraProject: "SUP"}, "external bugs in the last 3 days")
	if err != nil {
		t.Fatalf("GenerateJQLForDigest: %v (windowed translation must not hard-fail)", err)
	}
	if res.JQL != "status = Open AND project = SUP" {
		t.Errorf("JQL = %q, want %q (JSON retry hard-scopes the default project)", res.JQL, "status = Open AND project = SUP")
	}
	if res.Days != 3 {
		t.Errorf("Days = %d, want 3 (regex window wins over the retry's 99)", res.Days)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("LLM reached %d times, want 3 (strict fallback + JSON failure + retry)", n)
	}
}

// --- TranslatorCache (Issue #4) ---
//
// These lock in the payoff of the scoped engine cache: the expensive
// FallbackEngine (and its lazily constructed LLM provider) is built once per
// provider signature and shared across requests, not rebuilt per request. The
// build field is injectable so a test can count engine construction without a
// real LLM: the fast path never reaches the provider, and the injected
// fallback returns canned JQL.

// TestTranslatorCache_ReusesEngine proves the engine is built once for a
// signature and reused, and that a differing signature forces a new build.
func TestTranslatorCache_ReusesEngine(t *testing.T) {
	var builds int32
	cache := &TranslatorCache{
		engines: make(map[string]*nl2jql.FallbackEngine),
		build: func(cfg *config.Config) (*nl2jql.FallbackEngine, error) {
			atomic.AddInt32(&builds, 1)
			return nl2jql.NewFallbackEngine(&fakeJQLProvider{jql: "status = Open"})
		},
	}
	cfg := &config.Config{JiraProject: "SUP"}

	// Two requests with the same provider signature reuse one engine.
	if _, err := cache.GenerateJQL(context.Background(), cfg, "my open bugs in SUP", nil); err != nil {
		t.Fatalf("GenerateJQL: %v", err)
	}
	if _, err := cache.GenerateJQL(context.Background(), cfg, "my open bugs in SUP", nil); err != nil {
		t.Fatalf("GenerateJQL (2nd): %v", err)
	}
	if n := atomic.LoadInt32(&builds); n != 1 {
		t.Errorf("build called %d times, want 1 (same signature reuses the engine)", n)
	}

	// A different Vertex project changes the signature, forcing a new build.
	other := &config.Config{JiraProject: "SUP", VertexProjectID: "other-project"}
	if _, err := cache.GenerateJQL(context.Background(), other, "my open bugs in SUP", nil); err != nil {
		t.Fatalf("GenerateJQL (3rd): %v", err)
	}
	if n := atomic.LoadInt32(&builds); n != 2 {
		t.Errorf("build called %d times, want 2 (a new signature builds a new engine)", n)
	}
}

// TestTranslatorCache_DigestReusesEngine proves the digest path shares the
// same signature-cached engine as the query path.
func TestTranslatorCache_DigestReusesEngine(t *testing.T) {
	var builds int32
	cache := &TranslatorCache{
		engines: make(map[string]*nl2jql.FallbackEngine),
		build: func(cfg *config.Config) (*nl2jql.FallbackEngine, error) {
			atomic.AddInt32(&builds, 1)
			return nl2jql.NewFallbackEngine(&fakeJQLProvider{jql: "status = Open"})
		},
	}
	cfg := &config.Config{JiraProject: "SUP"}

	// A window-less digest query reuses the same engine as the query path.
	if _, err := cache.GenerateJQLForDigest(context.Background(), cfg, "my open bugs in SUP", nil); err != nil {
		t.Fatalf("GenerateJQLForDigest: %v", err)
	}
	if _, err := cache.GenerateJQLForDigest(context.Background(), cfg, "my open bugs in SUP", nil); err != nil {
		t.Fatalf("GenerateJQLForDigest (2nd): %v", err)
	}
	if n := atomic.LoadInt32(&builds); n != 1 {
		t.Errorf("build called %d times, want 1 (digest path reuses the engine)", n)
	}
}

// TestTranslatorCache_RetriesBuildError proves a failed build is not cached:
// the next request retries rather than serving a poisoned entry.
func TestTranslatorCache_RetriesBuildError(t *testing.T) {
	var attempts int32
	cache := &TranslatorCache{
		engines: make(map[string]*nl2jql.FallbackEngine),
		build: func(cfg *config.Config) (*nl2jql.FallbackEngine, error) {
			if atomic.AddInt32(&attempts, 1) == 1 {
				return nil, errors.New("transient provider setup failure")
			}
			return nl2jql.NewFallbackEngine(&fakeJQLProvider{jql: "status = Open"})
		},
	}
	cfg := &config.Config{JiraProject: "SUP"}

	// First call's build fails and surfaces an error (it must not be cached).
	if _, err := cache.GenerateJQL(context.Background(), cfg, "my open bugs in SUP", nil); err == nil {
		t.Fatalf("expected the first build error to surface")
	}
	// Second call retries the build and succeeds.
	if _, err := cache.GenerateJQL(context.Background(), cfg, "my open bugs in SUP", nil); err != nil {
		t.Fatalf("expected the retried build to succeed: %v", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("build attempted %d times, want 2 (a build error must not be cached)", n)
	}
}

// TestProviderSignatureNilAndDistinct pins the cache-key contract: a nil
// config yields a fixed empty key, identical configs collide, and a differing
// Vertex project produces a distinct key.
func TestProviderSignatureNilAndDistinct(t *testing.T) {
	if providerSignature(nil) != "" {
		t.Errorf("providerSignature(nil) = %q, want empty", providerSignature(nil))
	}
	a := providerSignature(&config.Config{JiraProject: "SUP"})
	b := providerSignature(&config.Config{JiraProject: "SUP"})
	if a != b {
		t.Errorf("equal configs gave different signatures: %q vs %q", a, b)
	}
	c := providerSignature(&config.Config{JiraProject: "SUP", VertexProjectID: "other-project"})
	if a == c {
		t.Errorf("differing VertexProjectID did not change the signature")
	}
}

// --- JIRA-list layer (Issue #5) ---
//
// The env-only vocabulary tests above are thorough; these pin the second half:
// folding a fetched project list into the normalizer so a human name like
// "Support" resolves to "SUP" with no JIRA_PROJECT_ALIASES entry, and the
// ResolveProjectMap / FetchProjects integration the production call sites use.

// buildProjectMapWithProjects layers a fetched project list on top of the
// env-only map: each project's key + display name (and lower-case variant)
// binds to the canonical key, so "Support" -> "SUP".
func TestBuildProjectMapWithProjects(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	projects := []jira.Project{
		{Key: "SUP", Name: "Support", ProjectType: "serviceDesks"},
		{Key: "ENG", Name: "Engineering", ProjectType: "software"},
		{Key: "PLT", Name: "Platform", ProjectType: "software"},
	}
	m := buildProjectMapWithProjects(&config.Config{JiraProject: "SUP"}, projects)

	// Display names (and their lower-case variants) resolve to their key.
	want := map[string]string{
		"Support": "SUP", "support": "SUP",
		"Engineering": "ENG", "engineering": "ENG",
		"Platform": "PLT", "platform": "PLT",
	}
	for name, key := range want {
		if got := m[name]; got != key {
			t.Errorf("%q -> %q, want %q", name, got, key)
		}
	}
	// Keys are still registered (and their lower-case variants).
	if m["SUP"] != "SUP" || m["sup"] != "SUP" {
		t.Errorf("key variants missing: %v", m)
	}
}

// A nil or empty project list leaves the env-only map unchanged (nil when the
// config has no project key).
func TestBuildProjectMapWithProjects_NilIsEnvOnly(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	if got := buildProjectMapWithProjects(&config.Config{}, nil); got != nil {
		t.Errorf("nil projects + empty config should yield nil map, got %v", got)
	}
	if got := buildProjectMapWithProjects(&config.Config{}, []jira.Project{}); got != nil {
		t.Errorf("empty projects + empty config should yield nil map, got %v", got)
	}

	env := buildProjectMap(&config.Config{JiraProject: "SUP"})
	if got := buildProjectMapWithProjects(&config.Config{JiraProject: "SUP"}, nil); len(got) != len(env) {
		t.Errorf("nil projects must not change the env-only map: got %v want %v", got, env)
	}
}

// A display name written in the configured key's casing still binds to the
// canonical key, and a display name equal to its key contributes nothing extra.
func TestBuildProjectMapWithProjects_CanonicalAndIdentity(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	// "sup" as a display name must bind to the configured "SUP"; a project whose
	// name equals its key adds no new binding beyond the key variants.
	projects := []jira.Project{
		{Key: "SUP", Name: "sup"},
		{Key: "ENG", Name: "ENG"},
	}
	m := buildProjectMapWithProjects(&config.Config{JiraProject: "SUP"}, projects)
	if m["sup"] != "SUP" {
		t.Errorf("lower-case display name 'sup' -> %q, want SUP", m["sup"])
	}
	if m["ENG"] != "ENG" {
		t.Errorf("identity display name 'ENG' -> %q, want ENG", m["ENG"])
	}
}

// ResolveProjectMap folds a fetched project list from the cache so "Support"
// resolves to "SUP" without JIRA_PROJECT_ALIASES; a fetch failure or missing
// cache degrades to the env-only vocabulary so a warm-up never fails a query.
func TestResolveProjectMap(t *testing.T) {
	t.Setenv("JIRA_PROJECT_KEYS", "")
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	// A nil cache or fetcher degrades to the env-only map.
	if m := ResolveProjectMap(&config.Config{JiraProject: "SUP"}, nil, nil, time.Hour); m["SUP"] != "SUP" {
		t.Errorf("nil cache: env-only map expected, got %v", m)
	}
	if m := ResolveProjectMap(&config.Config{JiraProject: "SUP"}, openLLMTestCache(t), nil, time.Hour); m["Support"] != "" {
		t.Errorf("nil fetcher: should not resolve fetched names, got %v", m)
	}

	// With a fetcher, fetched display names resolve to their key.
	fetcher := &llmProjectFetcher{projects: []jira.Project{{Key: "SUP", Name: "Support"}}}
	c := openLLMTestCache(t)
	m := ResolveProjectMap(&config.Config{JiraProject: "SUP"}, c, fetcher, time.Hour)
	if m["Support"] != "SUP" {
		t.Errorf("fetched name 'Support' -> %q, want SUP (map=%v)", m["Support"], m)
	}

	// A fetch error degrades to the env-only vocabulary, not an empty map.
	fetcher.err = errors.New("network down")
	if m := ResolveProjectMap(&config.Config{JiraProject: "SUP"}, c, fetcher, time.Hour); m["SUP"] != "SUP" {
		t.Errorf("fetch error should degrade to env-only, got %v", m)
	}
}

// llmProjectFetcher satisfies cache.ProjectFetcher for the ResolveProjectMap
// integration test.
type llmProjectFetcher struct {
	projects []jira.Project
	err      error
	calls    int
}

func (f *llmProjectFetcher) ListProjects() ([]jira.Project, error) {
	f.calls++
	return f.projects, f.err
}

// openLLMTestCache returns a real SQLite cache rooted at a temp HOME so the
// ResolveProjectMap integration test exercises the real cache path.
func openLLMTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	c, err := cache.OpenForConfig(&config.Config{JiraServer: "https://test.atlassian.net", JiraEmail: "test@test.com"})
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// GenerateJQLWithProjects with a non-nil list resolves a human project name on
// the fast path, matching what a JIRA_PROJECT_ALIASES entry would do -- and,
// like the fast path, it never reaches the LLM.
func TestGenerateJQLWithProjectsResolvesHumanName(t *testing.T) {
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	projects := []jira.Project{{Key: "SUP", Name: "Support"}}
	res, err := GenerateJQLWithProjects(context.Background(), &config.Config{JiraProject: "SUP"}, "my open bugs in Support", projects)
	if err != nil {
		t.Fatalf("GenerateJQLWithProjects: %v", err)
	}
	// With the list, "Support" canonicalizes to its key "SUP" on the fast path.
	want := `project = SUP AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`
	if res.JQL != want {
		t.Errorf("JQL = %q, want %q", res.JQL, want)
	}
	if res.Route != RouteFastPath {
		t.Errorf("route = %q, want fast_path (the list resolves the name without the LLM)", res.Route)
	}
}

func TestGenerateJQLStoryPointsFastPathKeepsDefaultProjectScope(t *testing.T) {
	const query = "list all my work items with story points > 3?"
	res, err := GenerateJQL(context.Background(), &config.Config{JiraProject: "SUP"}, query)
	if err != nil {
		t.Fatalf("GenerateJQL: %v", err)
	}
	if res.Route != nl2jql.RouteFastPath {
		t.Errorf("route = %q, want %q", res.Route, nl2jql.RouteFastPath)
	}
	want := `assignee = currentUser() AND "Story Points" > 3 AND project = SUP`
	if res.JQL != want {
		t.Errorf("JQL = %q, want %q", res.JQL, want)
	}
}

// Without the project list, the human name is emitted literally (no
// canonicalization), demonstrating that the list -- not the LLM -- is what maps
// "Support" to the configured key "SUP" on the fast path.
func TestGenerateJQLWithProjects_NilListKeepsHumanNameLiteral(t *testing.T) {
	t.Setenv("JIRA_PROJECT_ALIASES", "")
	res, err := GenerateJQLWithProjects(context.Background(), &config.Config{JiraProject: "SUP"}, "my open bugs in Support", nil)
	if err != nil {
		t.Fatalf("GenerateJQLWithProjects: %v", err)
	}
	if res.Route != RouteFastPath {
		t.Errorf("route = %q, want fast_path", res.Route)
	}
	// No list/alias => "Support" is not canonicalized to "SUP".
	want := `project = Support AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`
	if res.JQL != want {
		t.Errorf("JQL = %q, want %q (the name must stay literal without a list)", res.JQL, want)
	}
}

// TestGenerateJQLForDigestWithProjectsResolvesHumanName mirrors
// TestGenerateJQLWithProjectsResolvesHumanName for the digest entry point: the
// windowed variant must also fold the fetched project list into the normalizer's
// vocabulary so a human name resolves to its key on the fast path, and the
// digestCascade window logic is untouched by that resolution.
func TestGenerateJQLForDigestWithProjectsResolvesHumanName(t *testing.T) {
	t.Setenv("JIRA_PROJECT_ALIASES", "")

	projects := []jira.Project{{Key: "SUP", Name: "Support"}}
	res, err := GenerateJQLForDigestWithProjects(context.Background(), &config.Config{JiraProject: "SUP"}, "my open bugs in Support", projects)
	if err != nil {
		t.Fatalf("GenerateJQLForDigestWithProjects: %v", err)
	}
	// With the list, "Support" canonicalizes to its key "SUP" on the fast path,
	// and a window-less query bakes no separate comment window.
	want := `project = SUP AND issuetype = Bug AND statusCategory != Done AND assignee = currentUser()`
	if res.JQL != want {
		t.Errorf("JQL = %q, want %q", res.JQL, want)
	}
	if res.Route != RouteFastPath {
		t.Errorf("route = %q, want fast_path (the list resolves the name without the LLM)", res.Route)
	}
	if res.Days != 0 {
		t.Errorf("Days = %d, want 0 (window-less query bakes no separate window)", res.Days)
	}
}

func TestMalformedGeneratedJQLDoesNotRetryDigest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, openAIJSON(`status = Open ORDER BY updated DESC AND project = OTHER`))
	}))
	defer srv.Close()
	withOpenAIEnv(t, srv.URL)
	_, err := GenerateJQLForDigest(context.Background(), &config.Config{JiraProject: "PROJ"}, "external bugs")
	if !errors.Is(err, ErrInvalidGeneratedJQL) {
		t.Fatalf("want generated-query error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want one terminal parse failure", calls.Load())
	}
}
