package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
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
	for _, required := range []string{
		"exact original natural-language wording",
		"jira_query.natural_query",
		"jira_translate_query.natural_query",
		"does not search",
		"Do not pretranslate it to JQL",
		"replace my/me with a username or email",
		"call jira_lookup_user first",
		"deterministic translation",
		"configured LLM fallback",
		"report that error",
		"On translation errors, stop and report the error",
		"If it reports ambiguity, ask the user for clarification",
		"Report other translation failures without rewriting the request",
		"worked on",
		"does not automatically mean currently assigned",
		"authored comments, worklogs, and status changes",
		"incomplete scans",
		"do not seek credentials",
		"direct REST or shell calls",
		"Which of my open jiras should I work on next?",
		"retrieve candidate issues",
		"call jira_get_issue for each candidate",
		"disclose that shortlist",
		"host assistant should reason",
		"Call jira_query directly for filtering and sorting",
		"Jira executes the requested ORDER BY",
		"no extra timestamp lookup or client-side sorting",
		"If the Jira MCP tools are unavailable, report the connection/tool availability problem",
	} {
		if !strings.Contains(initResult.Instructions, required) {
			t.Errorf("server instructions missing %q: %q", required, initResult.Instructions)
		}
	}

	toolResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}

	expected := []string{
		"jira_summary",
		"jira_translate_query",
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
	discovered := make(map[string]mcp.Tool, len(toolResult.Tools))
	for _, tool := range toolResult.Tools {
		got[tool.Name] = true
		discovered[tool.Name] = tool
	}
	for _, name := range expected {
		if !got[name] {
			t.Errorf("discovered tools missing %q", name)
		}
	}
	for name, required := range map[string][]string{
		"jira_query": {
			"does not recommend", "jira_get_issue",
			"list my open issues ordered by date created",
			"On translation errors, stop and report the error",
			"if it reports ambiguity, ask the user for clarification",
			"Do not retry variants or add/drop constraints or change dates",
			"worked on",
			"does not automatically mean currently assigned to them or imply an updated-time filter",
			"Call jira_query directly for filtering and sorting",
			"Jira executes the requested ORDER BY",
			"no extra timestamp lookup or client-side sorting",
			"If the Jira MCP tools are unavailable, report the connection/tool availability problem",
			"Do not seek credentials",
			"even before a tool call",
		},
		"jira_get_issue": {"inspect jira_query candidates", "shortlisted issue"},
	} {
		tool := discovered[name]
		for _, phrase := range required {
			if !strings.Contains(tool.Description, phrase) {
				t.Errorf("discovered %s description missing %q: %q", name, phrase, tool.Description)
			}
		}
	}
	for name, required := range map[string][]string{
		"jira_query": {
			"Do not silently rephrase or retry variants, add/drop constraints, or change dates",
			"ask the user for clarification only when the error reports ambiguity",
			"No Jira issue search ran after a translation error",
			"worked on",
			"does not automatically imply current assignment or an updated-time filter",
		},
		"jira_translate_query": {
			"exact original wording verbatim",
			"ask the user for clarification only when the error reports ambiguity",
			"No Jira issue search ran",
			"Do not retry variants or add/drop constraints or change dates",
			"worked on",
			"does not automatically imply current assignment or an updated-time filter",
		},
	} {
		input, err := json.Marshal(discovered[name].InputSchema.Properties["natural_query"])
		if err != nil {
			t.Fatalf("marshal %s natural_query schema: %v", name, err)
		}
		for _, phrase := range required {
			if !strings.Contains(string(input), phrase) {
				t.Errorf("discovered %s natural_query description missing %q: %s", name, phrase, input)
			}
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
	if got, want := len(toolResult.Tools), 36; got != want {
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
		"jira_translate_query",
	} {
		if !got[name] {
			t.Errorf("Streamable HTTP discovery missing %q", name)
		}
	}
}
