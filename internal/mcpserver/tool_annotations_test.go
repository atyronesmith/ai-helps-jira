package mcpserver

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestToolDefinitionsHaveExplicitAnnotations(t *testing.T) {
	tools := []mcp.Tool{
		summaryToolDef(), queryToolDef(), digestToolDef(), enrichToolDef(), createEpicToolDef(),
		weeklyStatusToolDef(), summarizeCommentsToolDef(), backlogHealthToolDef(), findSimilarToolDef(),
		confluenceAnalyticsToolDef(), confluenceUpdateToolDef(), confluenceGetPageToolDef(),
		confluenceSearchToolDef(), confluenceListPagesToolDef(), confluenceGetCommentsToolDef(),
		confluenceAddLabelToolDef(), confluenceCreatePageToolDef(), confluenceCreateBlogToolDef(),
		getIssueToolDef(), createIssueToolDef(), editIssueToolDef(), getTransitionsToolDef(),
		transitionToolDef(), addCommentToolDef(), lookupUserToolDef(), attachFileToolDef(),
		addWorklogToolDef(), linkIssuesToolDef(), watchIssueToolDef(), unwatchIssueToolDef(),
		addLabelsToolDef(), removeLabelsToolDef(), listSprintsToolDef(), voteIssueToolDef(),
		unvoteIssueToolDef(),
	}
	if len(tools) != 35 {
		t.Fatalf("got %d tool definitions, want 35", len(tools))
	}

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			annotations := tool.Annotations
			if annotations.ReadOnlyHint == nil || annotations.DestructiveHint == nil ||
				annotations.IdempotentHint == nil || annotations.OpenWorldHint == nil {
				t.Fatal("expected all four annotation hints to be explicit")
			}
			if !*annotations.OpenWorldHint {
				t.Fatal("expected external Jira, Confluence, or LLM interaction to be marked open-world")
			}
		})
	}
}

func TestToolAnnotationClassifications(t *testing.T) {
	tests := []struct {
		name        string
		tool        mcp.Tool
		readOnly    bool
		destructive bool
		idempotent  bool
	}{
		{name: "Jira search", tool: queryToolDef(), readOnly: true, destructive: false, idempotent: true},
		{name: "Confluence read", tool: confluenceGetPageToolDef(), readOnly: true, destructive: false, idempotent: true},
		{name: "Jira create", tool: createIssueToolDef(), readOnly: false, destructive: false, idempotent: false},
		{name: "Jira edit", tool: editIssueToolDef(), readOnly: false, destructive: true, idempotent: true},
		{name: "Jira transition", tool: transitionToolDef(), readOnly: false, destructive: true, idempotent: false},
		{name: "Confluence replacement", tool: confluenceUpdateToolDef(), readOnly: false, destructive: true, idempotent: true},
		{name: "Jira label removal", tool: removeLabelsToolDef(), readOnly: false, destructive: true, idempotent: true},
		{name: "Conditional report post", tool: weeklyStatusToolDef(), readOnly: false, destructive: false, idempotent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := tt.tool.Annotations
			if annotations.ReadOnlyHint == nil || *annotations.ReadOnlyHint != tt.readOnly {
				t.Fatalf("readOnlyHint = %v, want %t", annotations.ReadOnlyHint, tt.readOnly)
			}
			if annotations.DestructiveHint == nil || *annotations.DestructiveHint != tt.destructive {
				t.Fatalf("destructiveHint = %v, want %t", annotations.DestructiveHint, tt.destructive)
			}
			if annotations.IdempotentHint == nil || *annotations.IdempotentHint != tt.idempotent {
				t.Fatalf("idempotentHint = %v, want %t", annotations.IdempotentHint, tt.idempotent)
			}
			if annotations.OpenWorldHint == nil || !*annotations.OpenWorldHint {
				t.Fatalf("openWorldHint = %v, want true", annotations.OpenWorldHint)
			}
		})
	}
}
