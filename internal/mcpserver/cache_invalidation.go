package mcpserver

import (
	"log/slog"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

// prepareMutationCache opens the account namespace before a remote mutation.
// Its failure is deliberately non-fatal: Jira remains authoritative and the
// completed write must not be reported as failed due to local cache setup.
func (h *Handlers) prepareMutationCache(cfg *config.Config) {
	if h.cache == nil {
		return
	}
	if _, err := h.cache.ForConfig(cfg); err != nil {
		slog.Warn("could not prepare scoped cache before Jira mutation", "error", err)
	}
}

// invalidateIssueCache clears derived issue data after a successful Jira
// mutation. A cache failure must not make an already-completed remote write
// look unsuccessful, so failures are logged and the mutation result is kept.
func (h *Handlers) invalidateIssueCache(cfg *config.Config, issueKeys ...string) {
	if h.cache == nil {
		return
	}
	db, err := h.cache.ForConfig(cfg)
	if err != nil {
		slog.Warn("could not scope issue cache after Jira mutation", "error", err)
		return
	}

	seen := make(map[string]struct{}, len(issueKeys))
	for _, key := range issueKeys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if err := db.InvalidateIssue(key); err != nil {
			slog.Warn("could not invalidate issue cache after Jira mutation", "issue", key, "error", err)
		}
	}
}
