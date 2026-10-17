package mcpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atyronesmith/ai-helps-jira/internal/web"
)

// The browser refreshes only the dashboard's local history projection. A second
// store handle models another MCP call/process adding or removing results.
func TestDashboardRefreshReadsCurrentSharedHistory(t *testing.T) {
	dir := t.TempDir()
	dashboardStore := openTestStore(t, dir, "dashboard", 0, 0)
	writer := openTestStore(t, dir, "dashboard", 0, 0)
	ws := NewWebServer(dashboardStore, nil, 0, "127.0.0.1")
	render := func() string {
		t.Helper()
		response := httptest.NewRecorder()
		ws.handleIndex(response, httptest.NewRequest("GET", "/", nil))
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("dashboard response=%d headers=%v", response.Code, response.Header())
		}
		body := response.Body.String()
		if !strings.Contains(body, `id="dashboard-results"`) {
			t.Fatal("missing refresh region")
		}
		return body
	}
	if body := render(); !strings.Contains(body, "No results yet") {
		t.Fatal("initial empty history not rendered")
	}
	id := writer.Save(ResultQuery, "fresh history", &QueryResultData{Query: `new result <script>alert(1)</script>`, Outcome: "failure"})
	body := render()
	if !strings.Contains(body, "new result &lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(body, id) || strings.Contains(body, "No results yet") {
		t.Fatal("refresh did not expose latest safely rendered history")
	}
	if strings.Contains(body, "new result <script>") {
		t.Fatal("refresh exposed unsafe query markup")
	}
	if !writer.Delete(id) {
		t.Fatal("delete shared history")
	}
	body = render()
	if strings.Contains(body, id) || !strings.Contains(body, "No results yet") {
		t.Fatal("refresh did not reconcile deletion to empty history")
	}
}

func TestDashboardRefreshScriptIsEmbedded(t *testing.T) {
	source, err := web.StaticFS.ReadFile("static/js/dashboard.js")
	if err != nil || len(source) == 0 {
		t.Fatalf("dashboard refresh asset not embedded: %v", err)
	}
}
