package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// handleClearHistory deletes only this server instance's saved dashboard data.
// Rendering, polling, and rejected requests never perform this operation.
func (ws *WebServer) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	failure := func(code int, message string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(struct {
			Message string `json:"message"`
		}{message})
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		failure(http.StatusMethodNotAllowed, "Use Clear log on the dashboard to clear saved history.")
		return
	}
	if r.Header.Get("X-Jira-Clear-History") != "1" || !dashboardSameOrigin(r) {
		failure(http.StatusForbidden, "Start this action from the dashboard.")
		return
	}
	if r.URL.RawQuery != "" || r.ContentLength != 0 {
		failure(http.StatusBadRequest, "Clearing dashboard history accepts no parameters.")
		return
	}
	if ws.store == nil {
		ws.dependencies.observe("history", errors.New("history store unavailable"))
		failure(http.StatusServiceUnavailable, "Could not confirm that the dashboard log was cleared. Refresh before retrying.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	deleted, err := ws.store.Clear(ctx)
	ws.dependencies.observe("history", err)
	if err != nil {
		failure(http.StatusServiceUnavailable, "Could not confirm that the dashboard log was cleared. Refresh before retrying.")
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Deleted int `json:"deleted"`
	}{deleted})
}
