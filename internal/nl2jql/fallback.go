// fallback.go implements the non-deterministic safety net of the JQL engine.
//
// Routing contract: the deterministic grammar parser is tried FIRST. Only
// when it rejects a query does control reach this layer:
//
//	jql, err := nl2jql.Parse(q)
//	if err != nil {
//	    jql, err = fallback.Generate(ctx, q, schema) // <- this file
//	}
//
// Guarantees of this layer:
//   - a strict, schema-scoped system prompt forces the model to emit a single
//     JSON object {"jql": "..."} (no markdown, no fences, no prose);
//   - schema rendering is deterministic (sorted keys), so identical
//     vocabulary always produces the identical prompt;
//   - the model response is sanitised defensively, because models are
//     models: a JSON envelope is unwrapped and accidental fences, quotes and labels are stripped, and a raw (non-JSON) response still parses.
package nl2jql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Provider is the narrow contract FallbackEngine needs from the LLM layer at
// github.com/your-org/repo/internal/llm. If the client that package exports
// satisfies it, pass it straight into NewFallbackEngine. If its shape
// differs, adapt it in three lines:
//
//	type llmAdapter struct{ c *llm.Client }
//
//	func (a llmAdapter) Generate(ctx context.Context, sys, user string) (string, error) {
//	    return a.c.Complete(ctx, user, llm.WithSystemPrompt(sys))
//	}
type Provider interface {
	// Generate returns the model's raw text for a (system, user) prompt
	// pair. Implementations MUST respect ctx cancellation and deadlines.
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Sentinel errors surfaced to callers. Use errors.Is to branch on them.
var (
	// ErrNilProvider: the engine was constructed without an LLM client.
	ErrNilProvider = errors.New("nl2jql/fallback: LLM provider must not be nil")
	// ErrEmptyQuery: the caller passed a blank natural-language query.
	ErrEmptyQuery = errors.New("nl2jql/fallback: natural query is empty")
	// ErrEmptyResponse: the model returned nothing usable after sanitising.
	ErrEmptyResponse = errors.New("nl2jql/fallback: LLM returned an empty response")
	// ErrUnmappable: the model declared (via the EMPTY sentinel demanded by
	// the prompt) that it could not map the query onto the schema.
	ErrUnmappable = errors.New("nl2jql/fallback: query cannot be mapped onto the provided schema")
	// ErrResponseTooLong: the completion exceeded MaxResponseBytes; almost
	// certainly a runaway model rather than a real JQL string.
	ErrResponseTooLong = errors.New("nl2jql/fallback: LLM response exceeds size budget")
)

// systemPromptTemplate is deliberately rigid. Every item in the OUTPUT
// CONTRACT exists because, empirically, models will otherwise wrap JQL in
// ```jql fences, quote it, or chat around it. The %s is replaced by the
// rendered schema in buildSystemPrompt.
const systemPromptTemplate = `You are a strict compiler from natural language to Jira Query Language (JQL).

OUTPUT CONTRACT (non-negotiable):
1. Reply with exactly ONE line: a single JSON object of the form {"jql": "<the raw JQL string>"}.
2. No Markdown of any kind: no code fences, no backticks, no bold, no bullets, no headings, and no prose before or after the object.
3. The value of "jql" is the raw JQL string itself: do not wrap that JQL in extra quotes, and escape any embedded double quote inside it as \".
4. Do not emit any field other than "jql".
5. If the input cannot be expressed as JQL using the SCHEMA vocabulary or core Jira fields, reply with exactly: {"jql": "EMPTY"}

COMPILATION RULES:
- Join every predicate with " AND ".
- Emit fields in this fixed order: project, issuetype, status, assignee, priority, resolution, labels, created, updated, duedate, then any SCHEMA fields.
- "me", "my", "mine", "myself" compile to currentUser(). Never invent usernames.
- Relative dates use JQL relative values: -3d, -2w, startOfDay(), startOfWeek().
- Quote a string value with double quotes ONLY when it contains whitespace; escape embedded quotes as \".
- Use ONLY the fields, projects, and values listed in SCHEMA, plus standard Jira fields. Never invent vocabulary.

SCHEMA (authoritative vocabulary for this tenant):
%s

Remember: your entire reply is a single-line JSON object of the form {"jql": "..."} (or {"jql": "EMPTY"}), in json, with no prose.`

// unmappableSentinel is the exact token the prompt demands when the model
// cannot honour the request. Matching is case-insensitive and whitespace-
// tolerant.
const unmappableSentinel = "EMPTY"

// defaultMaxResponseBytes bounds what we accept from the model. Real JQL for
// one question is short; anything bigger is a failure mode worth failing on.
const defaultMaxResponseBytes = 4096

// FallbackEngine converts natural-language Jira questions into JQL using an
// LLM, scoped to a tenant schema. It is immutable after construction and
// safe for concurrent use.
type FallbackEngine struct {
	provider Provider

	// MaxResponseBytes caps the raw model output accepted before
	// sanitising. Zero means defaultMaxResponseBytes.
	MaxResponseBytes int
}

// NewFallbackEngine wires the fallback layer to the repo's LLM client.
func NewFallbackEngine(p Provider) (*FallbackEngine, error) {
	if p == nil {
		return nil, ErrNilProvider
	}
	return &FallbackEngine{provider: p}, nil
}

// Generate translates naturalQuery into JQL.
//
// schema maps human-facing vocabulary to authoritative JQL identifiers, e.g.
//
//	{"support project":      "project = SUP",
//	 "silentInstallEnabled": "cf[10021]"}
//
// It is rendered into the system prompt with sorted keys so the prompt is
// byte-for-byte deterministic for identical input.
func (e *FallbackEngine) Generate(ctx context.Context, naturalQuery string, schema map[string]string) (string, error) {
	if e == nil || e.provider == nil {
		return "", ErrNilProvider
	}
	query := strings.TrimSpace(naturalQuery)
	if query == "" {
		return "", ErrEmptyQuery
	}
	// Fail fast before paying for a completion that is already doomed.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	system := buildSystemPrompt(schema)
	raw, err := e.provider.Generate(ctx, system, query)
	if err != nil {
		return "", fmt.Errorf("nl2jql/fallback: llm: %w", err)
	}

	limit := e.MaxResponseBytes
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	if len(raw) > limit {
		return "", fmt.Errorf("%w: got %d bytes", ErrResponseTooLong, len(raw))
	}

	jql := extractJQL(raw)
	if jql == "" {
		return "", ErrEmptyResponse
	}
	if strings.EqualFold(jql, unmappableSentinel) {
		return "", ErrUnmappable
	}
	return jql, nil
}

// buildSystemPrompt renders the strict system prompt with the tenant schema.
// Keys are sorted so that identical schemas always emit identical prompts —
// important because prompt jitter shifts model behaviour.
func buildSystemPrompt(schema map[string]string) string {
	return fmt.Sprintf(systemPromptTemplate, renderSchema(schema))
}

func renderSchema(schema map[string]string) string {
	if len(schema) == 0 {
		return "(none provided: use only core Jira fields)"
	}
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s -> %s\n", k, schema[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

// sanitizeJQL trims the accidental decorations models love to add even when
// expressly forbidden: whitespace, ```jql fences, stray backticks, "JQL:"
// labels, balanced wrapping quotes, and a trailing period.
func sanitizeJQL(s string) string {
	s = strings.TrimSpace(s)

	// Strip a leading fence (optionally language-tagged: ```jql, ```JQL, ...).
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		} else {
			// Degenerate single-line fence: just drop the backticks.
			s = strings.TrimLeft(s, "`")
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}

	// Any remaining stray backticks or whitespace.
	s = strings.TrimSpace(strings.Trim(s, "`"))

	// Chatty labels: "JQL: status = Open" -> "status = Open".
	for _, prefix := range []string{"jql query:", "jql:", "query:"} {
		if len(s) > len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			break
		}
	}

	// Balanced single-line wrapping quotes: '"status = Open"' -> status = Open.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}

	// A trailing period is never valid JQL, but never strip inside quotes or
	// closing parens.
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, ";") {
		if !strings.HasSuffix(s, `")`) {
			s = strings.TrimRight(s, ".;")
		}
	}
	return strings.TrimSpace(s)
}

