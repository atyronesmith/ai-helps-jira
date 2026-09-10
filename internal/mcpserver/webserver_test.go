package mcpserver

import (
	"strings"
	"testing"
)

// TestNewWebServerParsesTemplates guards that every view template -- including
// query.html's route badge/tooltip -- parses against the template funcMap, so a
// template or funcMap drift (e.g. an unregistered routeTooltip call) cannot panic
// the dashboard at startup.
func TestNewWebServerParsesTemplates(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	ws := NewWebServer(store, 0, "127.0.0.1")
	if ws.tmpls["query.html"] == nil {
		t.Fatal("query.html template not parsed")
	}
	if len(ws.tmpls) == 0 {
		t.Fatal("no templates parsed")
	}
}

// TestIndexRendersQueryJQL verifies the results index surfaces the natural
// language query (in the row title) and the generated JQL for query results,
// so a viewer can see both without opening the detail view.
func TestIndexRendersQueryJQL(t *testing.T) {
	store := &ResultStore{results: make(map[string]*StoredResult)}
	store.Save(ResultQuery, "Query: my open bugs", &QueryResultData{
		Query: "my open bugs",
		Route: "fast_path",
		JQL:   "project = TEST AND status = Open",
	})

	ws := NewWebServer(store, 0, "127.0.0.1")
	if ws.tmpls["index.html"] == nil {
		t.Fatal("index.html template not parsed")
	}

	var buf strings.Builder
	data := struct {
		Results []*StoredResult
	}{Results: store.List()}
	if err := ws.tmpls["index.html"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("index render: %v", err)
	}

	html := buf.String()
	for _, want := range []string{"my open bugs", "project = TEST AND status = Open", "fast_path"} {
		if !strings.Contains(html, want) {
			t.Errorf("index HTML missing %q", want)
		}
	}
}
