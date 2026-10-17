package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/mcpserver"
)

var (
	flagMCPPort              int
	flagMCPTransport         string
	flagMCPSSEPort           int
	flagMCPBind              string
	flagMCPHistoryInstance   string
	flagMCPHistoryMaxAge     time.Duration
	flagMCPHistoryMaxResults int
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run MCP server with local web dashboard",
	Long: `Start an MCP server for use with Claude Code or other MCP clients.
Also starts a local HTTP server for rich HTML dashboards.

Transports:
  stdio            Single client via stdin/stdout (default, for .mcp.json)
  sse              Multiple clients via legacy HTTP SSE (for older clients)
  streamable-http  Multiple clients via MCP Streamable HTTP (for containers /
                   shared servers; required by clients like Open WebUI that
                   dropped legacy SSE support)

Examples:
  # Single client (stdio) — configure in .mcp.json
  jira-cli mcp

  # Multi-client (SSE) — connect via http://host:8081/sse
  jira-cli mcp --transport sse --sse-port 8081

  # Multi-client (Streamable HTTP) — connect via http://host:8081/mcp
  jira-cli mcp --transport streamable-http --sse-port 8081

  # In a container
  podman run -p 8081:8081 -p 18080:18080 --env-file .env jira-cli

Claude Code .mcp.json for stdio:
  {"mcpServers": {"jira-cli": {"command": "jira-cli", "args": ["mcp"]}}}

Claude Code .mcp.json for SSE:
  {"mcpServers": {"jira-cli": {"type": "sse", "url": "http://localhost:8081/sse", "oauth": {}}}}

Claude Code .mcp.json for Streamable HTTP:
  {"mcpServers": {"jira-cli": {"type": "http", "url": "http://localhost:8081/mcp"}}}`,
	RunE: runMCP,
}

func init() {
	mcpCmd.Flags().IntVar(&flagMCPPort, "port", 18080,
		"Port for the local web dashboard.")
	mcpCmd.Flags().StringVar(&flagMCPTransport, "transport", "stdio",
		"MCP transport: stdio, sse, or streamable-http.")
	mcpCmd.Flags().IntVar(&flagMCPSSEPort, "sse-port", 8081,
		"Port for SSE/streamable-http transport (only used with --transport sse|streamable-http).")
	mcpCmd.Flags().StringVar(&flagMCPBind, "bind", "127.0.0.1",
		"Bind address for servers. Use 0.0.0.0 for containers.")
	mcpCmd.Flags().StringVar(&flagMCPHistoryInstance, "history-instance", "default", "Stable history owner; use a different name for each isolated local instance.")
	mcpCmd.Flags().DurationVar(&flagMCPHistoryMaxAge, "history-max-age", 30*24*time.Hour, "Delete this instance's history older than this duration at startup and on save (0 disables).")
	mcpCmd.Flags().IntVar(&flagMCPHistoryMaxResults, "history-max-results", 1000, "Keep at most this many results for this instance (0 disables).")
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	// Redirect all logging to file — never stdout (MCP stdio uses it)
	setupLogging()
	return mcpserver.Run(mcpserver.Config{
		WebPort:           flagMCPPort,
		Transport:         flagMCPTransport,
		SSEPort:           flagMCPSSEPort,
		BindHost:          flagMCPBind,
		Version:           binaryVersion,
		HistoryInstance:   flagMCPHistoryInstance,
		HistoryMaxAge:     flagMCPHistoryMaxAge,
		HistoryMaxResults: flagMCPHistoryMaxResults,
	})
}