// extractJQL recovers the JQL string from a raw model response.
//
// The providers this fallback is wired to (Vertex, OpenAI) force JSON output --
// Vertex prefills the assistant turn with "{" and OpenAI sets response_format
// json_object -- so the model returns the JQL inside a {"jql": "..."} envelope
// even though the prompt asks for a single object. We transparently unwrap that
// envelope. When the response is not JSON -- a raw (non-JSON-forcing) provider,
// or a small model that ignored the request -- we fall back to the historical
// raw-JQL sanitation so that path is unchanged.
func extractJQL(raw string) string {
	s := strings.TrimSpace(raw)
	if obj := firstJSONObject(s); obj != "" {
		var envelope struct {
			JQL   string `json:"jql"`
			Query string `json:"query"`
		}
		if json.Unmarshal([]byte(obj), &envelope) == nil {
			if v := strings.TrimSpace(envelope.JQL); v != "" {
				return v
			}
			if v := strings.TrimSpace(envelope.Query); v != "" {
				return v
			}
			// A well-formed envelope carrying no value is an explicit "no JQL"
			// answer, not raw text to salvage.
			return ""
		}
	}
	if strings.Contains(s, "{") {
		// Looked like JSON but yielded no parseable object: a truncated or
		// malformed envelope. Handing a brace-laden fragment to JIRA would only
		// be rejected, so surface empty and let the caller's cascade retry via
		// the JSON last-resort tier.
		return ""
	}
	return sanitizeJQL(raw)
}

// firstJSONObject returns the first balanced {...} object in s, tracking string
// literals so braces inside string values don't throw off the depth count. It
// returns "" when s contains no balanced object, so surrounding prose, fences,
// or whitespace are ignored.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start == -1 {
		return ""
	}
	depth := 0
	inString := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch c {
			case '\\':
				i++ // skip the escaped character
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
