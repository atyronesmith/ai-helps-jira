package nl2jql

import (
	"regexp"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

var leadingSelfRequest = regexp.MustCompile(`(?i)^\s*(?:(?:show|find|list|get|give|fetch|bring|see|count|how|many|all|of|the|me|please|retrieve|search|locate|display|return|which|what)\s+)*(?:my|mine|myself)\b`)
var explicitSortRequest = regexp.MustCompile(`(?i)\b(?:and\s+)?(?:order|ordered|sorted)\s+by\b|\bin\s+order\s+of\s+when\b`)

// These bounded guards verify recognizable constraints even when unsupported
// vocabulary requires a model. Reject changed meaning rather than silently
// adding a predicate to arbitrary Boolean intent. This is not a general
// semantic proof for all fallback vocabulary.
func verifyFallbackRequest(input string, plan *QueryPlan, resolver *fieldmeta.Resolver, project, self string) error {
	masked := maskFallbackLiterals(input)
	if leadingSelfRequest.MatchString(masked) && !requiresSelfOwner(plan.Filter, self) {
		return invalidOutput("translation did not preserve the requested ownership in every Boolean branch")
	}
	frames := explicitSortRequest.FindAllStringIndex(masked, -1)
	if len(frames) != 1 {
		return nil
	}
	expected, err := ParsePlan(input[frames[0][0]:], self, time.Time{})
	if err != nil || len(expected.Sort) == 0 {
		// Existing typed catalog validation still applies to model-only field
		// vocabulary whose ordering the closed grammar cannot establish.
		return nil
	}
	expected.Filter = plan.Filter
	if err := resolvePlanFields(expected, resolver, project); err != nil {
		return invalidOutput("cannot verify the requested ordering against cached metadata")
	}
	if len(expected.Sort) != len(plan.Sort) {
		return invalidOutput("translation did not preserve the requested ordering")
	}
	for n, want := range expected.Sort {
		got := plan.Sort[n]
		if canonicalSortField(want.Field) != canonicalSortField(got.Field) || !strings.EqualFold(want.Direction, got.Direction) {
			return invalidOutput("translation did not preserve the requested ordering")
		}
	}
	return nil
}

func canonicalSortField(field string) string {
	field = unquoteField(field)
	if meta, ok := fieldmeta.Core(field); ok {
		return strings.ToLower(meta.ID)
	}
	return strings.ToLower(field)
}

func maskFallbackLiterals(input string) string {
	masked := []byte(input)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			masked[i] = ' '
		}
	}
	if start, end, ok := textLiteralSpan(input); ok {
		blank(start, end)
	}
	for _, span := range keywordTokens.FindAllStringIndex(input, -1) {
		token := input[span[0]:span[1]]
		if strings.HasPrefix(token, `"`) || strings.HasPrefix(token, `'`) || strings.Contains(token, "@") {
			blank(span[0], span[1])
		}
	}
	return string(masked)
}

// A positive owner must restrict every OR branch. A negated owner, a history
// actor, or one owner branch beside an unrestricted branch is not a global
// ownership filter. Configured overrides remain literal operands.
func requiresSelfOwner(intent *Intent, self string) bool {
	if intent == nil {
		return false
	}
	if self == "" {
		self = "currentUser()"
	}
	match := func(value string) bool {
		if value == self {
			return true
		}
		return self != "currentUser()" && unquoteField(value) == unquoteField(self)
	}
	switch intent.Kind {
	case IntentOwner:
		return intent.Owner.Self || match(jql.QuoteIdent(intent.Owner.Name))
	case IntentComparison:
		return strings.EqualFold(intent.Field, "assignee") && ((intent.Operator == "=" && match(intent.Value)) || (intent.Operator == "IN" && intent.Value == "("+self+")"))
	case IntentAll:
		for _, child := range intent.Children {
			if requiresSelfOwner(child, self) {
				return true
			}
		}
	case IntentAny:
		if len(intent.Children) == 0 {
			return false
		}
		for _, child := range intent.Children {
			if !requiresSelfOwner(child, self) {
				return false
			}
		}
		return true
	}
	return false
}
