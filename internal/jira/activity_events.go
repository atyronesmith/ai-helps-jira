package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const activityPageSize = 100
const activityMaxPageLimit = 100

const (
	ActivityKindComment = "comment"
	ActivityKindWorklog = "worklog"
	ActivityKindStatus  = "status"
)

// ActivityEvent is one actor-attributed comment, worklog, or status change.
// Detail deliberately excludes comment and worklog bodies.
type ActivityEvent struct {
	ID       string
	Kind     string
	AuthorID string
	At       time.Time
	Detail   string
}

type activityPageMeta struct {
	StartAt    *int  `json:"startAt"`
	MaxResults *int  `json:"maxResults"`
	Total      *int  `json:"total"`
	IsLast     *bool `json:"isLast"`
}

type activityEventActor struct {
	AccountID *string `json:"accountId"`
}

type activityComment struct {
	ID      string              `json:"id"`
	Created string              `json:"created"`
	Author  *activityEventActor `json:"author"`
}

type activityWorklog struct {
	ID               string              `json:"id"`
	Started          string              `json:"started"`
	Author           *activityEventActor `json:"author"`
	TimeSpentSeconds *int                `json:"timeSpentSeconds"`
}

type activityHistoryItem struct {
	FieldID    string  `json:"fieldId"`
	Field      string  `json:"field"`
	FromString *string `json:"fromString"`
	ToString   *string `json:"toString"`
}

type activityHistory struct {
	ID      string                `json:"id"`
	Created string                `json:"created"`
	Author  *activityEventActor   `json:"author"`
	Items   []activityHistoryItem `json:"items"`
}

type activityCommentPage struct {
	activityPageMeta
	Comments []activityComment `json:"comments"`
}

type activityWorklogPage struct {
	activityPageMeta
	Worklogs []activityWorklog `json:"worklogs"`
}

type activityChangelogPage struct {
	activityPageMeta
	Values []activityHistory `json:"values"`
}

type activityPage struct {
	meta      activityPageMeta
	items     int
	recordIDs []string
	consume   func() ([]ActivityEvent, error)
}

// ReadActivityEvents reads comments, worklogs, and status-history entries for
// one issue. maxPages applies independently to each endpoint.
func (c *Client) ReadActivityEvents(ctx context.Context, key string, maxPages int) ([]ActivityEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("activity events: issue key is required")
	}
	if maxPages < 1 || maxPages > activityMaxPageLimit {
		return nil, fmt.Errorf("activity events: maxPages must be between 1 and %d", activityMaxPageLimit)
	}

	issuePath := "/rest/api/3/issue/" + url.PathEscape(key)
	var events []ActivityEvent
	seen := make(map[string]struct{})
	for _, endpoint := range []struct {
		name string
		path string
		read func(context.Context, string) (activityPage, error)
	}{
		{"comment", issuePath + "/comment", c.readActivityCommentPage},
		{"worklog", issuePath + "/worklog", c.readActivityWorklogPage},
		{"changelog", issuePath + "/changelog", c.readActivityChangelogPage},
	} {
		pageEvents, err := c.readActivityEndpoint(ctx, endpoint.name, endpoint.path, maxPages, endpoint.read, seen)
		events = append(events, pageEvents...)
		if err != nil {
			return events, err
		}
	}
	return events, nil
}

type activityPageReader func(context.Context, string) (activityPage, error)

