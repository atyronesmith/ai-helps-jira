package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jql"
)

// FetchSummary returns a verified complete open-work set and complete board
// memberships. A failed page or malformed pagination aborts the refresh.
func (c *Client) FetchSummary(ctx context.Context, invalidated []string) ([]BoardInfo, []Issue, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	q := (&jql.Query{Where: jql.All(
		jql.Compare("project", "=", jql.QuoteIdent(c.project)),
		jql.Compare("assignee", "=", c.assignee),
		jql.Compare("statusCategory", "!=", "Done"),
	), OrderBy: []jql.Sort{{Field: "priority", Direction: "ASC"}, {Field: "status", Direction: "ASC"}}}).Render()
	r, err := c.SearchJQLResult(ctx, q, SearchOptions{Limit: 10000, PageSize: 100, MaxPages: 100, ExactCount: true})
	if err != nil {
		return nil, nil, err
	}
	if !r.Complete {
		return nil, nil, fmt.Errorf("summary open-work search incomplete: %s", r.StopReason)
	}
	for _, issue := range r.Issues {
		if issue.Project == "" || issue.StatusCategory == "" {
			return nil, nil, fmt.Errorf("summary issue %s is missing project or status category", issue.Key)
		}
	}
	ownerID := ""
	needsID := len(invalidated) != 0
	for _, issue := range r.Issues {
		expected := c.authEmail
		if c.userOverride != "" {
			expected = c.userOverride
		}
		if issue.Assignee == "" || !strings.EqualFold(issue.Assignee, expected) {
			needsID = true
			break
		}
	}
	if needsID {
		ownerID, err = c.summaryAccountID(ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	filtered := make([]Issue, 0, len(r.Issues))
	for _, issue := range r.Issues {
		if issue.Project != c.project || !IsOpenIssue(issue) {
			continue
		}
		if !c.ownedByEffectiveUser(issue, ownerID) {
			continue
		}
		filtered = append(filtered, issue)
	}
	for _, key := range invalidated {
		detail, err := c.getIssueContext(ctx, key)
		if err != nil {
			// Jira hides deleted or newly inaccessible issues as 403/404.
			var statusErr *HTTPStatusError
			if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusNotFound) {
				filtered = removeSummaryIssue(filtered, key)
				continue
			}
			return nil, nil, fmt.Errorf("recheck invalidated issue %s: %w", key, err)
		}
		if detail.Project == "" || detail.StatusCategory == "" {
			return nil, nil, fmt.Errorf("rechecked issue %s is missing project or status category", key)
		}
		if detail.Project == c.project && IsOpenIssue(Issue{StatusCategory: detail.StatusCategory}) && c.detailOwned(detail, ownerID) {
			issue := Issue{Key: detail.Key, Summary: detail.Summary, Status: detail.Status, Project: detail.Project, StatusCategory: detail.StatusCategory, Assignee: detail.AssigneeEmail, AssigneeID: detail.AssigneeID, Updated: detail.Updated}
			filtered = replaceSummaryIssue(filtered, issue)
		} else {
			filtered = removeSummaryIssue(filtered, key)
		}
	}
	boards, err := c.fetchSummaryBoards(ctx, ownerID)
	if err != nil {
		return nil, nil, err
	}
	// Board results may include stale or closed Agile rows. Use the same verified
	// issue set as the canonical open-work snapshot.
	open := make(map[string]Issue, len(filtered))
	for _, i := range filtered {
		open[i.Key] = i
	}
	for bi := range boards {
		kept := boards[bi].Issues[:0]
		for _, issue := range boards[bi].Issues {
			if current, ok := open[issue.Key]; ok {
				current.Board, current.Sprint = issue.Board, issue.Sprint
				kept = append(kept, current)
			}
		}
		boards[bi].Issues = kept
	}
	return boards, filtered, nil
}

func (c *Client) ownedByEffectiveUser(i Issue, accountID string) bool {
	if c.userOverride == "" {
		return strings.EqualFold(i.Assignee, c.authEmail) || (accountID != "" && i.AssigneeID == accountID)
	}
	return strings.EqualFold(i.Assignee, c.userOverride) || i.AssigneeID == c.userOverride || (accountID != "" && i.AssigneeID == accountID)
}
func (c *Client) detailOwned(i *IssueDetail, accountID string) bool {
	return c.ownedByEffectiveUser(Issue{Assignee: i.AssigneeEmail, AssigneeID: i.AssigneeID}, accountID)
}
func replaceSummaryIssue(rows []Issue, i Issue) []Issue {
	for n := range rows {
		if rows[n].Key == i.Key {
			rows[n] = i
			return rows
		}
	}
	return append(rows, i)
}
func removeSummaryIssue(rows []Issue, key string) []Issue {
	out := rows[:0]
	for _, i := range rows {
		if i.Key != key {
			out = append(out, i)
		}
	}
	return out
}

func (c *Client) summaryAccountID(ctx context.Context) (string, error) {
	if c.userOverride == "" {
		var me apiAssignee
		if err := c.doRequestContext(ctx, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
			return "", err
		}
		if me.AccountID == "" {
			return "", errors.New("Jira /myself response omitted account ID")
		}
		return me.AccountID, nil
	}
	// Email visibility is often disabled. Resolve only the explicit override
	// when search rows cannot prove ownership by email.
	path := "/rest/api/3/user/search?query=" + url.QueryEscape(c.userOverride) + "&maxResults=100"
	var users []apiAssignee
	if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &users); err != nil {
		return "", fmt.Errorf("resolve configured Jira user: %w", err)
	}
	var matches []string
	for _, u := range users {
		if u.AccountID != "" && (strings.EqualFold(u.EmailAddress, c.userOverride) || strings.EqualFold(u.DisplayName, c.userOverride) || u.AccountID == c.userOverride) {
			found := false
			for _, id := range matches {
				if id == u.AccountID {
					found = true
					break
				}
			}
			if !found {
				matches = append(matches, u.AccountID)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("configured Jira user %q is ambiguous", c.userOverride)
	}
	return "", fmt.Errorf("configured Jira user %q could not be resolved exactly", c.userOverride)
}

type completePage struct {
	Values     json.RawMessage `json:"values"`
	Issues     json.RawMessage `json:"issues"`
	StartAt    int             `json:"startAt"`
	MaxResults int             `json:"maxResults"`
	Total      *int            `json:"total"`
	IsLast     *bool           `json:"isLast"`
}

func (c *Client) listComplete(ctx context.Context, path string, decode func(json.RawMessage) error) error {
	const size, maxPages = 100, 100
	startAt := 0
	var enumerated int
	var reportedTotal *int
	for page := 0; page < maxPages; page++ {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		var p completePage
		if err := c.doRequestContext(ctx, http.MethodGet, fmt.Sprintf("%s%smaxResults=%d&startAt=%d", path, sep, size, startAt), nil, &p); err != nil {
			return err
		}
		payload := p.Values
		if len(payload) == 0 {
			payload = p.Issues
		}
		if err := decode(payload); err != nil {
			return err
		}
		var count int
		var pageRows []json.RawMessage
		if err := json.Unmarshal(payload, &pageRows); err == nil {
			count = len(pageRows)
		} else {
			var envelope struct {
				Issues []json.RawMessage `json:"issues"`
			}
			if json.Unmarshal(payload, &envelope) == nil {
				count = len(envelope.Issues)
			}
		}
		if (startAt == 0 && p.StartAt != 0) || (startAt != 0 && p.StartAt != startAt) {
			return errors.New("Jira returned unexpected pagination startAt")
		}
		if p.Total != nil && *p.Total < 0 {
			return errors.New("Jira returned a negative pagination total")
		}
		if p.Total != nil {
			if reportedTotal != nil && *reportedTotal != *p.Total {
				return errors.New("Jira changed pagination total between pages")
			}
			value := *p.Total
			reportedTotal = &value
		}
		if count == 0 && p.IsLast != nil && !*p.IsLast {
			return errors.New("Jira returned an empty nonterminal page")
		}
		if count == 0 && p.Total != nil && startAt < *p.Total {
			return errors.New("Jira returned an empty page before reported total")
		}
		if count == 0 && p.Total != nil && *p.Total == 0 && (p.IsLast == nil || *p.IsLast) {
			return nil
		}
		if count > size {
			return errors.New("Jira returned more rows than requested")
		}
		if count == 0 && p.Total == nil && p.IsLast == nil {
			return nil
		}
		next := startAt + count
		if next <= startAt && !(p.IsLast != nil && *p.IsLast) {
			return errors.New("Jira pagination made no progress")
		}
		enumerated += count
		if p.IsLast != nil {
			if *p.IsLast {
				if p.Total != nil && next != *p.Total {
					return errors.New("Jira returned inconsistent terminal pagination total")
				}
				return nil
			}
			if p.Total != nil && next >= *p.Total {
				return errors.New("Jira pagination is nonterminal at reported total")
			}
			startAt = next
			continue
		}
		// Legacy fixture/server shape without metadata is trusted only for a short page.
		if p.Total == nil {
			if count < size {
				return nil
			}
			return errors.New("Jira omitted pagination metadata at full page")
		}
		if next >= *p.Total {
			if next != *p.Total {
				return errors.New("Jira returned inconsistent pagination total")
			}
			return nil
		}
		if next <= startAt || enumerated < next {
			return errors.New("Jira pagination made no progress")
		}
		startAt = next
	}
	return errors.New("Jira pagination exceeded 100 pages")
}

func (c *Client) fetchSummaryBoards(ctx context.Context, _ string) ([]BoardInfo, error) {
	var boards []apiBoard
	seenBoards := make(map[int]bool)
	path := "/rest/agile/1.0/board?projectKeyOrId=" + url.QueryEscape(c.project)
	if err := c.listComplete(ctx, path, func(raw json.RawMessage) error {
		var page []apiBoard
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, board := range page {
			if seenBoards[board.ID] {
				return fmt.Errorf("Jira returned duplicate board ID %d", board.ID)
			}
			seenBoards[board.ID] = true
		}
		boards = append(boards, page...)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	var result []BoardInfo
	for _, b := range boards {
		if strings.EqualFold(b.Type, "scrum") {
			var sprints []apiSprint
			seenSprints := make(map[int]bool)
			if err := c.listComplete(ctx, fmt.Sprintf("/rest/agile/1.0/board/%d/sprint?state=active", b.ID), func(raw json.RawMessage) error {
				var page []apiSprint
				if err := json.Unmarshal(raw, &page); err != nil {
					return err
				}
				for _, sprint := range page {
					if seenSprints[sprint.ID] {
						return fmt.Errorf("Jira returned duplicate sprint ID %d", sprint.ID)
					}
					seenSprints[sprint.ID] = true
				}
				sprints = append(sprints, page...)
				return nil
			}); err != nil {
				return nil, fmt.Errorf("list active sprints for board %s: %w", b.Name, err)
			}
			for _, s := range sprints {
				q := (&jql.Query{Where: jql.All(
					jql.Compare("project", "=", jql.QuoteIdent(c.project)),
					jql.Compare("assignee", "=", c.assignee),
					jql.Compare("sprint", "=", fmt.Sprint(s.ID)),
					jql.Compare("statusCategory", "!=", "Done"),
				), OrderBy: []jql.Sort{{Field: "priority", Direction: "ASC"}}}).Render()
				sr, err := c.SearchJQLResult(ctx, q, SearchOptions{Limit: 10000, PageSize: 100, MaxPages: 100, ExactCount: true})
				if err != nil {
					return nil, fmt.Errorf("fetch sprint %s: %w", s.Name, err)
				}
				if !sr.Complete {
					return nil, errors.New("incomplete sprint issue search")
				}
				for n := range sr.Issues {
					if IsOpenIssue(sr.Issues[n]) {
						sr.Issues[n].Board = b.Name
						sr.Issues[n].Sprint = s.Name
					}
				}
				result = append(result, BoardInfo{Name: b.Name, BoardType: "scrum", SprintName: s.Name, Issues: sr.Issues})
			}
		} else if strings.EqualFold(b.Type, "kanban") {
			q := (&jql.Query{Where: jql.All(
				jql.Compare("assignee", "=", c.assignee),
				jql.Compare("statusCategory", "!=", "Done"),
			), OrderBy: []jql.Sort{{Field: "priority", Direction: "ASC"}, {Field: "status", Direction: "ASC"}}}).Render()
			if err := jql.Validate(q, jql.Options{}); err != nil {
				return nil, err
			}
			var rows []Issue
			seenIssues := make(map[string]bool)
			err := c.listComplete(ctx, fmt.Sprintf("/rest/agile/1.0/board/%d/issue?jql=%s", b.ID, url.QueryEscape(q)), func(raw json.RawMessage) error {
				var pageRows []apiIssue
				if err := json.Unmarshal(raw, &pageRows); err != nil {
					return err
				}
				for _, i := range parseAPIIssues(pageRows) {
					if i.Key == "" || seenIssues[i.Key] {
						return fmt.Errorf("Jira returned a missing or duplicate issue key %q for board %s", i.Key, b.Name)
					}
					seenIssues[i.Key] = true
					if i.Project == c.project && IsOpenIssue(i) {
						i.Board = b.Name
						rows = append(rows, i)
					}
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("fetch kanban board %s: %w", b.Name, err)
			}
			result = append(result, BoardInfo{Name: b.Name, BoardType: "kanban", Issues: rows})
		}
	}
	return result, nil
}
