package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/atyronesmith/ai-helps-jira/internal/cache"
	"github.com/atyronesmith/ai-helps-jira/internal/config"
	"github.com/atyronesmith/ai-helps-jira/internal/jira"
	"github.com/atyronesmith/ai-helps-jira/internal/llm"
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

	db, err := cache.OpenManager()
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := &ResultStore{results: make(map[string]*StoredResult)}
	return NewHandlers(store, db, 18080, "127.0.0.1")
}

// setTestJIRAEnv sets the minimum env vars for handlers that call loadJIRAConfig.
func setTestJIRAEnv(t *testing.T) {
	t.Helper()
	t.Setenv("JIRA_SERVER", "https://test.atlassian.net")
	t.Setenv("JIRA_EMAIL", "test@test.com")
	t.Setenv("JIRA_API_TOKEN", "fake-token")
	t.Setenv("JIRA_PROJECT", "TEST")
}

// setTestFullEnv sets env vars for handlers that call loadConfig (JIRA + LLM).
func setTestFullEnv(t *testing.T) {
	t.Helper()
	setTestJIRAEnv(t)
	t.Setenv("LLM_PROVIDER", "ollama")
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
		{"translateQuery", translateQueryToolDef()},
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
		{"addWorklog", addWorklogToolDef()},
		{"linkIssues", linkIssuesToolDef()},
		{"attachFile", attachFileToolDef()},
		{"watchIssue", watchIssueToolDef()},
		{"unwatchIssue", unwatchIssueToolDef()},
		{"addLabels", addLabelsToolDef()},
		{"removeLabels", removeLabelsToolDef()},
		{"listSprints", listSprintsToolDef()},
		{"voteIssue", voteIssueToolDef()},
		{"unvoteIssue", unvoteIssueToolDef()},
	}

	if len(defs) != 36 {
		t.Errorf("expected 36 tool definitions, got %d", len(defs))
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

	queryTool := queryToolDef()
	if !slices.Contains(queryTool.InputSchema.Required, "natural_query") {
		t.Errorf("jira_query required arguments = %v, want natural_query", queryTool.InputSchema.Required)
	}
	translateTool := translateQueryToolDef()
	if !slices.Contains(translateTool.InputSchema.Required, "natural_query") {
		t.Errorf("jira_translate_query required arguments = %v, want natural_query", translateTool.InputSchema.Required)
	}
	if _, ok := translateTool.InputSchema.Properties["natural_query"]; !ok {
		t.Error("jira_translate_query schema is missing natural_query")
	}
	if _, ok := queryTool.InputSchema.Properties["natural_query"]; !ok {
		t.Error("jira_query schema is missing natural_query")
	}
	if _, ok := queryTool.InputSchema.Properties["query"]; ok {
		t.Error("jira_query schema still exposes the legacy query alias")
	}
	if _, ok := queryTool.InputSchema.Properties["exact_count"]; !ok {
		t.Error("jira_query schema is missing exact_count")
	}
	querySchema, _ := json.Marshal(queryTool.InputSchema.Properties["natural_query"])
	for _, required := range []string{"verbatim", "Which of my open jiras should I work on next?", "my open Jiras", "do not look up or replace", "return the MCP error", "do not bypass it with REST or shell"} {
		if !strings.Contains(string(querySchema), required) {
			t.Errorf("natural_query description is missing %q: %s", required, querySchema)
		}
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
	setTestJIRAEnv(t)

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

func TestHandleAddWorklog_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleAddWorklog(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key and time_spent are required")
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

func TestHandleWatchIssue_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleWatchIssue(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleUnwatchIssue_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleUnwatchIssue(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleAddLabels_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleAddLabels(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key and labels are required")
}

func TestHandleRemoveLabels_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleRemoveLabels(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key and labels are required")
}

func TestHandleVoteIssue_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleVoteIssue(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleUnvoteIssue_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	result, err := h.HandleUnvoteIssue(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestParseCommaSeparated(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"bug,urgent", []string{"bug", "urgent"}},
		{" bug , urgent , ", []string{"bug", "urgent"}},
		{"single", []string{"single"}},
		{",,,", nil},
		{"", nil},
	}
	for _, tc := range cases {
		got := parseCommaSeparated(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("parseCommaSeparated(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseCommaSeparated(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestHandleSummarizeComments_MissingKey(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)

	result, err := h.HandleSummarizeComments(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "issue_key is required")
}

func TestHandleFindSimilar_MissingKeyAndText(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)

	result, err := h.HandleFindSimilar(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either issue_key or text is required")
}

func TestHandleConfluenceSearch_MissingCQL(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	result, err := h.HandleConfluenceSearch(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "cql is required")
}

func TestHandleConfluenceListPages_MissingSpaceKey(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	result, err := h.HandleConfluenceListPages(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "space_key is required")
}

func TestHandleConfluenceGetComments_MissingPageID(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	result, err := h.HandleConfluenceGetComments(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "page_id is required")
}

func TestHandleConfluenceAddLabel_MissingFields(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

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
	setTestJIRAEnv(t)

	result, err := h.HandleConfluenceAnalytics(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "page_id is required")
}

func TestHandleConfluenceGetPage_MissingIDAndTitle(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	result, err := h.HandleConfluenceGetPage(context.Background(), makeRequest(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	assertIsError(t, result)
	assertContains(t, resultText(result), "either page_id or title is required")
}

func TestHandleConfluenceUpdate_MissingIDAndTitle(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

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
	setTestJIRAEnv(t)

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

	result, err = h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "test"}))
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
	if !strings.Contains(s, substr) {
		t.Errorf("string %q does not contain %q", s, substr)
	}
}

// --- Query / digest handler translation tests ---
//
// These pin the Phase-4 integration: the query and digest handlers route their
// natural-language input through Handlers.translate / Handlers.digestTranslate
// (stubbed here so no real LLM is invoked), then feed the generated JQL into the
// JIRA search. jqlSearchServer stands in for a live JIRA so a test can assert the
// translated JQL actually reached the search call.

// jqlSearchServer returns an httptest server that answers a POST to
// /rest/api/3/search/jql, recording the received JQL into *recorded.
func jqlSearchServer(t *testing.T, issues []map[string]any, recorded *string) *httptest.Server {
	return jqlSearchServerWithTotal(t, issues, len(issues), recorded)
}

func jqlSearchServerWithTotal(t *testing.T, issues []map[string]any, total int, recorded *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			JQL string `json:"jql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			*recorded = req.JQL
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": issues, "total": total, "isLast": true})
	}))
	t.Cleanup(ts.Close)
	return ts
}

// sampleJQLIssues builds the /search/jql response shape the client expects.
func sampleJQLIssues(keys ...string) []map[string]any {
	statuses := []string{"Open", "In Progress"}
	priorities := []string{"High", "Medium"}
	issues := make([]map[string]any, 0, len(keys))
	for i, key := range keys {
		issues = append(issues, map[string]any{
			"key": key,
			"fields": map[string]any{
				"summary":  "Summary for " + key,
				"status":   map[string]any{"name": statuses[i%len(statuses)]},
				"priority": map[string]any{"name": priorities[i%len(priorities)]},
				"updated":  "2026-01-15T10:00:00.000+0000",
				"assignee": map[string]any{"emailAddress": "alice@example.com"},
			},
		})
	}
	return issues
}

// A translator error must surface as an error result, not a panic or an empty
// success.
func TestHandleQuery_TranslationErrorSurfaces(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return nil, errors.New("translation boom")
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "my open bugs"}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	assertIsError(t, res)
	assertContains(t, resultText(res), "JQL generation failed")
}

// The happy path: the natural query is forwarded to the translator, the returned
// JQL drives the JIRA search, and the route/JQL/issues are persisted for the web
// view.
func TestHandleQuery_FastPathDrivesSearchAndStores(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	var recorded string
	ts := jqlSearchServer(t, sampleJQLIssues("TEST-1", "TEST-2"), &recorded)
	t.Setenv("JIRA_SERVER", ts.URL)

	var gotQuery string
	h.translate = func(_ context.Context, _ *config.Config, query string) (*llm.QueryResult, error) {
		gotQuery = query
		return &llm.QueryResult{JQL: "project = TEST AND status = Open", Route: "fast_path"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "my open bugs in TEST"}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}

	if gotQuery != "my open bugs in TEST" {
		t.Errorf("translator received query %q, want %q", gotQuery, "my open bugs in TEST")
	}
	// The translated JQL (not the raw query) must reach the JIRA search.
	if recorded != "project = TEST AND status = Open" {
		t.Errorf("JIRA search received JQL %q, want the translated JQL", recorded)
	}

	list := h.store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 stored result, got %d", len(list))
	}
	data, ok := list[0].Data.(*QueryResultData)
	if !ok {
		t.Fatalf("stored data type = %T, want *QueryResultData", list[0].Data)
	}
	if data.Route != "fast_path" {
		t.Errorf("stored route = %q, want fast_path", data.Route)
	}
	if list[0].Title != "Query: my open bugs in TEST" {
		t.Errorf("stored title = %q, want original query in title", list[0].Title)
	}
	if data.Query != "my open bugs in TEST" {
		t.Errorf("stored natural query = %q, want exact original input", data.Query)
	}
	if data.JQL != "project = TEST AND status = Open" {
		t.Errorf("stored JQL = %q, want the translated JQL", data.JQL)
	}
	if len(data.Issues) != 2 {
		t.Errorf("stored issues = %d, want 2", len(data.Issues))
	}
	if data.Search == nil || data.Search.Total == nil || *data.Search.Total != 2 || data.Search.TotalKind != jira.CountExact {
		t.Errorf("stored search metadata = %+v, want verified exact total 2", data.Search)
	}

	text := resultText(res)
	assertContains(t, text, "project = TEST AND status = Open")
	assertContains(t, text, "TEST-1")
	assertContains(t, text, "TEST-2")
	assertContains(t, text, "Exact matching total: 2")
	// Two issues are well under the default 50 cap, so no cap warning.
	if strings.Contains(text, "max_results cap") {
		t.Errorf("unexpected max_results cap warning:\n%s", text)
	}
}

func TestHandleQuery_RecommendationAddsIssueInspectionGuidance(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)
	var recorded string
	ts := jqlSearchServer(t, sampleJQLIssues("TEST-1", "TEST-2"), &recorded)
	t.Setenv("JIRA_SERVER", ts.URL)
	const query = "Which of my open jiras should I work on next?"
	h.translate = func(_ context.Context, _ *config.Config, got string) (*llm.QueryResult, error) {
		if got != query {
			t.Errorf("translator received %q, want original wording %q", got, query)
		}
		return &llm.QueryResult{JQL: "project = TEST AND status = Open", Route: "fast_path"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": query}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	text := resultText(res)
	assertContains(t, text, "inspect each candidate with jira_get_issue")
	assertContains(t, text, "use the issue content to make your recommendation")
	if strings.Contains(text, "shortlist using") {
		t.Errorf("bounded candidate result unexpectedly asks for a shortlist:\n%s", text)
	}
}

func TestIsRecommendationQuery(t *testing.T) {
	for _, query := range []string{
		"Which of my open jiras should I work on next?",
		"What should I work on today?",
		"Prioritise my open issues",
		"Recommend a Jira to tackle",
	} {
		if !isRecommendationQuery(query) {
			t.Errorf("isRecommendationQuery(%q) = false, want true", query)
		}
	}
	if isRecommendationQuery("Show my open issues") {
		t.Error("ordinary search was classified as a recommendation request")
	}
}

func TestHandleQuery_CountQuestionEnumeratesExactCount(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	var recorded string
	ts := jqlSearchServer(t, sampleJQLIssues("TEST-1", "TEST-2"), &recorded)
	t.Setenv("JIRA_SERVER", ts.URL)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST AND status = Open", Route: "fast_path"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "count open bugs",
		"max_results":   float64(2),
	}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	text := resultText(res)
	assertContains(t, text, "Exact matching total: 2")

	list := h.store.List()
	if len(list) != 1 {
		t.Fatalf("stored results = %d, want 1", len(list))
	}
	data, ok := list[0].Data.(*QueryResultData)
	if !ok {
		t.Fatalf("stored data type = %T, want *QueryResultData", list[0].Data)
	}
	if data.Search == nil || data.Search.Total == nil || *data.Search.Total != 2 || data.Search.TotalKind != jira.CountExact {
		t.Errorf("stored search metadata = %+v, want verified exact total 2", data.Search)
	}
	if got := len(data.Issues); got != 2 {
		t.Errorf("stored issue rows = %d, want 2", got)
	}
}

func TestHandleQuery_RejectsPretranslatedJQLAndRecordsFailure(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	translatorCalled := false
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		translatorCalled = true
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "assignee = currentUser() AND status = Closed ORDER BY updated DESC",
	}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	assertIsError(t, res)
	assertContains(t, resultText(res), "pass the user's original natural-language wording verbatim")
	if translatorCalled {
		t.Error("translator was called for pretranslated JQL")
	}
	if got := len(h.store.List()); got != 1 {
		t.Errorf("stored %d attempts after rejecting JQL input, want one failure", got)
	}
}

func TestLooksLikeJQL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`project = PROJ AND statusCategory != Done`, true},
		{`resolved >= startOfYear()`, true},
		{`due >= startOfWeek() AND due <= endOfWeek()`, true},
		{`show me issues with status updates`, false},
		{`what is the status of my project`, false},
		{`issues assigned to me`, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := looksLikeJQL(tt.input); got != tt.want {
				t.Errorf("looksLikeJQL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestHandleQuery_RejectsLegacyQueryArgumentBeforeTranslation(t *testing.T) {
	h := newTestHandlers(t)
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("JIRA_PROJECT", "")
	var calls int
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		calls++
		return nil, errors.New("translator must not be called")
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"query": "open bugs"}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	assertIsError(t, res)
	if calls != 0 {
		t.Errorf("translator called %d times for legacy-only arguments, want 0", calls)
	}
	if len(h.store.List()) != 1 {
		t.Error("expected one failed attempt for rejected legacy-only arguments")
	}
}

func TestHandleTranslateQueryUsesSameTranslationWithoutSearching(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	const original = "my open bugs in TEST"
	const translated = "project = TEST AND statusCategory != Done AND assignee = currentUser()"
	var searchedJQL string
	searchServer := jqlSearchServer(t, nil, &searchedJQL)
	t.Setenv("JIRA_SERVER", searchServer.URL)
	var translatedInputs []string
	h.translate = func(_ context.Context, cfg *config.Config, query string) (*llm.QueryResult, error) {
		translatedInputs = append(translatedInputs, query)
		if cfg.JiraProject != "TEST" {
			t.Errorf("translator default project = %q, want TEST", cfg.JiraProject)
		}
		return &llm.QueryResult{
			Input: original, NormalizedInput: original, Route: llm.RouteFastPath, JQL: translated,
		}, nil
	}

	translatedRes, err := h.HandleTranslateQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
	if err != nil {
		t.Fatalf("HandleTranslateQuery returned err: %v", err)
	}
	if translatedRes.IsError {
		t.Fatalf("unexpected translation error: %s", resultText(translatedRes))
	}
	var got struct {
		Input           string `json:"input"`
		NormalizedInput string `json:"normalized_input"`
		Route           string `json:"route"`
		JQL             string `json:"jql"`
		Validation      struct {
			Valid bool `json:"valid"`
		} `json:"validation"`
	}
	if err := json.Unmarshal([]byte(resultText(translatedRes)), &got); err != nil {
		t.Fatalf("decode translation result: %v\n%s", err, resultText(translatedRes))
	}
	if got.Input != original || got.NormalizedInput != original || got.Route != llm.RouteFastPath || got.JQL != translated {
		t.Errorf("translation diagnostics = %+v, want exact input/normalized input, fast_path, and generated JQL", got)
	}
	if !got.Validation.Valid {
		t.Errorf("generated JQL failed validation: %s", resultText(translatedRes))
	}
	if len(translatedInputs) != 1 || translatedInputs[0] != original {
		t.Errorf("translator inputs after translate-only call = %v, want one exact original input", translatedInputs)
	}
	if len(h.store.List()) != 0 {
		t.Errorf("translate-only call stored %d issue results, want none", len(h.store.List()))
	}
	if searchedJQL != "" {
		t.Errorf("translate-only call executed Jira search with %q", searchedJQL)
	}
	queryRes, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": original}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	if queryRes.IsError {
		t.Fatalf("unexpected query error: %s", resultText(queryRes))
	}
	if len(translatedInputs) != 2 || translatedInputs[1] != original {
		t.Errorf("translator inputs = %v, want same original wording for both tools", translatedInputs)
	}
	if searchedJQL != got.JQL {
		t.Errorf("jira_query searched %q, translate-only returned %q", searchedJQL, got.JQL)
	}
}

func TestHandleTranslateQueryRejectsBlankAndJQL(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	called := false
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		called = true
		return &llm.QueryResult{JQL: "project = TEST"}, nil
	}
	for _, input := range []string{" \n\t", "project = TEST AND status = Open"} {
		res, err := h.HandleTranslateQuery(context.Background(), makeRequest(map[string]any{"natural_query": input}))
		if err != nil {
			t.Fatalf("HandleTranslateQuery(%q) returned err: %v", input, err)
		}
		assertIsError(t, res)
	}
	if called {
		t.Error("translator was called for blank or pretranslated JQL")
	}
}

func TestHandleTranslateQueryReportsLocalValidationFailure(t *testing.T) {
	h := newTestHandlers(t)
	setTestFullEnv(t)
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "status = Open", Route: llm.RouteFastPath}, nil
	}
	res, err := h.HandleTranslateQuery(context.Background(), makeRequest(map[string]any{"natural_query": "open bugs"}))
	if err != nil {
		t.Fatalf("HandleTranslateQuery returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(res))
	}
	var got struct {
		Validation struct {
			Valid bool   `json:"valid"`
			Error string `json:"error"`
		} `json:"validation"`
	}
	if err := json.Unmarshal([]byte(resultText(res)), &got); err != nil {
		t.Fatalf("decode validation result: %v", err)
	}
	if got.Validation.Valid || !strings.Contains(got.Validation.Error, "project predicate") {
		t.Errorf("validation = %+v, want a local project-scope failure", got.Validation)
	}
}

// Reaching the display limit on a terminal page does not imply truncation.
func TestHandleQuery_TerminalAtDisplayLimitIsComplete(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	var recorded string
	ts := jqlSearchServer(t, sampleJQLIssues("TEST-1", "TEST-2", "TEST-3"), &recorded)
	t.Setenv("JIRA_SERVER", ts.URL)

	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: "project = TEST AND status = Open", Route: "fast_path"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{
		"natural_query": "open bugs",
		"max_results":   float64(3),
	}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	assertContains(t, resultText(res), "Completeness: complete")
	if strings.Contains(resultText(res), "cap") || strings.Contains(resultText(res), "more matches") {
		t.Fatalf("terminal search incorrectly claimed truncation: %s", resultText(res))
	}
}

// The digest NL path routes its issues argument through digestTranslate and feeds
// the returned JQL into the JIRA search.
func TestHandleDigest_NLTranslationDrivesSearch(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	var recorded string
	ts := jqlSearchServer(t, nil, &recorded) // no matching issues
	t.Setenv("JIRA_SERVER", ts.URL)

	var gotQuery string
	h.digestTranslate = func(_ context.Context, _ *config.Config, query string) (*llm.QueryResult, error) {
		gotQuery = query
		return &llm.QueryResult{JQL: "project = TEST AND status = Open", Route: "fast_path", Days: 5}, nil
	}

	res, err := h.HandleDigest(context.Background(), makeRequest(map[string]any{
		"issues": "open bugs in TEST in the last 5 days",
	}))
	if err != nil {
		t.Fatalf("HandleDigest returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}

	if gotQuery != "open bugs in TEST in the last 5 days" {
		t.Errorf("digest translator received query %q, want %q", gotQuery, "open bugs in TEST in the last 5 days")
	}
	// The translated JQL (not the raw arg) must reach the JIRA search.
	if recorded != "project = TEST AND status = Open" {
		t.Errorf("JIRA search received JQL %q, want the translated JQL", recorded)
	}
	assertContains(t, resultText(res), "No issues found.")
}

// A digest translator error must surface as an error result.
func TestHandleDigest_TranslationErrorSurfaces(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	h.digestTranslate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return nil, errors.New("translation boom")
	}

	res, err := h.HandleDigest(context.Background(), makeRequest(map[string]any{
		"issues": "critical bugs in TEST",
	}))
	if err != nil {
		t.Fatalf("HandleDigest returned err: %v", err)
	}
	assertIsError(t, res)
	assertContains(t, resultText(res), "JQL generation failed")
}

// A 400 from the JIRA search endpoint (a malformed query) must surface as an
// error result, not an empty success, and Jira's own diagnostic text — plus
// the offending JQL — must survive into the message (FIELDCTO-2814).
func TestHandleQuery_SearchErrorSurfacesJiraMessageAndJQL(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errorMessages": []string{"Error in the JQL Query: Expecting ',' but got 'AND'. (line 1, character 39)"},
		})
	}))
	t.Cleanup(ts.Close)
	t.Setenv("JIRA_SERVER", ts.URL)

	const badJQL = "project = TEST AND status = Open"
	h.translate = func(_ context.Context, _ *config.Config, _ string) (*llm.QueryResult, error) {
		return &llm.QueryResult{JQL: badJQL, Route: "fast_path"}, nil
	}

	res, err := h.HandleQuery(context.Background(), makeRequest(map[string]any{"natural_query": "broken query"}))
	if err != nil {
		t.Fatalf("HandleQuery returned err: %v", err)
	}
	assertIsError(t, res)
	text := resultText(res)
	assertContains(t, text, "Expecting ',' but got 'AND'")
	assertContains(t, text, badJQL)
}

// --- projectList fetch+cache hot path ---
//
// projectList is what NewHandlers wires into the query/digest translation seams:
// it warms the tenant's project list into the cache on a cold cache and serves
// it from there while it is fresh, so the deterministic translation hot path is
// network-free after the first call.

// TestProjectList_CachesFetch verifies the cold-then-warm behaviour: the first
// call hits JIRA and the second is served from cache, so the project endpoint is
// called exactly once across both calls.
func TestProjectList_CachesFetch(t *testing.T) {
	h := newTestHandlers(t)
	setTestJIRAEnv(t)

	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/project" {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"key": "SUP", "name": "Support", "projectTypeKey": "serviceDesks"},
			{"key": "ENG", "name": "Engineering", "projectTypeKey": "software"},
		})
	}))
	t.Cleanup(ts.Close)

	cfg := &config.Config{
		JiraServer:   ts.URL,
		JiraEmail:    "test@test.com",
		JiraAPIToken: "fake",
		JiraProject:  "SUP",
	}

	first := projectList(h.cache, cfg)
	if len(first) != 2 {
		t.Fatalf("cold fetch returned %d projects, want 2", len(first))
	}
	second := projectList(h.cache, cfg)
	if len(second) != 2 {
		t.Fatalf("warm fetch returned %d projects, want 2", len(second))
	}
	if calls != 1 {
		t.Errorf("project endpoint called %d times, want 1 (the second call must be a cache hit)", calls)
	}
}

// TestProjectList_NilCache verifies a nil cache degrades to an empty project
// list (the translator falls back to its env-only vocabulary) without panicking.
func TestProjectList_NilCache(t *testing.T) {
	if got := projectList(nil, &config.Config{JiraProject: "SUP"}); got != nil {
		t.Errorf("projectList(nil, _) = %v, want nil", got)
	}
}
