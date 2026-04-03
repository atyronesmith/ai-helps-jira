package mcpserver

import (
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/format"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
)

// SummaryResult holds summary data for the web template.
type SummaryResult struct {
	Boards     []jira.BoardInfo
	OpenIssues []jira.Issue
	Project    string
	JiraServer string
	FetchedAt  time.Time
}

// QueryResultData holds query results for the web template.
type QueryResultData struct {
	Query      string
	JQL        string
	Issues     []jira.Issue
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
