package jira

import (
	"errors"
	"net/http"
	"testing"
)

func TestSearchJQLResultDoesNotTreatMissingIssueArrayAsEmpty(t *testing.T) {
	for _, shape := range []string{"missing", "null", "empty"} {
		t.Run(shape, func(t *testing.T) {
			r, err := runSearch(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				page := map[string]any{"isLast": true, "total": 0}
				if shape == "null" {
					page["issues"] = nil
				} else if shape == "empty" {
					page["issues"] = []any{}
				}
				jsonResponse(t, w, http.StatusOK, page)
			}), SearchOptions{})
			if shape == "empty" {
				if err != nil || !r.Complete || r.Total == nil || *r.Total != 0 || r.TotalKind != CountExact {
					t.Fatalf("explicit empty array must establish empty result: %+v, %v", r, err)
				}
			} else if !errors.Is(err, ErrIncompleteSearch) || r.Complete || r.TotalKind == CountExact || r.StopReason != "invalid_response" {
				t.Fatalf("malformed response became a complete empty search: %+v, %v", r, err)
			}
		})
	}
}
