package jira

// CountKind distinguishes a completed enumeration from other count sources.
type CountKind string

const (
	CountUnknown   CountKind = "unknown"
	CountExact     CountKind = "exact"
	CountReported  CountKind = "reported"
	CountEstimated CountKind = "estimated"
)

// SearchInfo describes the returned rows independently of the matching count.
// Complete means all matching rows are present. A limited count preview can
// have an exact Total while Complete is false.
// A nil HasMore or Total means unknown. Only completed enumeration is exact.
type SearchInfo struct {
	Returned   int       `json:"returned"`
	Enumerated int       `json:"enumerated"`
	Complete   bool      `json:"complete"`
	HasMore    *bool     `json:"has_more,omitempty"`
	Total      *int      `json:"total,omitempty"`
	TotalKind  CountKind `json:"total_kind"`
	StopReason string    `json:"stop_reason,omitempty"`
}

type SearchResult struct {
	Issues []Issue
	SearchInfo
}

// SearchOptions separates displayed rows, HTTP page size, and execution budget.
// Zero values select 50 displayed rows, 100 rows per page, and at most 100 pages.
// ExactCount continues beyond the displayed-row limit. Every search has an
// overall 60-second deadline, or the caller's earlier cancellation/deadline.
type SearchOptions struct {
	Limit      int
	PageSize   int
	ExactCount bool
	MaxPages   int
}
