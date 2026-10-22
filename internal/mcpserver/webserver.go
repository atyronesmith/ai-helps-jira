package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
	"github.com/atyronesmith/ai-helps-jira/internal/web"
)

// WebServer serves rich HTML dashboards for MCP tool results.
type WebServer struct {
	store               *ResultStore
	port                int
	bindHost            string
	tmpls               map[string]*template.Template
	startedAt           time.Time
	dependencies        *dependencyObservations
	httpServer          *http.Server
	jiraConnectionMu    sync.Mutex
	checkJiraConnection func(context.Context) jira.ConnectionCheck
}

// NewWebServer creates a web server with parsed templates.
func NewWebServer(store *ResultStore, db *cache.Cache, port int, bindHost string) *WebServer {
	var ws *WebServer
	funcMap := template.FuncMap{
		"cacheStatus": func() cacheStatusView {
			if db == nil {
				return cacheStatusView{}
			}
			stats := db.AggregateStats()
			hits, lookups := db.IssueDetailLookupStats()
			return cacheStatusView{CachedIssues: stats.CachedIssues, Hits: hits, Lookups: lookups}
		},
		"serverStatus": func() serverStatusView {
			return ws.status()
		},
		"statusClass": func(status string) string {
			return strings.ReplaceAll(strings.ToLower(status), " ", "-")
		},
		"priorityClass": func(pri string) string {
			return strings.ToLower(pri)
		},
		"issueURL": func(server, key string) string {
			return fmt.Sprintf("%s/browse/%s", server, key)
		},
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04")
		},
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"add": func(a, b int) int {
			return a + b
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n-3] + "..."
		},
		"mermaidHierarchy": func(links []interface{}) template.HTML {
			// Built in the template via JS instead
			return ""
		},
		// routeTooltip renders a one-line, human-readable explanation of a
		// query result's Route tier for the query view's badge title
		// (hover tooltip) and caption, so a viewer can tell the two routes
		// apart without reading the code.
		"routeTooltip": func(route string) string {
			switch route {
			case llm.RouteFastPath:
				return llm.RouteFastPath + " — the deterministic parser handled this query with no LLM call."
			case llm.RouteFallback:
				return llm.RouteFallback + " — the parser could not handle it, so one LLM produced typed intent compiled locally to JQL."
			case "activity":
				return "activity — authored comments, worklogs, and status changes were checked by account and event date. No LLM translation was used."
			default:
				return ""
			}
		},
		// queryMeta projects a stored result to its natural-language query,
		// translation route, and generated JQL when it is a query result, so
		// the results index can list the query and its generated JQL without
		// the full issue payload.
		"queryMeta": func(r *StoredResult) queryMetaView {
			if q, ok := r.Data.(*QueryResultData); ok {
				return queryMetaView{Query: q.Query, Route: q.Route, JQL: q.JQL, Outcome: q.Outcome, ErrorStage: q.ErrorStage, ErrorCategory: q.ErrorCategory}
			}
			if a, ok := r.Data.(*AttemptResultData); ok {
				meta := queryMetaView{Outcome: a.Outcome, ErrorCategory: a.ErrorCategory}
				if a.Trace != nil {
					meta.Query, meta.Route, meta.JQL, meta.ErrorStage = a.Trace.Query, a.Trace.Route, a.Trace.DisplayJQL, a.Trace.ErrorStage
				}
				return meta
			}
			return queryMetaView{}
		},
		"resultTitle": resultTitle,
	}

	// Parse each view template separately with layout to avoid block name collisions.
	pages := []string{"index.html", "summary.html", "query.html", "digest.html", "enrich.html", "create_epic.html", "weekly_status.html", "attempt.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		tmpls[page] = template.Must(
			template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/layout.html", "templates/"+page),
		)
	}

	ws = &WebServer{
		store:               store,
		port:                port,
		bindHost:            bindHost,
		tmpls:               tmpls,
		startedAt:           time.Now(),
		dependencies:        newDependencyObservations(),
		checkJiraConnection: checkConfiguredJira,
	}
	if db != nil {
		ws.dependencies.observe("cache", nil)
	}
	return ws
}

// resultTitle prefers the original natural-language input for query results,
// including results saved before the title format was fixed. Keep the stored
// title for other result types and incomplete legacy data.
func resultTitle(r *StoredResult) string {
	if r == nil {
		return ""
	}
	if q, ok := r.Data.(*QueryResultData); r.Type == ResultQuery && ok && strings.TrimSpace(q.Query) != "" {
		return "Query: " + q.Query
	}
	return r.Title
}

type cacheStatusView struct {
	CachedIssues int
	Hits         int
	Lookups      int
}

