package mcpserver

import "testing"

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
