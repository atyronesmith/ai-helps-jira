package mcpserver

import (
	"fmt"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"

	"github.com/atyronesmith/ai-helps-jira/internal/format"
)

// AttemptResultData stores bounded lifecycle metadata for tools without a query
// trace. Arguments and upstream errors are deliberately excluded.
type AttemptResultData struct {
	Tool          string
	Outcome       string
	ErrorCategory string
	StartedAt     time.Time
	CompletedAt   time.Time
	Duration      time.Duration
	Trace         *QueryResultData `json:",omitempty"`
}

// SummaryResult holds summary data for the web template.
type SummaryResult struct {
	Boards     []jira.BoardInfo
	OpenIssues []jira.Issue
	Project    string
	User       string
	JiraServer string
	FetchedAt  time.Time
}

// QueryResultData holds query results for the web template.
type QueryResultData struct {
	Query           string
	NormalizedInput string
	// JQL retains the legacy display field. Execution evidence is separate and
	// remains empty when validation/translation prevented a search attempt.
	GeneratedJQL         string
	ExecutedJQL          string
	DisplayJQL           string
	Outcome              string
	ErrorStage           string
	ErrorCategory        string
	Validation           string
	StartedAt            time.Time
	CompletedAt          time.Time
	Duration             time.Duration
	Route                string
	JQL                  string
	Issues               []jira.Issue
	Search               *jira.SearchInfo
	SearchError          string
	Activity             *jira.ActivityResult
	ActivityError        string
	ActivityYearInferred bool
	ActivityDisplayLimit int
	User                 string
	JiraServer           string
	QueriedAt            time.Time
}

// Metadata keeps missing or incomplete metadata explicitly unknown. Returned
// row counts alone never establish a matching total.
func (q *QueryResultData) Metadata() jira.SearchInfo {
	if q == nil {
		return jira.SearchInfo{TotalKind: jira.CountUnknown}
	}
	if q.Search == nil {
		return jira.SearchInfo{Returned: len(q.Issues), TotalKind: jira.CountUnknown}
	}
	info := *q.Search
	if info.TotalKind == "" {
		info.TotalKind = jira.CountUnknown
	}
	if info.Returned == 0 && len(q.Issues) > 0 {
		info.Returned = len(q.Issues)
	}
	return info
}

func (q *QueryResultData) Summary() string {
	if q != nil && q.Activity != nil {
		outcome := "partial"
		if q.Outcome == "success" && q.Activity.CandidateComplete && q.Activity.Complete {
			outcome = "complete"
		}
		summary := fmt.Sprintf("Activity lookup: %d observed matching issues from %d candidates (%s)", len(q.Activity.Matches), q.Activity.Candidates, outcome)
		if q.ActivityError != "" {
			summary += " | Activity error: " + q.ActivityError
		}
		return summary
	}
	summary := format.SearchSummary(q.Metadata())
	if q != nil && q.SearchError != "" {
		summary += " | Search error: " + q.SearchError
	}
	return summary
}

func (q *QueryResultData) ActivityText() string {
	if q == nil || q.Activity == nil {
		return ""
	}
	text := format.RenderActivity(*q.Activity, q.ActivityDisplayLimit)
	if q.ActivityYearInferred && len(q.Activity.StartDate) >= 4 {
		text = fmt.Sprintf("Years omitted; both dates were interpreted in %s.\n\n", q.Activity.StartDate[:4]) + text
	}
	return text
}

func (q *QueryResultData) HasMore() bool {
	info := q.Metadata()
	return info.HasMore != nil && *info.HasMore
}

// DigestResultData holds digest data for the web template.
type DigestResultData struct {
	ParentKey     string
	ParentSummary string
	ParentType    string
	Digest        *format.DigestData
	Links         []jira.IssueLink
	User          string
	JiraServer    string
	GeneratedAt   time.Time
}

// EnrichResultData holds enrichment data for the web template.
type EnrichResultData struct {
	Issue      *jira.IssueDetail
	Enrichment *llm.EnrichmentContent
	Applied    bool
	JiraServer string
	EnrichedAt time.Time
}

// CreateEpicResultData holds epic creation data for the web template.
type CreateEpicResultData struct {
	CreatedKey string
	Epic       *llm.EpicContent
	JiraServer string
	CreatedAt  time.Time
}

// WeeklyStatusResultData holds weekly status report data for the web template.
type WeeklyStatusResultData struct {
	UserName    string
	StartDate   string
	EndDate     string
	Projects    []llm.WeeklyProjectItem
	JiraServer  string
	GeneratedAt time.Time
}

// FindSimilarResultData holds similarity analysis results for the web template.
type FindSimilarResultData struct {
	TargetKey  string
	TargetText string
	Matches    []llm.SimilarIssue
	JiraServer string
	FoundAt    time.Time
}

// ConfluenceAnalyticsResultData holds Confluence page view analytics for the web template.
type ConfluenceAnalyticsResultData struct {
	ParentTitle string
	Pages       []ConfluencePageStats
	JiraServer  string
	FetchedAt   time.Time
}

// ConfluencePageStats holds view stats for a single Confluence page.
type ConfluencePageStats struct {
	PageID        string
	Title         string
	TotalViews    int
	UniqueViewers int
}

// queryMetaView is a lightweight projection of a query result's natural-language
// query, translation route, and generated JQL. The results index uses it to
// list the query and its generated JQL without the full issue payload.
type queryMetaView struct {
	Query         string
	Route         string
	JQL           string
	Outcome       string
	ErrorStage    string
	ErrorCategory string
}
