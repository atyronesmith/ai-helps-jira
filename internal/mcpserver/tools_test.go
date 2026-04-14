package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
)

// makeRequest builds a CallToolRequest with the given arguments.
func makeRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

// newTestHandlers creates a Handlers with an in-memory result store and
// a real SQLite cache in a temp directory.
func newTestHandlers(t *testing.T) *Handlers {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	db, err := cache.Open()
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := &ResultStore{results: make(map[string]*StoredResult)}
	return NewHandlers(store, db, 18080, "127.0.0.1")
}

// --- Helper function tests ---

func TestGetString(t *testing.T) {
	req := makeRequest(map[string]any{"key": "value"})
	if got := getString(req, "key"); got != "value" {
		t.Errorf("getString = %q, want %q", got, "value")
	}
	if got := getString(req, "missing"); got != "" {
		t.Errorf("getString(missing) = %q, want empty", got)
	}
}

func TestGetStringNilArgs(t *testing.T) {
	req := makeRequest(nil)
	if got := getString(req, "key"); got != "" {
		t.Errorf("getString(nil args) = %q, want empty", got)
	}
}

func TestGetBool(t *testing.T) {
	req := makeRequest(map[string]any{"flag": true})
	if got := getBool(req, "flag"); !got {
		t.Error("getBool(flag) = false, want true")
	}
	if got := getBool(req, "missing"); got {
		t.Error("getBool(missing) = true, want false")
	}
}

func TestGetFloat(t *testing.T) {
	req := makeRequest(map[string]any{"num": 42.5})
	if got := getFloat(req, "num"); got != 42.5 {
		t.Errorf("getFloat = %f, want 42.5", got)
	}
	if got := getFloat(req, "missing"); got != 0 {
		t.Errorf("getFloat(missing) = %f, want 0", got)
	}
}

func TestGetStringSlice(t *testing.T) {
	req := makeRequest(map[string]any{
		"tags": []any{"a", "b", "c"},
	})
	got := getStringSlice(req, "tags")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("getStringSlice = %v, want [a b c]", got)
	}

	got = getStringSlice(req, "missing")
	if got != nil {
		t.Errorf("getStringSlice(missing) = %v, want nil", got)
	}
}

func TestGetBody(t *testing.T) {
	// body parameter
	req := makeRequest(map[string]any{"body": "<p>hello</p>"})
	body, errMsg := getBody(req)
	if errMsg != "" {
		t.Fatalf("getBody error: %s", errMsg)
	}
	if body != "<p>hello</p>" {
		t.Errorf("getBody = %q, want %q", body, "<p>hello</p>")
	}

	// neither body nor body_file
	req = makeRequest(map[string]any{})
	_, errMsg = getBody(req)
	if errMsg == "" {
		t.Error("getBody with no body/body_file should return error")
	}
}

func TestGetBodyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.html")
	if err := os.WriteFile(path, []byte("<h1>test</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := makeRequest(map[string]any{"body_file": path})
	body, errMsg := getBody(req)
	if errMsg != "" {
		t.Fatalf("getBody(body_file) error: %s", errMsg)
	}
	if body != "<h1>test</h1>" {
		t.Errorf("getBody(body_file) = %q, want %q", body, "<h1>test</h1>")
	}

	// non-existent file
	req = makeRequest(map[string]any{"body_file": "/nonexistent/file.html"})
	_, errMsg = getBody(req)
	if errMsg == "" {
		t.Error("getBody with bad body_file should return error")
	}
}

func TestViewURL(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	h := &Handlers{store: store, webPort: 18080, bindHost: "127.0.0.1"}
	got := h.viewURL("abc-123")
	want := "http://127.0.0.1:18080/view/abc-123"
	if got != want {
		t.Errorf("viewURL = %q, want %q", got, want)
	}

	h.bindHost = "0.0.0.0"
	got = h.viewURL("abc-123")
	want = "http://127.0.0.1:18080/view/abc-123"
	if got != want {
		t.Errorf("viewURL(0.0.0.0) = %q, want %q", got, want)
	}
}

// --- Tool definition tests ---

func TestAllToolDefsHaveDescriptions(t *testing.T) {
	defs := []struct {
		name string
		tool mcp.Tool
	}{
		{"summary", summaryToolDef()},
		{"query", queryToolDef()},
		{"digest", digestToolDef()},
		{"enrich", enrichToolDef()},
		{"weeklyStatus", weeklyStatusToolDef()},
		{"backlogHealth", backlogHealthToolDef()},
		{"summarizeComments", summarizeCommentsToolDef()},
		{"findSimilar", findSimilarToolDef()},
		{"createEpic", createEpicToolDef()},
		{"confluenceAnalytics", confluenceAnalyticsToolDef()},
		{"confluenceUpdate", confluenceUpdateToolDef()},
		{"confluenceGetPage", confluenceGetPageToolDef()},
		{"confluenceSearch", confluenceSearchToolDef()},
		{"confluenceListPages", confluenceListPagesToolDef()},
		{"confluenceGetComments", confluenceGetCommentsToolDef()},
		{"confluenceAddLabel", confluenceAddLabelToolDef()},
		{"confluenceCreatePage", confluenceCreatePageToolDef()},
		{"confluenceCreateBlog", confluenceCreateBlogToolDef()},
		{"getIssue", getIssueToolDef()},
		{"createIssue", createIssueToolDef()},
		{"editIssue", editIssueToolDef()},
		{"getTransitions", getTransitionsToolDef()},
		{"transition", transitionToolDef()},
		{"addComment", addCommentToolDef()},
		{"lookupUser", lookupUserToolDef()},
		{"linkIssues", linkIssuesToolDef()},
		{"attachFile", attachFileToolDef()},
	}

	if len(defs) != 27 {
		t.Errorf("expected 27 tool definitions, got %d", len(defs))
	}

	for _, d := range defs {
		t.Run(d.name, func(t *testing.T) {
			if d.tool.Name == "" {
				t.Error("tool has no name")
			}
			if d.tool.Description == "" {
				t.Error("tool has no description")
			}
		})
	}
}

// --- Handler validation tests (missing required params) ---

func TestHandleGetIssue_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	req := makeRequest(map[string]any{})
	result, err := h.HandleGetIssue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleCreateIssue_MissingFields(t *testing.T) {
	h := newTestHandlers(t)

	// missing both
	result, err := h.HandleCreateIssue(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "summary and issue_type are required")

	// missing issue_type
	result, err = h.HandleCreateIssue(context.Background(), makeRequest(map[string]any{
		"summary": "test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
}

func TestHandleEditIssue_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleEditIssue(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleEditIssue_NoFields(t *testing.T) {
	h := newTestHandlers(t)
	// Set env vars so config loads but no fields provided
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleEditIssue(context.Background(), makeRequest(map[string]any{
		"issue_key": "TEST-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "no fields provided")
}

func TestHandleGetTransitions_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleGetTransitions(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleTransition_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleTransition(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key and transition_id are required")
}

func TestHandleAddComment_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleAddComment(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key and body are required")
}

func TestHandleLookupUser_MissingQuery(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleLookupUser(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "query is required")
}

func TestHandleLinkIssues_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleLinkIssues(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "inward_issue, outward_issue, and link_type are required")
}

func TestHandleAttachFile_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleAttachFile(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key and file_path are required")
}

func TestHandleSummarizeComments_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	// Need config env for this handler (it loads config before checking key)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")
	t.Setenv("LLM_PROVIDER", "ollama")

	result, err := h.HandleSummarizeComments(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleFindSimilar_MissingKeyAndText(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")
	t.Setenv("LLM_PROVIDER", "ollama")

	result, err := h.HandleFindSimilar(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either issue_key or text is required")
}

func TestHandleConfluenceSearch_MissingCQL(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceSearch(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "cql is required")
}

func TestHandleConfluenceListPages_MissingSpaceKey(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceListPages(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "space_key is required")
}

func TestHandleConfluenceGetComments_MissingPageID(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceGetComments(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "page_id is required")
}

func TestHandleConfluenceAddLabel_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	// missing both
	result, err := h.HandleConfluenceAddLabel(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "page_id is required")

	// missing label
	result, err = h.HandleConfluenceAddLabel(context.Background(), makeRequest(map[string]any{
		"page_id": "123",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "label is required")
}

func TestHandleConfluenceAnalytics_MissingPageID(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceAnalytics(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "page_id is required")
}

func TestHandleConfluenceGetPage_MissingIDAndTitle(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceGetPage(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either page_id or title is required")
}

func TestHandleConfluenceUpdate_MissingIDAndTitle(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceUpdate(context.Background(), makeRequest(map[string]any{
		"body": "<p>test</p>",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either page_id or title is required")
}

func TestHandleConfluenceUpdate_MissingBody(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceUpdate(context.Background(), makeRequest(map[string]any{
		"page_id": "123",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either body or body_file is required")
}

func TestHandleConfluenceCreatePage_MissingBody(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceCreatePage(context.Background(), makeRequest(map[string]any{
		"space_id":  "123",
		"parent_id": "456",
		"title":     "Test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either body or body_file is required")
}

func TestHandleConfluenceCreateBlog_MissingBody(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")

	result, err := h.HandleConfluenceCreateBlog(context.Background(), makeRequest(map[string]any{
		"space_id": "123",
		"title":    "Test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either body or body_file is required")
}

// --- Config validation tests ---

func TestHandlersMissingConfig(t *testing.T) {
	h := newTestHandlers(t)
	// Clear env so config.Load fails
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "")

	// Handlers that use loadConfig (requires LLM too)
	result, err := h.HandleSummary(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)

	result, err = h.HandleQuery(context.Background(), makeRequest(map[string]any{"query": "test"}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
}

// --- Result store tests ---

func TestResultStore_SaveAndGet(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultQuery, "Test Query", &QueryResultData{Query: "test"})

	result, ok := store.Get(id)
	if !ok {
		t.Fatal("result not found after save")
	}
	if result.Title != "Test Query" {
		t.Errorf("title = %q, want %q", result.Title, "Test Query")
	}
	if result.Type != ResultQuery {
		t.Errorf("type = %q, want %q", result.Type, ResultQuery)
	}
}

func TestResultStore_List(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "First", &QueryResultData{})
	store.Save(ResultQuery, "Second", &QueryResultData{})

	list := store.List()
	if len(list) != 2 {
		t.Fatalf("list length = %d, want 2", len(list))
	}
	// Most recent first
	if list[0].Title != "Second" {
		t.Errorf("first item = %q, want Second", list[0].Title)
	}
}

func TestResultStore_Delete(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	id := store.Save(ResultQuery, "Delete me", &QueryResultData{})

	if !store.Delete(id) {
		t.Error("delete returned false")
	}
	if _, ok := store.Get(id); ok {
		t.Error("result still found after delete")
	}
	if store.Delete(id) {
		t.Error("second delete returned true")
	}
}

// --- isContainerType tests ---

func TestIsContainerType(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"Initiative", true},
		{"initiative", true},
		{"Feature", true},
		{"feature", true},
		{"Epic", false},
		{"Story", false},
		{"Bug", false},
		{"Task", false},
	}
	for _, tc := range cases {
		if got := isContainerType(tc.input); got != tc.want {
			t.Errorf("isContainerType(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// --- Assertion helpers ---

func resultText(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func assertIsError(t *testing.T, r *mcp.CallToolResult) {
	t.Helper()
	if !r.IsError {
		t.Errorf("expected error result, got success: %s", resultText(r))
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if len(s) == 0 {
		t.Errorf("empty string, expected to contain %q", substr)
		return
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return
		}
	}
	t.Errorf("string %q does not contain %q", s, substr)
}
