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
	WebPort   int
	Transport string // "stdio", "sse", or "streamable-http"
	SSEPort   int    // port for SSE / streamable-http transport
	BindHost  string // bind address: "127.0.0.1" (default) or "0.0.0.0" (container)
	Version   string // build version reported during MCP initialization; defaults to "dev"
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
		server.WithInstructions("For Jira searches, pass the user's exact original natural-language wording to jira_query.natural_query. Do not pretranslate it to JQL, replace my/me with a username or email, or call jira_lookup_user first. Use jira_translate_query.natural_query when you only need to inspect the translation; it validates locally and does not search. jira_query performs deterministic translation, then uses the configured LLM fallback if needed. For recommendation or prioritization requests (for example, 'Which of my open jiras should I work on next?'), use jira_query to retrieve candidate issues. When the candidate count is reasonably bounded, call jira_get_issue for each candidate to inspect its full issue content before recommending. If there are too many candidates, shortlist using the returned priority, status, and summary, disclose that shortlist in your answer, and inspect those issues with jira_get_issue. The host assistant should reason over the inspected issue content and make the recommendation; jira_query only retrieves candidates and does not make recommendations. If jira_query returns an error, report that error; do not seek credentials or bypass MCP with direct REST or shell calls."),
	)

	// Register core tools.
	s.AddTool(summaryToolDef(), h.HandleSummary)
	s.AddTool(translateQueryToolDef(), h.HandleTranslateQuery)
	s.AddTool(queryToolDef(), h.HandleQuery)
	s.AddTool(digestToolDef(), h.HandleDigest)
	s.AddTool(enrichToolDef(), h.HandleEnrich)
	s.AddTool(createEpicToolDef(), h.HandleCreateEpic)
	s.AddTool(weeklyStatusToolDef(), h.HandleWeeklyStatus)
	s.AddTool(summarizeCommentsToolDef(), h.HandleSummarizeComments)
	s.AddTool(backlogHealthToolDef(), h.HandleBacklogHealth)
	s.AddTool(findSimilarToolDef(), h.HandleFindSimilar)
	s.AddTool(confluenceAnalyticsToolDef(), h.HandleConfluenceAnalytics)
	s.AddTool(confluenceUpdateToolDef(), h.HandleConfluenceUpdate)
	s.AddTool(confluenceGetPageToolDef(), h.HandleConfluenceGetPage)
	s.AddTool(confluenceSearchToolDef(), h.HandleConfluenceSearch)
	s.AddTool(confluenceListPagesToolDef(), h.HandleConfluenceListPages)
	s.AddTool(confluenceGetCommentsToolDef(), h.HandleConfluenceGetComments)
	s.AddTool(confluenceAddLabelToolDef(), h.HandleConfluenceAddLabel)
	s.AddTool(confluenceCreatePageToolDef(), h.HandleConfluenceCreatePage)
	s.AddTool(confluenceCreateBlogToolDef(), h.HandleConfluenceCreateBlog)

	// CRUD tools.
	s.AddTool(getIssueToolDef(), h.HandleGetIssue)
	s.AddTool(createIssueToolDef(), h.HandleCreateIssue)
	s.AddTool(editIssueToolDef(), h.HandleEditIssue)
	s.AddTool(getTransitionsToolDef(), h.HandleGetTransitions)
	s.AddTool(transitionToolDef(), h.HandleTransition)
	s.AddTool(addCommentToolDef(), h.HandleAddComment)
	s.AddTool(lookupUserToolDef(), h.HandleLookupUser)
	s.AddTool(addWorklogToolDef(), h.HandleAddWorklog)
	s.AddTool(linkIssuesToolDef(), h.HandleLinkIssues)
	s.AddTool(attachFileToolDef(), h.HandleAttachFile)
	s.AddTool(watchIssueToolDef(), h.HandleWatchIssue)
	s.AddTool(unwatchIssueToolDef(), h.HandleUnwatchIssue)
	s.AddTool(addLabelsToolDef(), h.HandleAddLabels)
	s.AddTool(removeLabelsToolDef(), h.HandleRemoveLabels)
	s.AddTool(listSprintsToolDef(), h.HandleListSprints)
	s.AddTool(voteIssueToolDef(), h.HandleVoteIssue)
	s.AddTool(unvoteIssueToolDef(), h.HandleUnvoteIssue)

	return s
}

// Run starts the MCP server and web dashboard.
func Run(cfg Config) error {
	store := NewResultStore()

	db, err := cache.Open()
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
	actualPort, err := ws.Start()
	if err != nil {
		slog.Error("web dashboard failed to start", "error", err)
		actualPort = cfg.WebPort
	} else {
		fmt.Fprintf(os.Stderr, "Web dashboard: http://%s:%d\n", bindHost, actualPort)
	}

	// Create handlers
	h := NewHandlers(store, db, actualPort, bindHost)
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