func (c *Client) readActivityEndpoint(ctx context.Context, name, basePath string, maxPages int, read activityPageReader, seen map[string]struct{}) ([]ActivityEvent, error) {
	var events []ActivityEvent
	var stableTotal *int
	offset := 0
	for pageNumber := 1; pageNumber <= maxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return events, err
		}
		path := basePath + "?startAt=" + strconv.Itoa(offset) + "&maxResults=" + strconv.Itoa(activityPageSize)
		page, err := read(ctx, path)
		if err != nil {
			return events, fmt.Errorf("read Jira %s activity page at %d: %w", name, offset, err)
		}
		if err := validateActivityPage(name, offset, page, stableTotal); err != nil {
			return events, err
		}
		if stableTotal == nil {
			total := *page.meta.Total
			stableTotal = &total
		}
		pageEvents, consumeErr := page.consume()
		eventByID := make(map[string]ActivityEvent, len(pageEvents))
		for _, event := range pageEvents {
			eventByID[event.ID] = event
		}
		matchedEvents := 0
		for _, id := range page.recordIDs {
			if id == "" {
				return events, fmt.Errorf("Jira %s activity contains a record with no ID", name)
			}
			identity := name + ":" + id
			if _, exists := seen[identity]; exists {
				return events, fmt.Errorf("Jira %s activity contains duplicate record %q", name, id)
			}
			seen[identity] = struct{}{}
			if event, ok := eventByID[id]; ok {
				events = append(events, event)
				matchedEvents++
			}
		}
		if matchedEvents != len(pageEvents) {
			return events, fmt.Errorf("Jira %s activity event IDs do not match page records", name)
		}
		if consumeErr != nil {
			return events, fmt.Errorf("invalid Jira %s activity at page offset %d: %w", name, offset, consumeErr)
		}
		if page.meta.IsLast != nil && *page.meta.IsLast != (offset+page.items >= *page.meta.Total) {
			return events, fmt.Errorf("Jira %s activity page has inconsistent isLast metadata", name)
		}
		next := offset + page.items
		if next >= *page.meta.Total {
			return events, nil
		}
		if next <= offset {
			return events, fmt.Errorf("Jira %s activity pagination did not progress", name)
		}
		offset = next
	}
	if stableTotal != nil && offset < *stableTotal {
		return events, fmt.Errorf("Jira %s activity exceeded page limit %d before reaching total %d", name, maxPages, *stableTotal)
	}
	return events, nil
}

func validateActivityPage(name string, requested int, page activityPage, stableTotal *int) error {
	meta := page.meta
	if meta.StartAt == nil || meta.MaxResults == nil || meta.Total == nil {
		return fmt.Errorf("Jira %s activity page is missing startAt, maxResults, or total metadata", name)
	}
	if *meta.StartAt != requested {
		return fmt.Errorf("Jira %s activity page startAt is %d, requested %d", name, *meta.StartAt, requested)
	}
	if *meta.MaxResults <= 0 {
		return fmt.Errorf("Jira %s activity page maxResults must be positive", name)
	}
	if *meta.Total < 0 {
		return fmt.Errorf("Jira %s activity page total cannot be negative", name)
	}
	if stableTotal != nil && *meta.Total != *stableTotal {
		return fmt.Errorf("Jira %s activity total changed from %d to %d", name, *stableTotal, *meta.Total)
	}
	if requested > *meta.Total {
		return fmt.Errorf("Jira %s activity page starts beyond total %d", name, *meta.Total)
	}
	remaining := *meta.Total - requested
	if page.items < 0 || page.items > activityPageSize || page.items > *meta.MaxResults || page.items > remaining {
		return fmt.Errorf("Jira %s activity page length %d exceeds maxResults/remaining %d/%d", name, page.items, *meta.MaxResults, remaining)
	}
	if len(page.recordIDs) != page.items {
		return fmt.Errorf("Jira %s activity page record ID count %d does not match length %d", name, len(page.recordIDs), page.items)
	}
	if remaining > 0 && page.items == 0 {
		return fmt.Errorf("Jira %s activity pagination did not progress", name)
	}
	return nil
}

func (c *Client) readActivityCommentPage(ctx context.Context, path string) (activityPage, error) {
	var response activityCommentPage
	if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &response); err != nil {
		return activityPage{}, err
	}
	if response.Comments == nil {
		return activityPage{}, fmt.Errorf("Jira comment activity page is missing comments array")
	}
	return activityPage{
		meta:      response.activityPageMeta,
		items:     len(response.Comments),
		recordIDs: commentIDs(response.Comments),
		consume: func() ([]ActivityEvent, error) {
			events := make([]ActivityEvent, 0, len(response.Comments))
			for _, comment := range response.Comments {
				authorID, err := activityAuthorID(comment.Author)
				if err != nil {
					return events, fmt.Errorf("comment %q: %w", comment.ID, err)
				}
				at, err := parseActivityTime(comment.Created)
				if err != nil {
					return events, fmt.Errorf("comment %q has invalid created time: %w", comment.ID, err)
				}
				if comment.ID == "" {
					return events, fmt.Errorf("comment has no ID")
				}
				events = append(events, ActivityEvent{ID: comment.ID, Kind: ActivityKindComment, AuthorID: authorID, At: at})
			}
			return events, nil
		},
	}, nil
}

