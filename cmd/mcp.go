package cmd

import (
	"github.com/spf13/cobra"

	"github.com/atyronesmith/ai-helps-jira/internal/mcpserver"
)

var flagMCPPort int

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run MCP server (stdio) with local web dashboard",
	Long: `Start an MCP server on stdin/stdout for use with Claude Code.
Also starts a local HTTP server for rich HTML dashboards.

Configure in .mcp.json:
  {"mcpServers": {"jira-cli": {"command": "jira-cli", "args": ["mcp"]}}}`,
	RunE: runMCP,
}

func init() {
	mcpCmd.Flags().IntVar(&flagMCPPort, "port", 18080,
		"Port for the local web dashboard.")
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	// Redirect all logging to file — never stdout (MCP uses stdio)
	setupLogging()
	return mcpserver.Run(flagMCPPort)
}
