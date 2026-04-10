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
// requests. MCP clients probe /.well-known/ paths before connecting;
// returning a clean JSON 404 prevents parse errors in the client SDK.
func noAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/") {
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
	Transport string // "stdio" or "sse"
	SSEPort   int    // port for SSE transport
	BindHost  string // bind address: "127.0.0.1" (default) or "0.0.0.0" (container)
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

	// Start web server in background
	ws := NewWebServer(store, cfg.WebPort, bindHost)
	go func() {
		if err := ws.Start(); err != nil {
			slog.Error("web server failed", "error", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "Web dashboard: http://%s:%d\n", bindHost, cfg.WebPort)

	// Create MCP server
	s := server.NewMCPServer(
		"jira-cli",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	// Create handlers
	h := NewHandlers(store, db, cfg.WebPort, bindHost)

	// Register tools
	s.AddTool(summaryToolDef(), h.HandleSummary)
	s.AddTool(queryToolDef(), h.HandleQuery)
	s.AddTool(digestToolDef(), h.HandleDigest)
	s.AddTool(enrichToolDef(), h.HandleEnrich)
	s.AddTool(createEpicToolDef(), h.HandleCreateEpic)
	s.AddTool(weeklyStatusToolDef(), h.HandleWeeklyStatus)
	s.AddTool(summarizeCommentsToolDef(), h.HandleSummarizeComments)
	s.AddTool(backlogHealthToolDef(), h.HandleBacklogHealth)
	s.AddTool(findSimilarToolDef(), h.HandleFindSimilar)

	// CRUD tools
	s.AddTool(getIssueToolDef(), h.HandleGetIssue)
	s.AddTool(createIssueToolDef(), h.HandleCreateIssue)
	s.AddTool(editIssueToolDef(), h.HandleEditIssue)
	s.AddTool(getTransitionsToolDef(), h.HandleGetTransitions)
	s.AddTool(transitionToolDef(), h.HandleTransition)
	s.AddTool(addCommentToolDef(), h.HandleAddComment)
	s.AddTool(lookupUserToolDef(), h.HandleLookupUser)
	s.AddTool(linkIssuesToolDef(), h.HandleLinkIssues)
	s.AddTool(attachFileToolDef(), h.HandleAttachFile)

	switch cfg.Transport {
	case "sse":
		sseAddr := fmt.Sprintf("%s:%d", bindHost, cfg.SSEPort)
		baseURL := fmt.Sprintf("http://%s:%d", bindHost, cfg.SSEPort)

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

	default: // stdio
		slog.Info("MCP server ready", "transport", "stdio")
		return server.ServeStdio(s)
	}
}
