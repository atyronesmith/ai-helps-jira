package mcpserver

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
)

// Run starts the MCP server on stdio and the web server on the given port.
func Run(webPort int) error {
	store := NewResultStore()

	db, err := cache.Open()
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer db.Close()

	// Start web server in background
	ws := NewWebServer(store, webPort)
	go func() {
		if err := ws.Start(); err != nil {
			slog.Error("web server failed", "error", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "Web dashboard: http://127.0.0.1:%d\n", webPort)

	// Create MCP server
	s := server.NewMCPServer(
		"jira-cli",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	// Create handlers
	h := NewHandlers(store, db, webPort)

	// Register tools
	s.AddTool(summaryToolDef(), h.HandleSummary)
	s.AddTool(queryToolDef(), h.HandleQuery)
	s.AddTool(digestToolDef(), h.HandleDigest)
	s.AddTool(enrichToolDef(), h.HandleEnrich)
	s.AddTool(createEpicToolDef(), h.HandleCreateEpic)
	s.AddTool(weeklyStatusToolDef(), h.HandleWeeklyStatus)

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

	slog.Info("MCP server ready, listening on stdio")
	return server.ServeStdio(s)
}
