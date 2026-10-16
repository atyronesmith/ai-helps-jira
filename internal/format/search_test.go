package format

import (
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func TestSearchSummaryCountAndCompletenessKinds(t *testing.T) {
	more := true
	noMore := false
	cases := []struct {
		name string
		info jira.SearchInfo
		want []string
	}{
		{"exact", jira.SearchInfo{Returned: 2, Complete: true, Total: intPtr(2), TotalKind: jira.CountExact}, []string{"Returned rows: 2", "Exact matching total: 2", "Completeness: complete"}},
		{"reported", jira.SearchInfo{Returned: 2, HasMore: &more, Total: intPtr(11), TotalKind: jira.CountReported}, []string{"Jira-reported matching total (unverified): 11", "partial; more matches known"}},
		{"estimated", jira.SearchInfo{Returned: 2, HasMore: &more, Total: intPtr(11), TotalKind: jira.CountEstimated}, []string{"Estimated matching total: 11"}},
		{"exact preview", jira.SearchInfo{Returned: 2, HasMore: &more, Total: intPtr(11), TotalKind: jira.CountExact, StopReason: "result_limit"}, []string{"Exact matching total: 11", "preview limit reached"}},
		{"unknown", jira.SearchInfo{Returned: 0}, []string{"Returned rows: 0", "Matching total: unknown", "Completeness: partial; whether more matches exist is unknown"}},
		{"observed lower bound", jira.SearchInfo{Returned: 1, Enumerated: 10000, TotalKind: jira.CountReported}, []string{"Returned rows: 1", "Observed matches: at least 10000", "Matching total: unknown"}},
		{"no more reported", jira.SearchInfo{HasMore: &noMore, StopReason: "cancelled"}, []string{"no more matches reported", "stopped: cancelled"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SearchSummary(tc.info)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("SearchSummary() = %q; missing %q", got, want)
				}
			}
		})
	}
}

func intPtr(v int) *int { return &v }
