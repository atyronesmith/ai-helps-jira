package cmd

import (
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/mcpserver"
)

var (
	flagMCPPort      int
	flagMCPTransport string
	flagMCPSSEPort   int
	flagMCPBind      string
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run MCP server with local web dashboard",
	Long: `Start an MCP server for use with Claude Code or other MCP clients.
Also starts a local HTTP server for rich HTML dashboards.

Transports:
  stdio  Single client via stdin/stdout (default, for .mcp.json)
  sse    Multiple clients via HTTP SSE (for containers / shared servers)

Examples:
  # Single client (stdio) — configure in .mcp.json
  jira-cli mcp

  # Multi-client (SSE) — connect via http://host:8081/sse
  jira-cli mcp --transport sse --sse-port 8081

  # In a container
  podman run -p 8081:8081 -p 18080:18080 --env-file .env jira-cli

Claude Code .mcp.json for stdio:
  {"mcpServers": {"jira-cli": {"command": "jira-cli", "args": ["mcp"]}}}

Claude Code .mcp.json for SSE:
  {"mcpServers": {"jira-cli": {"type": "sse", "url": "http://localhost:8081/sse"}}}`,
	RunE: runMCP,
}

func init() {
	mcpCmd.Flags().IntVar(&flagMCPPort, "port", 18080,
		"Port for the local web dashboard.")
	mcpCmd.Flags().StringVar(&flagMCPTransport, "transport", "stdio",
		"MCP transport: stdio or sse.")
	mcpCmd.Flags().IntVar(&flagMCPSSEPort, "sse-port", 8081,
		"Port for SSE transport (only used with --transport sse).")
	mcpCmd.Flags().StringVar(&flagMCPBind, "bind", "127.0.0.1",
		"Bind address for servers. Use 0.0.0.0 for containers.")
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	// Redirect all logging to file — never stdout (MCP stdio uses it)
	setupLogging()
	return mcpserver.Run(mcpserver.Config{
		WebPort:   flagMCPPort,
		Transport: flagMCPTransport,
		SSEPort:   flagMCPSSEPort,
		BindHost:  flagMCPBind,
	})
}
