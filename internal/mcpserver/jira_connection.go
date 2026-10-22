package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func checkConfiguredJira(ctx context.Context) jira.ConnectionCheck {
	cfg, err := config.LoadJIRAConnection()
	if err != nil {
		return jira.ConnectionCheck{State: "configuration", Message: "Configure the Jira server, email, and API token before testing the connection."}
	}
	return jira.CheckConnection(ctx, cfg)
}

// handleJiraConnection is deliberately separate from passive status polling.
// Only an explicit same-origin POST can trigger the authenticated upstream GET.
func (ws *WebServer) handleJiraConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	write := func(code int, result jira.ConnectionCheck) {
		if result.CheckedAt.IsZero() {
			result.CheckedAt = time.Now().UTC()
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(result)
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		write(http.StatusMethodNotAllowed, jira.ConnectionCheck{State: "invalid_request", Message: "Use the Test Jira connection button to start a check."})
		return
	}
	if r.Header.Get("X-Jira-Connection-Test") != "1" || !dashboardSameOrigin(r) {
		write(http.StatusForbidden, jira.ConnectionCheck{State: "invalid_request", Message: "Start the connection test from this dashboard."})
		return
	}
	if r.URL.RawQuery != "" || r.ContentLength != 0 {
		write(http.StatusBadRequest, jira.ConnectionCheck{State: "invalid_request", Message: "The connection test uses the server's configured Jira connection and accepts no parameters."})
		return
	}
	if !ws.jiraConnectionMu.TryLock() {
		write(http.StatusConflict, jira.ConnectionCheck{State: "busy", Message: "A Jira connection check is already running. Try again when it finishes."})
		return
	}
	defer ws.jiraConnectionMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	started := time.Now()
	result := ws.checkJiraConnection(ctx)
	result.CheckedAt, result.LatencyMS = time.Now().UTC(), time.Since(started).Milliseconds()
	var observationErr error
	if !result.OK {
		observationErr = errors.New("Jira connection check failed")
	}
	ws.dependencies.observe("jira", observationErr)
	code := http.StatusBadGateway
	switch {
	case result.OK:
		code = http.StatusOK
	case result.State == "configuration":
		code = http.StatusBadRequest
	case result.State == "timeout":
		code = http.StatusGatewayTimeout
	case result.State == "cancelled":
		code = http.StatusRequestTimeout
	}
	write(code, result)
}
