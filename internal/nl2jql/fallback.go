package nl2jql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/translationerror"
)

// Provider must respect cancellation. A translation invokes Generate once only.
type Provider interface {
	Generate(context.Context, string, string) (string, error)
}

var (
	ErrNilProvider     = errors.New("nl2jql: LLM provider must not be nil")
	ErrEmptyQuery      = errors.New("nl2jql: natural query is empty")
	ErrEmptyResponse   = errors.New("nl2jql: empty model response")
	ErrUnmappable      = errors.New("nl2jql: unsupported intent")
	ErrResponseTooLong = errors.New("nl2jql: model response exceeds size budget")
)

const defaultMaxResponseBytes = 32768
const maxFallbackPromptBytes = 128 << 10
const maxFallbackInputBytes = 16 << 10

// There is one response contract, with data operands rather than executable JQL.
const systemPromptTemplate = `Translate the whole request into typed query intent. Reply ONLY with a JSON object:
{"version":1,"outcome":"supported","mode":"list","filter":{"kind":"comparison","field":"statusCategory","operator":"!=","value":{"kind":"string","text":"Done"}},"sort":[]}
Use outcome unsupported for intent you cannot represent; ambiguous when clarification is needed. In either case omit filter and sort. Never drop a constraint.
mode: list, count, recommendation. filter kinds: and/or (children array), not (one child), comparison (field, operator, value or values for IN/NOT IN), owner (value kind self or string), history (status, actor kind self/string, start/end ISO dates for closing transitions).
Operators: = != > >= < <= ~ !~ IN NOT IN IS IS NOT. IS operands have kind empty. Values: {kind:string,text:...}, {kind:number,text:...}, {kind:boolean,text:true/false}, {kind:self}, {kind:relative_days,days:positive integer}, {kind:calendar_date,text:YYYY-MM-DD}, {kind:calendar_function,text:now/startOfDay/startOfWeek/startOfMonth/endOfDay/endOfWeek/endOfMonth,offset:integer}.
No raw JQL, function calls or quoting in text values. sort: [{field:...,direction:ASC/DESC}]. Preserve Boolean groups and sorting exactly. Do not invent ordering. When a requested sort omits direction, priority, updated, numeric and duration fields default to DESC; summary/title and other date fields default to ASC. Explicit directions override these defaults.
Use self for my/me/mine including history actors; the caller supplies the effective identity.
Requests about items someone 'worked on' need clarification of the activity evidence. Never substitute current assignee and updated timestamps for comments, logged work or changes performed by that person.
Requests for open, unresolved, active, unfinished, or not-done work mean statusCategory != Done. Never substitute an issuetype != ... predicate for a requested status filter.
Relative last/past N days/weeks use inclusive lower bounds through now; calendar periods use inclusive start and exclusive next start. Do not approximate months or sprints with day counts. Search dates must name an issue date field. Comment windows are handled separately by the caller.
Explicit calendar-date ranges include the final day: represent 'between START and END' using separate comparison nodes joined by and, with >= START and < the day after END. BETWEEN is not an operator. Never repeat JSON object keys.
Fields must be core fields or fields in the supplied catalog. Prefer exact field IDs; never invent custom IDs, types or applicability.
SCHEMA:
%s`

type FallbackEngine struct {
	provider         Provider
	MaxResponseBytes int
}

func NewFallbackEngine(p Provider) (*FallbackEngine, error) {
	if p == nil {
		return nil, ErrNilProvider
	}
	return &FallbackEngine{provider: p}, nil
}
func (e *FallbackEngine) Generate(ctx context.Context, query string, schema map[string]string) (string, error) {
	return e.generate(ctx, query, schema, fieldmeta.New(nil, nil), "", "")
}
func (e *FallbackEngine) generate(ctx context.Context, query string, schema map[string]string, resolver *fieldmeta.Resolver, project, self string) (string, error) {
	if e == nil || e.provider == nil {
		return "", ErrNilProvider
	}
	if strings.TrimSpace(query) == "" {
		return "", ErrEmptyQuery
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", classifiedFailure(err)
	}
	system := buildSystemPrompt(schema)
	if len(query) > maxFallbackInputBytes || len(system) > maxFallbackPromptBytes {
		return "", translationerror.New(translationerror.Unsupported, errors.New("translation input or metadata exceeds fallback prompt budget"))
	}
	raw, err := e.provider.Generate(ctx, system, query)
	if err != nil {
		return "", classifiedFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return "", classifiedFailure(err)
	}
	limit := e.MaxResponseBytes
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	if len(raw) > limit {
		return "", translationerror.New(translationerror.InvalidOutput, ErrResponseTooLong)
	}
	if strings.TrimSpace(raw) == "" {
		return "", translationerror.New(translationerror.InvalidOutput, ErrEmptyResponse)
	}
	if err := uniqueJSONKeys(raw); err != nil {
		return "", invalidOutput("duplicate keys or excessive JSON nesting")
	}
	var response ModelPlan
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return "", invalidOutput("expected typed intent JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", invalidOutput("trailing model output")
	}
	plan, err := response.plan(resolver, project, self)
	if err != nil {
		if errors.Is(err, fieldmeta.ErrMissingMetadata) || errors.Is(err, fieldmeta.ErrAmbiguousField) || errors.Is(err, fieldmeta.ErrInapplicableField) || errors.Is(err, ErrAmbiguousIntent) {
			return "", translationerror.New(translationerror.Unsupported, err)
		}
		return "", err
	}
	if err := verifyFallbackRequest(query, plan, resolver, project, self); err != nil {
		return "", err
	}
	tree, err := plan.Compile()
	if err != nil {
		return "", invalidOutput("cannot compile plan")
	}
	return tree.Render(), nil
}
func invalidOutput(message string) error {
	return translationerror.New(translationerror.InvalidOutput, errors.New(message))
}
func buildSystemPrompt(schema map[string]string) string {
	return fmt.Sprintf(systemPromptTemplate, renderSchema(schema))
}
func renderSchema(schema map[string]string) string {
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s -> %s\n", k, schema[k])
	}
	return b.String()
}

func classifiedFailure(err error) error {
	kind := translationerror.KindOf(err)
	if kind == "" {
		kind = translationerror.Connectivity
	}
	return translationerror.New(kind, err)
}
