package mcpserver

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/web"
)

// WebServer serves rich HTML dashboards for MCP tool results.
type WebServer struct {
	store *ResultStore
	port  int
	tmpl  *template.Template
}

// NewWebServer creates a web server with parsed templates.
func NewWebServer(store *ResultStore, port int) *WebServer {
	funcMap := template.FuncMap{
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
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(web.TemplateFS, "templates/*.html"))

	return &WebServer{
		store: store,
		port:  port,
		tmpl:  tmpl,
	}
}

// Start begins listening on the configured port (localhost only).
func (ws *WebServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", ws.handleIndex)
	mux.HandleFunc("GET /view/{id}", ws.handleView)
	mux.HandleFunc("GET /api/result/{id}", ws.handleAPIResult)
	mux.Handle("GET /static/", http.FileServerFS(web.StaticFS))

	addr := fmt.Sprintf("127.0.0.1:%d", ws.port)
	slog.Info("web dashboard starting", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

// handleIndex shows all stored results.
func (ws *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
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
	if err := ws.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("template error", "template", "index", "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// handleView renders a result by ID using the appropriate template.
func (ws *WebServer) handleView(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, ok := ws.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	templateName := string(result.Type) + ".html"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ws.tmpl.ExecuteTemplate(w, templateName, result); err != nil {
		slog.Error("template error", "template", templateName, "error", err)
		http.Error(w, "Internal Server Error", 500)
	}
}

// handleAPIResult returns result data as JSON for JS charts.
func (ws *WebServer) handleAPIResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, ok := ws.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result.Data)
}
