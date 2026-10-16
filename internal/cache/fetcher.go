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
	op, err := c.operation()
	if err != nil {
		return nil, false, err
	}
	c = op

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
	op, err := c.operation()
	if err != nil {
		return nil, false, err
	}
	c = op

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
	op, err := c.operation()
	if err != nil {
		return nil, nil, false, err
	}
	c = op

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

// ProjectFetcher abstracts the tenant's project-list fetch so the caching layer
// can be tested without a real JIRA connection. The jira.Client satisfies it.
type ProjectFetcher interface {
	ListProjects() ([]jira.Project, error)
}

// DefaultProjectTTL is how long a cached project list stays fresh. Projects
// change rarely, so a day is a comfortable default: the deterministic hot path
// reads the cached list and only re-fetches once a day.
const DefaultProjectTTL = 24 * time.Hour

// FetchProjects retrieves the tenant's project list, preferring a fresh cached
// copy and only touching the network when the cache is missing or older than
// ttl. It returns the projects, whether they came from cache, and any error.
//
// The fetcher is built lazily through build so a cache hit never pays for
// client construction: build is invoked only on the cold or stale path below.
// A build failure on a warm cache serves the stale copy alongside the error,
// just like a failed re-fetch.
//
// On a stale cache with a failed re-fetch it still returns the stale copy (so
// human names keep resolving) alongside the error, letting the caller log and
// decide. On a cold cache with a failed fetch it returns an empty slice and the
// error. Callers should treat a non-nil error as "use whatever was returned, and
// fall back to the env-only vocabulary if it is empty" so a warm-up never fails
// a query.
func (c *Cache) FetchProjects(build func() (ProjectFetcher, error), ttl time.Duration) ([]jira.Project, bool, error) {
	op, err := c.operation()
	if err != nil {
		return nil, false, err
	}
	c = op

	projects, fetchedAt, ok := c.GetProjects()
	if ok && (ttl <= 0 || time.Since(fetchedAt) < ttl) {
		slog.Debug("cache hit for projects", "count", len(projects))
		return projects, true, nil
	}

	// The fetcher is built lazily so the cache hit above never pays for client
	// construction; build runs only on the cold or stale path.
	fetcher, err := build()
	if err != nil {
		if ok {
			slog.Warn("project re-fetch unavailable; serving stale cache", "error", err, "age", time.Since(fetchedAt))
			return projects, false, err
		}
		return nil, false, err
	}

	fresh, err := fetcher.ListProjects()
	if err != nil {
		if ok {
			slog.Warn("project re-fetch failed; serving stale cache", "error", err, "age", time.Since(fetchedAt))
			return projects, false, err
		}
		return nil, false, err
	}

	if err := c.UpsertProjects(fresh); err != nil {
		if ok {
			slog.Warn("project upsert failed; serving stale cache", "error", err)
			return projects, false, err
		}
		return nil, false, err
	}
	return fresh, false, nil
}

// WarmupProjects is the shared best-effort wrapper over FetchProjects used by the
// natural-language translation seams -- the MCP server's projectList and the CLI's
// fetchProjectList -- so the warm-up orchestration lives in one place. It resolves
// the tenant's project list, preferring a fresh cached copy and building the
// fetcher only on a cold or stale cache (callers pass DefaultProjectTTL). A nil
// cache short-circuits to nil; a build or fetch error is logged and degrades to
// whatever FetchProjects already returned -- a stale copy when the cache was warm,
// or an empty slice otherwise -- which the translator folds into its env-only
// vocabulary, so a warm-up never fails a query. The fetcher is built lazily
// through build, so a fresh cache keeps the per-query hot path network- and
// client-construction-free.
func WarmupProjects(c *Cache, build func() (ProjectFetcher, error), ttl time.Duration) []jira.Project {
	if c == nil {
		return nil
	}
	projects, fromCache, err := c.FetchProjects(build, ttl)
	if err != nil {
		slog.Warn("project warm-up failed; using cached or env-only vocabulary", "error", err, "from_cache", fromCache, "count", len(projects))
		return projects
	}
	return projects
}
