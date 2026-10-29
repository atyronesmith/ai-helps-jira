package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
)

// serveWithGracefulShutdown starts an HTTP server and shuts it down
// cleanly on SIGTERM/SIGINT, draining connections for up to 5 seconds.
func serveWithGracefulShutdown(srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-quit:
		slog.Info("shutting down", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-errCh:
		return err
	}
}

// noAuthMiddleware wraps an http.Handler to intercept OAuth discovery
// and auth endpoint requests. MCP clients (Claude Code SDK) probe
// /.well-known/, /authorize, /token, and /register before connecting;
// returning a clean JSON response prevents parse errors in the client SDK.
func noAuthMiddleware(next http.Handler) http.Handler {
	authPaths := map[string]bool{
		"/authorize": true,
		"/token":     true,
		"/register":  true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/") || authPaths[r.URL.Path] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not_found"}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Config holds MCP server configuration.
type Config struct {
	WebPort           int
	Transport         string        // "stdio", "sse", or "streamable-http"
	SSEPort           int           // port for SSE / streamable-http transport
	BindHost          string        // bind address: "127.0.0.1" (default) or "0.0.0.0" (container)
	HistoryInstance   string        // stable local history namespace
	HistoryMaxAge     time.Duration // zero disables age retention
	HistoryMaxResults int           // zero disables count retention
	Version           string        // build version reported during MCP initialization; defaults to "dev"
}

// newMCPServer constructs the MCP server and registers all available tools.
// Keeping registration here makes discovery testable without starting listeners.
func newMCPServer(version string, h *Handlers) *server.MCPServer {
	if version == "" {
		version = "dev"
	}

	// Name must match the "jira" key clients use in their MCP config (e.g. Codex's
	// mcp_servers.jira): some clients namespace tools by this self-reported name
	// rather than the config key, so a mismatch causes "unsupported call" errors.
	s := server.NewMCPServer(
		"jira",
		version,
		server.WithToolCapabilities(false),
		server.WithInstructions(
			jiraSearchInputInstructions+
				"For Jira searches, pass the user's exact original natural-language wording of the Jira search clause to jira_query.natural_query. On translation errors, stop and report the error. If it reports ambiguity, ask the user for clarification; do not silently rephrase or retry variants, add or drop constraints, or change dates. Report other translation failures without rewriting the request. "+
				"Call jira_query directly for filtering and sorting. Jira executes the requested ORDER BY; no extra timestamp lookup or client-side sorting is needed. "+
				`For example, call jira_query with {"natural_query":"list my open issues ordered by date created"} for that exact user request. `+
				"For these Jira business requests, do not seek credentials, inspect local files/environment/secrets for authentication, or bypass MCP with direct REST or shell calls. "+
				"If the Jira MCP tools are unavailable, report the connection/tool availability problem; do not attempt a shell or REST workaround. "+
				"Do not pretranslate it to JQL, replace my/me with a username or email, or call jira_lookup_user first. "+
				"Preserve the user's spelling, casing, and spacing; server-side normalization owns typo repair. "+
				"Use jira_translate_query.natural_query when you only need to inspect the translation; it validates locally and does not search. Pass the exact original search clause there too, and stop after a translation error. "+
				"jira_query performs deterministic translation, then uses the configured LLM fallback if needed. "+
				"For requests about work someone 'worked on', treat that as activity; it does not automatically mean currently assigned to them or imply an updated-time filter. Personal requests with supported date ranges use authored comments, worklogs, and status changes, regardless of current assignee. The server reports timezone, inferred years, visible-record scope, and incomplete scans. Unsupported activity constraints require clarification; do not replace them with assignment or update-time searches. "+
				"For recommendation or prioritization requests (for example, 'Which of my open jiras should I work on next?'), use jira_query to retrieve candidate issues. "+
				"When the candidate count is reasonably bounded, call jira_get_issue for each candidate to inspect its full issue content before recommending. "+
				"If there are too many candidates, shortlist using the returned priority, status, and summary, disclose that shortlist in your answer, and inspect those issues with jira_get_issue. "+
				"The host assistant should reason over the inspected issue content and make the recommendation; jira_query only retrieves candidates and does not make recommendations. "+
				"If jira_query returns an error, report that error. Do not seek credentials, inspect local authentication config, or bypass MCP using shell, curl, CLI, or direct REST."),
	)

	add := func(tool mcp.Tool, handler server.ToolHandlerFunc) {
		s.AddTool(tool, h.withDiagnostics(tool.Name, handler))
	}

	// Register core tools.
	add(summaryToolDef(), h.HandleSummary)
	add(translateQueryToolDef(), h.HandleTranslateQuery)
	add(queryToolDef(), h.HandleQuery)
	add(digestToolDef(), h.HandleDigest)
	add(enrichToolDef(), h.HandleEnrich)
	add(createEpicToolDef(), h.HandleCreateEpic)
	add(weeklyStatusToolDef(), h.HandleWeeklyStatus)
	add(summarizeCommentsToolDef(), h.HandleSummarizeComments)
	add(backlogHealthToolDef(), h.HandleBacklogHealth)
	add(findSimilarToolDef(), h.HandleFindSimilar)
	add(confluenceAnalyticsToolDef(), h.HandleConfluenceAnalytics)
	add(confluenceUpdateToolDef(), h.HandleConfluenceUpdate)
	add(confluenceGetPageToolDef(), h.HandleConfluenceGetPage)
	add(confluenceSearchToolDef(), h.HandleConfluenceSearch)
	add(confluenceListPagesToolDef(), h.HandleConfluenceListPages)
	add(confluenceGetCommentsToolDef(), h.HandleConfluenceGetComments)
	add(confluenceAddLabelToolDef(), h.HandleConfluenceAddLabel)
	add(confluenceCreatePageToolDef(), h.HandleConfluenceCreatePage)
	add(confluenceCreateBlogToolDef(), h.HandleConfluenceCreateBlog)

	// CRUD tools.
	add(getIssueToolDef(), h.HandleGetIssue)
	add(createIssueToolDef(), h.HandleCreateIssue)
	add(editIssueToolDef(), h.HandleEditIssue)
	add(getTransitionsToolDef(), h.HandleGetTransitions)
	add(transitionToolDef(), h.HandleTransition)
	add(addCommentToolDef(), h.HandleAddComment)
	add(lookupUserToolDef(), h.HandleLookupUser)
	add(addWorklogToolDef(), h.HandleAddWorklog)
	add(linkIssuesToolDef(), h.HandleLinkIssues)
	add(attachFileToolDef(), h.HandleAttachFile)
	add(watchIssueToolDef(), h.HandleWatchIssue)
	add(unwatchIssueToolDef(), h.HandleUnwatchIssue)
	add(addLabelsToolDef(), h.HandleAddLabels)
	add(removeLabelsToolDef(), h.HandleRemoveLabels)
	add(listSprintsToolDef(), h.HandleListSprints)
	add(voteIssueToolDef(), h.HandleVoteIssue)
	add(unvoteIssueToolDef(), h.HandleUnvoteIssue)

	return s
}

// Run starts the MCP server and web dashboard.
func Run(cfg Config) error {
	store, err := OpenResultStore(ResultStoreOptions{InstanceID: cfg.HistoryInstance, MaxAge: cfg.HistoryMaxAge, MaxResults: cfg.HistoryMaxResults})
	if err != nil {
		return fmt.Errorf("open result history: %w", err)
	}
	defer store.Close()

	db, err := cache.OpenManager()
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer db.Close()

	bindHost := cfg.BindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}

	if bindHost != "127.0.0.1" && bindHost != "localhost" {
		fmt.Fprintf(os.Stderr, "WARNING: Binding to %s — server is accessible on the network. "+
			"Ensure JIRA credentials are not exposed to untrusted clients.\n", bindHost)
	}

	// Start web server (finds an available port starting from cfg.WebPort)
	ws := NewWebServer(store, db, cfg.WebPort, bindHost)
	defer ws.Close()
	actualPort, err := ws.Start()
	if err != nil {
		slog.Error("web dashboard failed to start", "error", err)
		actualPort = cfg.WebPort
	} else {
		fmt.Fprintf(os.Stderr, "Web dashboard: http://%s:%d\n", bindHost, actualPort)
	}

	// Create handlers
	h := NewHandlers(store, db, actualPort, bindHost)
	h.dependencies = ws.dependencies
	s := newMCPServer(cfg.Version, h)

	switch cfg.Transport {
	case "sse":
		sseAddr := fmt.Sprintf("%s:%d", bindHost, cfg.SSEPort)
		// Use localhost for baseURL when binding to 0.0.0.0, since clients
		// connect from the host machine and 0.0.0.0 won't resolve there.
		baseHost := bindHost
		if baseHost == "0.0.0.0" {
			baseHost = "localhost"
		}
		baseURL := fmt.Sprintf("http://%s:%d", baseHost, cfg.SSEPort)

		sseServer := server.NewSSEServer(s,
			server.WithBaseURL(baseURL),
			server.WithKeepAlive(true),
		)

		// Wrap SSE server with middleware that handles OAuth discovery
		// endpoints. Claude Code probes /.well-known/* before connecting;
		// without this, the default 404 body isn't valid JSON and the
		// client fails with "Invalid OAuth error response".
		srv := &http.Server{
			Addr:    sseAddr,
			Handler: noAuthMiddleware(sseServer),
		}

		fmt.Fprintf(os.Stderr, "MCP SSE server: %s/sse\n", baseURL)
		slog.Info("MCP server ready", "transport", "sse", "addr", sseAddr)
		return serveWithGracefulShutdown(srv)

	case "streamable-http":
		httpAddr := fmt.Sprintf("%s:%d", bindHost, cfg.SSEPort)
		baseHost := bindHost
		if baseHost == "0.0.0.0" {
			baseHost = "localhost"
		}

		// Stateless: no session id tracking, every request is independent.
		// Simplest fit for tool-call-only clients (e.g. Open WebUI) behind a
		// container that may be recreated between calls.
		mcpHTTPServer := server.NewStreamableHTTPServer(s, server.WithStateLess(true))

		mux := http.NewServeMux()
		mux.Handle("/mcp", mcpHTTPServer)

		srv := &http.Server{
			Addr:    httpAddr,
			Handler: noAuthMiddleware(mux),
		}

		fmt.Fprintf(os.Stderr, "MCP Streamable HTTP server: http://%s:%d/mcp\n", baseHost, cfg.SSEPort)
		slog.Info("MCP server ready", "transport", "streamable-http", "addr", httpAddr)
		return serveWithGracefulShutdown(srv)

	default: // stdio
		slog.Info("MCP server ready", "transport", "stdio")
		return server.ServeStdio(s)
	}
}