func (s cacheStatusView) HitPercent() float64 {
	if s.Lookups == 0 {
		return 0
	}
	return float64(s.Hits) / float64(s.Lookups) * 100
}

// Start binds to the configured port (or the next available port if busy)
// and begins serving in the background. Returns the actual bound port.
func (ws *WebServer) Start() (int, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ws.handleHealthz)
	mux.HandleFunc("GET /readyz", ws.handleReadyz)
	mux.HandleFunc("GET /api/status", ws.handleStatus)
	mux.HandleFunc("/api/jira/test-connection", ws.handleJiraConnection)
	mux.HandleFunc("/api/history", ws.handleClearHistory)
	mux.HandleFunc("GET /{$}", ws.handleIndex)
	mux.HandleFunc("GET /view/{id}", ws.handleView)
	mux.HandleFunc("GET /api/result/{id}", ws.handleAPIResult)
	mux.HandleFunc("DELETE /api/result/{id}", ws.handleDeleteResult)
	mux.Handle("GET /static/", http.FileServerFS(web.StaticFS))

	const maxAttempts = 20
	var ln net.Listener
	var err error
	port := ws.port

	for i := 0; i < maxAttempts; i++ {
		addr := fmt.Sprintf("%s:%d", ws.bindHost, port)
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		slog.Debug("port busy, trying next", "port", port, "error", err)
		port++
	}
	if err != nil {
		return 0, fmt.Errorf("no available port in range %d-%d: %w", ws.port, ws.port+maxAttempts-1, err)
	}

	ws.port = port
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		ws.port = addr.Port
	}
	ws.startedAt = time.Now()
	ws.httpServer = &http.Server{Handler: mux}
	slog.Info("web dashboard starting", "addr", ln.Addr().String())
	go func() {
		if err := ws.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("web server failed", "error", err)
		}
	}()

	return ws.port, nil
}

// Close stops dashboard requests before its stores are closed by Run.
func (ws *WebServer) Close() error {
	if ws.httpServer == nil {
		return nil
	}
	return ws.httpServer.Close()
}

type serverStatusView struct {
	Status        string                  `json:"status"`
	Uptime        string                  `json:"uptime"`
	UptimeSeconds int64                   `json:"uptime_seconds"`
	Readiness     string                  `json:"readiness"`
	Dependencies  []DependencyObservation `json:"dependencies"`
}

func (ws *WebServer) status() serverStatusView {
	status := statusSince(ws.startedAt)
	status.Dependencies, status.Readiness = ws.dependencies.snapshot(time.Now())
	if ws.store != nil {
		err, checkedAt := ws.store.PersistenceObservation()
		for i, obs := range status.Dependencies {
			if obs.Name != "history" {
				continue
			}
			obs.CheckedAt, obs.State = checkedAt, "ok"
			if checkedAt.IsZero() {
				obs.State = "unknown"
			} else if time.Since(checkedAt) > observationMaxAge {
				obs.State = "stale"
			} else if err != nil {
				obs.State = "failed"
			}
			status.Dependencies[i] = obs
		}
		status.Readiness = observedReadiness(status.Dependencies)
	}
	return status
}

func statusSince(startedAt time.Time) serverStatusView {
	seconds := int64(time.Since(startedAt).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return serverStatusView{Status: "ok", Uptime: (time.Duration(seconds) * time.Second).String(), UptimeSeconds: seconds}
}

// handleReadyz reports recent passive observations. Unknown/stale observations
// are not readiness. Polling performs no dependency operation.
func (ws *WebServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	status := ws.status()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if status.Readiness != "ready" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(status)
}

// handleStatus returns non-sensitive process status for the dashboard header.
func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(ws.status())
}

// handleIndex shows all stored results.
func (ws *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Results []*StoredResult
	}{
		Results: ws.store.List(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ws.tmpls["index.html"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("template error", "template", "index", "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// handleView renders a result by ID using the appropriate template.
func (ws *WebServer) handleView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	result, ok := ws.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	templateName := string(result.Type) + ".html"
	tmpl, ok := ws.tmpls[templateName]
	if !ok {
		http.Error(w, "Unknown result type", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", result); err != nil {
		slog.Error("template error", "template", templateName, "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// handleAPIResult returns result data as JSON for JS charts.
func (ws *WebServer) handleAPIResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	result, ok := ws.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result.Data)
}

// handleHealthz returns 200 OK for container/orchestrator health checks.
func (ws *WebServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok"}`)
}

// handleDeleteResult removes a result by ID.
func (ws *WebServer) handleDeleteResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !ws.store.Delete(id) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
