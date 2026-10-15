package mcpserver

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestMCPServerDiscovery(t *testing.T) {
	const version = "2.4.1-test"
	server := newMCPServer(version, &Handlers{})

	mcpClient, err := client.NewInProcessClient(server)
	if err != nil {
		t.Fatalf("create in-process client: %v", err)
	}
	defer mcpClient.Close()

	ctx := context.Background()
	if err := mcpClient.Start(ctx); err != nil {
		t.Fatalf("start in-process client: %v", err)
	}
	initResult, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{})
	if err != nil {
		t.Fatalf("initialize MCP client: %v", err)
	}
	if got := initResult.ServerInfo.Name; got != "jira" {
		t.Errorf("server name = %q, want %q", got, "jira")
	}
	if got := initResult.ServerInfo.Version; got != version {
		t.Errorf("server version = %q, want %q", got, version)
	}

	toolResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}

	expected := []string{
		"jira_summary",
		"jira_query",
		"jira_digest",
		"jira_enrich",
		"jira_create_epic",
		"jira_weekly_status",
		"jira_summarize_comments",
		"jira_backlog_health",
		"jira_find_similar",
		"jira_confluence_analytics",
		"jira_confluence_update",
		"jira_confluence_get_page",
		"jira_confluence_search",
		"jira_confluence_list_pages",
		"jira_confluence_get_comments",
		"jira_confluence_add_label",
		"jira_confluence_create_page",
		"jira_confluence_create_blog",
		"jira_get_issue",
		"jira_create_issue",
		"jira_edit_issue",
		"jira_get_transitions",
		"jira_transition",
		"jira_add_comment",
		"jira_lookup_user",
		"jira_add_worklog",
		"jira_link_issues",
		"jira_attach_file",
		"jira_watch_issue",
		"jira_unwatch_issue",
		"jira_add_labels",
		"jira_remove_labels",
		"jira_list_sprints",
		"jira_vote_issue",
		"jira_unvote_issue",
	}
	if len(toolResult.Tools) != len(expected) {
		t.Fatalf("discovered %d tools, want %d", len(toolResult.Tools), len(expected))
	}
	got := make(map[string]bool, len(toolResult.Tools))
	for _, tool := range toolResult.Tools {
		got[tool.Name] = true
	}
	for _, name := range expected {
		if !got[name] {
			t.Errorf("discovered tools missing %q", name)
		}
	}
}

func TestMCPServerDefaultsVersionToDev(t *testing.T) {
	server := newMCPServer("", &Handlers{})
	mcpClient, err := client.NewInProcessClient(server)
	if err != nil {
		t.Fatalf("create in-process client: %v", err)
	}
	defer mcpClient.Close()

	ctx := context.Background()
	if err := mcpClient.Start(ctx); err != nil {
		t.Fatalf("start in-process client: %v", err)
	}
	initResult, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{})
	if err != nil {
		t.Fatalf("initialize MCP client: %v", err)
	}
	if got := initResult.ServerInfo.Version; got != "dev" {
		t.Errorf("server version = %q, want %q", got, "dev")
	}
}

func TestMCPServerStreamableHTTPDiscovery(t *testing.T) {
	mcpServer := newMCPServer("2.4.1-test", &Handlers{})
	httpServer := server.NewTestStreamableHTTPServer(mcpServer, server.WithStateLess(true))
	defer httpServer.Close()

	mcpClient, err := client.NewStreamableHttpClient(httpServer.URL)
	if err != nil {
		t.Fatalf("create Streamable HTTP client: %v", err)
	}
	defer mcpClient.Close()

	ctx := context.Background()
	if err := mcpClient.Start(ctx); err != nil {
		t.Fatalf("start Streamable HTTP client: %v", err)
	}
	if _, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize Streamable HTTP client: %v", err)
	}

	toolResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools over Streamable HTTP: %v", err)
	}
	if got, want := len(toolResult.Tools), 35; got != want {
		t.Fatalf("discovered %d tools over Streamable HTTP, want %d", got, want)
	}

	got := make(map[string]bool, len(toolResult.Tools))
	for _, tool := range toolResult.Tools {
		got[tool.Name] = true
	}
	for _, name := range []string{
		"jira_get_issue",
		"jira_get_transitions",
		"jira_transition",
		"jira_add_comment",
		"jira_query",
	} {
		if !got[name] {
			t.Errorf("Streamable HTTP discovery missing %q", name)
		}
	}
}
