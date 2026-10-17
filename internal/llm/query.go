package llm

import (
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/nl2jql"
)

type QueryResult struct {
	MetadataFetchedAt          time.Time        `json:"metadata_fetched_at,omitzero"`
	MetadataApplicabilityKnown bool             `json:"metadata_applicability_known"`
	MetadataStale              bool             `json:"metadata_stale,omitempty"`
	JQL                        string           `json:"jql"`
	Route                      string           `json:"route,omitempty"`
	Mode                       nl2jql.QueryMode `json:"mode,omitempty"`
	Days                       int              `json:"days,omitempty"` // optional time window extracted from natural language
	Input                      string           `json:"input,omitempty"`
	NormalizedInput            string           `json:"normalized_input,omitempty"`
	ParseError                 string           `json:"parse_error,omitempty"`
	ProviderCalls              int              `json:"provider_calls"`
	DurationNanos              int64            `json:"duration_nanos"`
}

const (
	RouteFastPath = nl2jql.RouteFastPath
	RouteFallback = nl2jql.RouteFallback
)
