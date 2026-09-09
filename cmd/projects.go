package cmd

import (
	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

// fetchProjectList resolves the tenant's project list for natural-language JQL
// translation through the shared cache.WarmupProjects helper -- the same one the
// MCP server's projectList uses -- so the warm-up orchestration lives in one
// place. It prefers a fresh cached copy and only touches the network on a cold or
// stale cache, and is best-effort: a nil cache, a failed client build, or a fetch
// error degrades to whatever the cache already has -- a stale copy when warm, or an
// empty slice otherwise -- which the translator folds into its env-only vocabulary,
// so a warm-up never fails a query. The fetcher is built lazily inside
// FetchProjects, so a fresh cache keeps the call network- and client-construction-
// free.
func fetchProjectList(cfg *config.Config, db *cache.Cache) []jira.Project {
	return cache.WarmupProjects(db, func() (cache.ProjectFetcher, error) {
		client, err := jira.NewClient(cfg)
		if err != nil {
			return nil, err
		}
		return client, nil
	}, cache.DefaultProjectTTL)
}
