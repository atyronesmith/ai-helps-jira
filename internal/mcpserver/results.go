package mcpserver

import (
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"

	"github.com/atyronesmith/ai-helps-jira/internal/format"
)

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
	Query      string
	JQL        string
	Issues     []jira.Issue
	User       string
	JiraServer string
	QueriedAt  time.Time
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
