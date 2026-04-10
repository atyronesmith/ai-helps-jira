package cache

import (
	"log/slog"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// Fetcher abstracts JIRA API calls so the caching layer can be tested
// without a real JIRA connection.
type Fetcher interface {
	GetIssue(key string) (*jira.IssueDetail, error)
	GetComments(key string) ([]jira.Comment, error)
	GetIssueWithLinks(key string) (*jira.IssueDetail, []jira.IssueLink, error)
}

// CachedIssueResult holds a detail and its comments, populated from cache or API.
type CachedIssueResult struct {
	Detail   *jira.IssueDetail
	Links    []jira.IssueLink
	Comments []jira.Comment
	// Whether each piece came from cache
	DetailCached   bool
	LinksCached    bool
	CommentsCached bool
}

// FetchIssue retrieves an issue detail, preferring cache when fresh.
// knownUpdated is the issue's current updated time from a search result;
// pass zero time to accept any cached value.
func (c *Cache) FetchIssue(f Fetcher, key string, knownUpdated time.Time) (*jira.IssueDetail, bool, error) {
	if detail, ok := c.GetIssueDetail(key, knownUpdated); ok {
		slog.Debug("cache hit for issue detail", "key", key)
		return detail, true, nil
	}

	detail, err := f.GetIssue(key)
	if err != nil {
		return nil, false, err
	}
	c.UpsertIssueDetail(detail)
	return detail, false, nil
}

// FetchComments retrieves comments for an issue, preferring cache.
// since filters comments to those created after this time.
func (c *Cache) FetchComments(f Fetcher, key string, since time.Time, preferCache bool) ([]jira.Comment, bool, error) {
	if preferCache {
		comments, _ := c.GetCommentsByKeys([]string{key}, since)
		if len(comments) > 0 {
			return comments, true, nil
		}
	}

	comments, err := f.GetComments(key)
	if err != nil {
		return nil, false, err
	}
	if len(comments) > 0 {
		c.UpsertComments(comments)
	}
	return comments, false, nil
}

// FetchIssueWithLinks retrieves an issue with its links, preferring cache.
func (c *Cache) FetchIssueWithLinks(f Fetcher, key string) (*jira.IssueDetail, []jira.IssueLink, bool, error) {
	detail, detailCached := c.GetIssueDetail(key, time.Time{})
	links, _ := c.GetIssueLinks(key)

	if detailCached && len(links) > 0 {
		return detail, links, true, nil
	}

	fetchedDetail, fetchedLinks, err := f.GetIssueWithLinks(key)
	if err != nil {
		return nil, nil, false, err
	}
	c.UpsertIssueDetail(fetchedDetail)
	if len(fetchedLinks) > 0 {
		c.UpsertIssueLinks(fetchedLinks)
	}
	return fetchedDetail, fetchedLinks, false, nil
}