func (c *Client) readActivityWorklogPage(ctx context.Context, path string) (activityPage, error) {
	var response activityWorklogPage
	if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &response); err != nil {
		return activityPage{}, err
	}
	if response.Worklogs == nil {
		return activityPage{}, fmt.Errorf("Jira worklog activity page is missing worklogs array")
	}
	return activityPage{
		meta:      response.activityPageMeta,
		items:     len(response.Worklogs),
		recordIDs: worklogIDs(response.Worklogs),
		consume: func() ([]ActivityEvent, error) {
			events := make([]ActivityEvent, 0, len(response.Worklogs))
			for _, worklog := range response.Worklogs {
				authorID, err := activityAuthorID(worklog.Author)
				if err != nil {
					return events, fmt.Errorf("worklog %q: %w", worklog.ID, err)
				}
				at, err := parseActivityTime(worklog.Started)
				if err != nil {
					return events, fmt.Errorf("worklog %q has invalid started time: %w", worklog.ID, err)
				}
				if worklog.ID == "" {
					return events, fmt.Errorf("worklog has no ID")
				}
				detail := ""
				if worklog.TimeSpentSeconds != nil {
					if *worklog.TimeSpentSeconds < 0 {
						return events, fmt.Errorf("worklog %q has negative timeSpentSeconds", worklog.ID)
					}
					detail = fmt.Sprintf("time spent: %d seconds", *worklog.TimeSpentSeconds)
				}
				events = append(events, ActivityEvent{ID: worklog.ID, Kind: ActivityKindWorklog, AuthorID: authorID, At: at, Detail: detail})
			}
			return events, nil
		},
	}, nil
}

func (c *Client) readActivityChangelogPage(ctx context.Context, path string) (activityPage, error) {
	var response activityChangelogPage
	if err := c.doRequestContext(ctx, http.MethodGet, path, nil, &response); err != nil {
		return activityPage{}, err
	}
	if response.Values == nil {
		return activityPage{}, fmt.Errorf("Jira changelog activity page is missing values array")
	}
	return activityPage{
		meta:      response.activityPageMeta,
		items:     len(response.Values),
		recordIDs: historyIDs(response.Values),
		consume: func() ([]ActivityEvent, error) {
			events := make([]ActivityEvent, 0, len(response.Values))
			for _, history := range response.Values {
				if history.Items == nil {
					return events, fmt.Errorf("changelog entry %q is missing items array", history.ID)
				}
				var statusDetails []string
				for _, item := range history.Items {
					if strings.TrimSpace(item.FieldID) == "" && strings.TrimSpace(item.Field) == "" {
						return events, fmt.Errorf("changelog entry %q contains an item with no field identity", history.ID)
					}
					if strings.EqualFold(item.FieldID, "status") || strings.EqualFold(item.Field, "status") {
						statusDetails = append(statusDetails, statusChangeDetail(item.FromString, item.ToString))
					}
				}
				if len(statusDetails) == 0 {
					continue
				}
				if history.ID == "" {
					return events, fmt.Errorf("status changelog entry has no ID")
				}
				authorID, err := activityAuthorID(history.Author)
				if err != nil {
					return events, fmt.Errorf("status changelog entry %q: %w", history.ID, err)
				}
				at, err := parseActivityTime(history.Created)
				if err != nil {
					return events, fmt.Errorf("status changelog entry %q has invalid created time: %w", history.ID, err)
				}
				events = append(events, ActivityEvent{ID: history.ID, Kind: ActivityKindStatus, AuthorID: authorID, At: at, Detail: strings.Join(statusDetails, "; ")})
			}
			return events, nil
		},
	}, nil
}

func commentIDs(records []activityComment) []string {
	ids := make([]string, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	return ids
}

func worklogIDs(records []activityWorklog) []string {
	ids := make([]string, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	return ids
}

func historyIDs(records []activityHistory) []string {
	ids := make([]string, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	return ids
}

func activityAuthorID(author *activityEventActor) (string, error) {
	if author == nil || author.AccountID == nil || strings.TrimSpace(*author.AccountID) == "" {
		return "", fmt.Errorf("missing author accountId")
	}
	return *author.AccountID, nil
}

func parseActivityTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999-0700",
		"2006-01-02T15:04:05.999999-0700",
		"2006-01-02T15:04:05-0700",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func statusChangeDetail(from, to *string) string {
	oldValue, newValue := "", ""
	if from != nil {
		oldValue = *from
	}
	if to != nil {
		newValue = *to
	}
	return "status: " + oldValue + " -> " + newValue
}
