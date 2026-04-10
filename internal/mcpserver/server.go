package mcpserver

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
)

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

		fmt.Fprintf(os.Stderr, "MCP SSE server: %s/sse\n", baseURL)
		slog.Info("MCP server ready", "transport", "sse", "addr", sseAddr)
		return sseServer.Start(sseAddr)

	default: // stdio
		slog.Info("MCP server ready", "transport", "stdio")
		return server.ServeStdio(s)
	}
}
