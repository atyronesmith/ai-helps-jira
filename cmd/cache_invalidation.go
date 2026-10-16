package cmd

import (
	"log/slog"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

// invalidateIssueCache clears derived cached data after a successful JIRA
// mutation. Cache failures should not turn a completed JIRA write into a
// failed CLI command, so errors are logged and ignored.
func invalidateIssueCache(cfg *config.Config, issueKeys ...string) {
	db, err := cache.OpenForConfig(cfg)
	if err != nil {
		slog.Warn("could not open cache after JIRA mutation", "error", err)
		return
	}
	defer db.Close()

	seen := make(map[string]struct{}, len(issueKeys))
	for _, key := range issueKeys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if err := db.InvalidateIssue(key); err != nil {
			slog.Warn("could not invalidate issue cache after JIRA mutation", "issue", key, "error", err)
		}
	}
}
