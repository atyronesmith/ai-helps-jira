package mcpserver

import "log/slog"

// invalidateIssueCache clears derived issue data after a successful Jira
// mutation. A cache failure must not make an already-completed remote write
// look unsuccessful, so failures are logged and the mutation result is kept.
func (h *Handlers) invalidateIssueCache(issueKeys ...string) {
	if h.cache == nil {
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
		if err := h.cache.InvalidateIssue(key); err != nil {
			slog.Warn("could not invalidate issue cache after Jira mutation", "issue", key, "error", err)
		}
	}
}
