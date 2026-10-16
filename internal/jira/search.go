package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// ErrIncompleteSearch accompanies interrupted enumeration, never a verified
// exact total. A bounded list reaching its requested row limit is not an error.
var ErrIncompleteSearch = errors.New("search incomplete")

// SearchJQLResult retrieves bounded rows and explicit completeness metadata.
// An error may accompany partial rows, which never constitute an exact count.
func (c *Client) SearchJQLResult(ctx context.Context, rawJQL string, opts SearchOptions) (SearchResult, error) {
	result := SearchResult{SearchInfo: SearchInfo{TotalKind: CountUnknown}}
	if err := jql.Validate(rawJQL, jql.Options{DefaultProject: c.project}); err != nil {
		return result, err
	}
	if opts.Limit < 0 || opts.PageSize < 0 || opts.MaxPages < 0 {
		return result, errors.New("search limit, page size, and page budget must not be negative")
	}
	if opts.Limit == 0 {
		opts.Limit = 50
	}
	if opts.PageSize == 0 {
		opts.PageSize = 100
	}
	if opts.MaxPages == 0 {
		opts.MaxPages = 100
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	seenKeys := make(map[string]bool)
	seenTokens := make(map[string]bool)
	token := ""
	stop := func(reason string, err error) (SearchResult, error) {
		result.StopReason = reason
		return result, fmt.Errorf("JQL %q: %w: %w", rawJQL, ErrIncompleteSearch, err)
	}
	for page := 0; page < opts.MaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return stop("canceled", err)
		}
		pageSize := opts.PageSize
		if !opts.ExactCount {
			pageSize = min(pageSize, opts.Limit-len(result.Issues))
		}
		request := searchRequest{
			JQL: rawJQL, MaxResults: pageSize, NextPageToken: token,
			Fields: []string{"summary", "status", "priority", "updated", "assignee"},
		}
		var response searchResponse
		if err := c.doRequestContext(ctx, http.MethodPost, "/rest/api/3/search/jql", request, &response); err != nil {
			reason := "request_failed"
			var typeError *json.UnmarshalTypeError
			if errors.As(err, &typeError) && (typeError.Field == "nextPageToken" || typeError.Field == "isLast") {
				reason = "invalid_pagination"
			}
			if ctx.Err() != nil {
				reason = "canceled"
			}
			return stop(reason, err)
		}
		// A previous token only proved that this page existed. It says nothing
		// about further matches after the page now being processed.
		result.HasMore = nil
		if result.Enumerated > result.Returned {
			result.HasMore = boolPointer(true)
		}
		if response.Total != nil {
			if *response.Total < 0 || (result.Total != nil && *result.Total != *response.Total) {
				return stop("inconsistent_results", errors.New("Jira reported an invalid or changing total"))
			}
			result.Total = response.Total
			result.TotalKind = CountReported
		}
		for _, issue := range parseAPIIssues(response.Issues) {
			if issue.Key == "" || seenKeys[issue.Key] {
				return stop("inconsistent_results", errors.New("Jira returned a missing or duplicate issue key"))
			}
			seenKeys[issue.Key] = true
			result.Enumerated++
			if len(result.Issues) < opts.Limit {
				result.Issues = append(result.Issues, issue)
				result.Returned++
			} else {
				result.HasMore = boolPointer(true)
			}
		}
		if result.Enumerated > result.Returned {
			result.HasMore = boolPointer(true)
		}
		if result.Total != nil && result.Enumerated > *result.Total {
			return stop("inconsistent_results", errors.New("enumerated issues exceed Jira's reported total"))
		}
		next := ""
		if response.NextPageToken != nil {
			next = *response.NextPageToken
			if next != "" && strings.TrimSpace(next) == "" {
				return stop("invalid_pagination", errors.New("Jira returned a blank pagination token"))
			}
		}
		if response.IsLast != nil && *response.IsLast {
			if next != "" {
				return stop("invalid_pagination", errors.New("terminal page also contained a continuation token"))
			}
			if result.Total != nil && *result.Total != result.Enumerated {
				return stop("inconsistent_results", errors.New("terminal enumeration disagrees with Jira's reported total"))
			}
			total := result.Enumerated
			result.Total, result.TotalKind = &total, CountExact
			result.Complete = result.Returned == result.Enumerated
			result.HasMore = boolPointer(!result.Complete)
			if !result.Complete {
				result.StopReason = "result_limit"
			}
			return result, nil
		}
		if next == "" {
			if response.IsLast != nil {
				return stop("invalid_pagination", errors.New("nonterminal page has no continuation token"))
			}
			result.StopReason = "pagination_unknown"
			if opts.ExactCount {
				return stop("pagination_unknown", errors.New("Jira omitted pagination metadata; exact count unavailable"))
			}
			return result, nil
		}
		if seenTokens[next] {
			return stop("invalid_pagination", errors.New("Jira repeated a pagination token"))
		}
		seenTokens[next] = true
		result.HasMore = boolPointer(true)
		if !opts.ExactCount && result.Returned >= opts.Limit {
			result.StopReason = "result_limit"
			return result, nil
		}
		token = next
	}
	return stop("page_budget", fmt.Errorf("stopped after %d pages; exact count unavailable", opts.MaxPages))
}

func boolPointer(value bool) *bool { return &value }
