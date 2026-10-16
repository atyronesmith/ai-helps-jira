package format

import (
	"fmt"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// SearchSummary describes returned rows, count evidence, and completeness
// without treating a page length or Jira estimate as a matching total.
func SearchSummary(info jira.SearchInfo) string {
	rows := fmt.Sprintf("Returned rows: %d", info.Returned)
	if info.Enumerated > info.Returned && info.TotalKind != jira.CountExact {
		rows += fmt.Sprintf(" | Observed matches: at least %d", info.Enumerated)
	}
	count := "Matching total: unknown"
	if info.Total != nil {
		switch info.TotalKind {
		case jira.CountExact:
			count = fmt.Sprintf("Exact matching total: %d", *info.Total)
		case jira.CountReported:
			count = fmt.Sprintf("Jira-reported matching total (unverified): %d", *info.Total)
		case jira.CountEstimated:
			count = fmt.Sprintf("Estimated matching total: %d", *info.Total)
		}
	}
	completion := "Completeness: partial; whether more matches exist is unknown"
	if info.Complete {
		completion = "Completeness: complete"
	} else if info.HasMore != nil {
		if *info.HasMore {
			completion = "Completeness: partial; more matches known"
		} else {
			completion = "Completeness: partial; no more matches reported"
		}
	}
	if info.StopReason == "result_limit" {
		completion += "; preview limit reached"
	} else if info.StopReason != "" {
		completion += "; stopped: " + info.StopReason
	}
	return rows + " | " + count + " | " + completion
}
