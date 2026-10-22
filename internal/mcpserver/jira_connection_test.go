package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/jira"
)

func newConnectionTestServer(t *testing.T) *WebServer {
	t.Helper()
	ws := NewWebServer(&ResultStore{results: make(map[string]*StoredResult)}, nil, 0, "127.0.0.1")
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func connectionRequest(method, target string, origin string, header string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if header != "" {
		r.Header.Set("X-Jira-Connection-Test", header)
	}
	return r
}

func TestJiraConnectionRequestValidationDoesNotProbe(t *testing.T) {
	ws := newConnectionTestServer(t)
	var calls atomic.Int32
	ws.checkJiraConnection = func(context.Context) jira.ConnectionCheck {
		calls.Add(1)
		return jira.ConnectionCheck{OK: true, State: "ok"}
	}
	tests := []struct {
		name, method, target, origin, marker string
		want                                 int
	}{
		{"method", http.MethodGet, "/api/jira/test-connection", "", "1", http.StatusMethodNotAllowed},
		{"missing header", http.MethodPost, "/api/jira/test-connection", "", "", http.StatusForbidden},
		{"wrong header", http.MethodPost, "/api/jira/test-connection", "", "true", http.StatusForbidden},
		{"null origin", http.MethodPost, "/api/jira/test-connection", "null", "1", http.StatusForbidden},
		{"wrong scheme", http.MethodPost, "/api/jira/test-connection", "ftp://example.test", "1", http.StatusForbidden},
		{"origin userinfo", http.MethodPost, "/api/jira/test-connection", "https://user@dashboard.test", "1", http.StatusForbidden},
		{"origin path", http.MethodPost, "/api/jira/test-connection", "https://dashboard.test/path", "1", http.StatusForbidden},
		{"origin query", http.MethodPost, "/api/jira/test-connection", "https://dashboard.test/?x=1", "1", http.StatusForbidden},
		{"origin fragment", http.MethodPost, "/api/jira/test-connection", "https://dashboard.test/#x", "1", http.StatusForbidden},
		{"origin host", http.MethodPost, "/api/jira/test-connection", "https://other.test", "1", http.StatusForbidden},
		{"query", http.MethodPost, "/api/jira/test-connection?token=secret", "", "1", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := connectionRequest(tc.method, tc.target, tc.origin, tc.marker)
			if tc.name == "method" {
				r.Host = "dashboard.test"
			}
			w := httptest.NewRecorder()
			ws.handleJiraConnection(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("security headers missing: %v", w.Header())
			}
			if tc.name == "method" && w.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow = %q", w.Header().Get("Allow"))
			}
		})
	}
	for _, tc := range []struct{ name, origin string }{{"empty origin", ""}, {"matching host, scheme case", "HTTPS://DASHBOARD.TEST"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := connectionRequest(http.MethodPost, "/api/jira/test-connection", tc.origin, "1")
			r.Host = "dashboard.test"
			// A matching-origin request is permitted to invoke the probe.
			ws.checkJiraConnection = func(context.Context) jira.ConnectionCheck { return jira.ConnectionCheck{OK: true, State: "ok"} }
			w := httptest.NewRecorder()
			ws.handleJiraConnection(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid requests triggered %d probes", got)
	}
}

func TestJiraConnectionContentLengthAndResponse(t *testing.T) {
	ws := newConnectionTestServer(t)
	var calls atomic.Int32
	ws.checkJiraConnection = func(context.Context) jira.ConnectionCheck {
		calls.Add(1)
		return jira.ConnectionCheck{OK: true, State: "ok", Message: "connected"}
	}
	for _, body := range []string{"{}", " ", "token=secret"} {
		r := connectionRequest(http.MethodPost, "/api/jira/test-connection", "", "1")
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		w := httptest.NewRecorder()
		ws.handleJiraConnection(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d", body, w.Code)
		}
	}
	r := connectionRequest(http.MethodPost, "/api/jira/test-connection", "", "1")
	w := httptest.NewRecorder()
	ws.handleJiraConnection(w, r)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ok", "state", "reachable", "authenticated", "message", "checked_at", "latency_ms"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing JSON field %q: %v", key, got)
		}
	}
	if _, ok := got["http_status"]; ok {
		t.Errorf("http_status should be omitted when zero: %v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want one", calls.Load())
	}
	var checked time.Time
	if err := checked.UnmarshalText([]byte(got["checked_at"].(string))); err != nil {
		t.Fatal(err)
	}
	if checked.Location() != time.UTC || got["latency_ms"].(float64) < 0 {
		t.Fatalf("bad time or latency: %v", got)
	}
}

func TestJiraConnectionFailureStatusesAndPassiveEndpoints(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  int
	}{{"configuration", 400}, {"timeout", 504}, {"cancelled", 408}, {"upstream", 502}} {
		t.Run(tc.state, func(t *testing.T) {
			ws := newConnectionTestServer(t)
			var calls atomic.Int32
			ws.checkJiraConnection = func(context.Context) jira.ConnectionCheck {
				calls.Add(1)
				return jira.ConnectionCheck{State: tc.state, Message: "safe failure"}
			}
			r := connectionRequest(http.MethodPost, "/api/jira/test-connection", "", "1")
			w := httptest.NewRecorder()
			ws.handleJiraConnection(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			for _, request := range []*http.Request{httptest.NewRequest("GET", "/healthz", nil), httptest.NewRequest("GET", "/readyz", nil), httptest.NewRequest("GET", "/api/status", nil), httptest.NewRequest("GET", "/", nil)} {
				pw := httptest.NewRecorder()
				switch request.URL.Path {
				case "/healthz":
					ws.handleHealthz(pw, request)
				case "/readyz":
					ws.handleReadyz(pw, request)
				case "/api/status":
					ws.handleStatus(pw, request)
				default:
					ws.handleIndex(pw, request)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("passive endpoint invoked probe; calls=%d", calls.Load())
			}
		})
	}
}

func TestJiraConnectionSuccessDoesNotClaimFullReadiness(t *testing.T) {
	ws := newConnectionTestServer(t)
	ws.checkJiraConnection = func(context.Context) jira.ConnectionCheck {
		return jira.ConnectionCheck{OK: true, State: "ok", Reachable: true, Authenticated: true}
	}
	w := httptest.NewRecorder()
	ws.handleJiraConnection(w, connectionRequest(http.MethodPost, "/api/jira/test-connection", "", "1"))
	if w.Code != http.StatusOK {
		t.Fatalf("connection status = %d", w.Code)
	}
	if got := ws.status().Readiness; got == "ready" {
		t.Fatal("a successful Jira probe incorrectly marked all dependencies ready")
	}
	w = httptest.NewRecorder()
	ws.handleReadyz(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503 while other dependencies remain unknown", w.Code)
	}
}

func TestJiraConnectionProbeConcurrency(t *testing.T) {
	ws := newConnectionTestServer(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	ws.checkJiraConnection = func(ctx context.Context) jira.ConnectionCheck {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return jira.ConnectionCheck{OK: true, State: "ok"}
		case <-ctx.Done():
			return jira.ConnectionCheck{State: "cancelled"}
		}
	}
	first := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		ws.handleJiraConnection(w, connectionRequest("POST", "/api/jira/test-connection", "", "1"))
		first <- w.Code
	}()
	<-entered
	w := httptest.NewRecorder()
	ws.handleJiraConnection(w, connectionRequest("POST", "/api/jira/test-connection", "", "1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("second status=%d, want 409", w.Code)
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first status=%d", code)
	}
	ws.checkJiraConnection = func(context.Context) jira.ConnectionCheck {
		calls.Add(1)
		return jira.ConnectionCheck{OK: true, State: "ok"}
	}
	w = httptest.NewRecorder()
	ws.handleJiraConnection(w, connectionRequest("POST", "/api/jira/test-connection", "", "1"))
	if w.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("third status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestJiraConnectionProbePropagatesCancellation(t *testing.T) {
	ws := newConnectionTestServer(t)
	started := make(chan struct{})
	ws.checkJiraConnection = func(ctx context.Context) jira.ConnectionCheck {
		close(started)
		<-ctx.Done()
		return jira.ConnectionCheck{State: "cancelled", Message: "Connection check was cancelled."}
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := connectionRequest(http.MethodPost, "/api/jira/test-connection", "", "1").WithContext(ctx)
	finished := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		ws.handleJiraConnection(w, request)
		finished <- w.Code
	}()
	<-started
	cancel()
	if code := <-finished; code != http.StatusRequestTimeout {
		t.Fatalf("cancelled request status = %d, want 408", code)
	}
}

func TestJiraConnectionMissingConfigurationIsSanitized(t *testing.T) {
	t.Setenv("JIRA_SERVER", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "do-not-echo-this")
	ws := newConnectionTestServer(t)
	w := httptest.NewRecorder()
	ws.handleJiraConnection(w, connectionRequest(http.MethodPost, "/api/jira/test-connection", "", "1"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if strings.Contains(w.Body.String(), "do-not-echo-this") || strings.Contains(w.Body.String(), "JIRA_SERVER=") {
		t.Fatalf("configuration output contains sensitive details: %s", w.Body.String())
	}
}

func TestJiraConnectionRouteAndConfiguredProbe(t *testing.T) {
	upstreamToken := "never-return-this-token"
	marker := "upstream-private-error-marker"
	var acceptCredentials atomic.Bool
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" || r.Method != "GET" {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "dashboard@example.test" || pass != upstreamToken {
			t.Errorf("bad upstream auth")
		}
		if !acceptCredentials.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, marker)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accountId":"private-account-marker","displayName":"private-display-marker","active":true}`)
	})}
	go func() { _ = upstream.Serve(listener) }()
	defer upstream.Close()
	t.Setenv("JIRA_SERVER", "http://"+listener.Addr().String())
	t.Setenv("JIRA_EMAIL", "dashboard@example.test")
	t.Setenv("JIRA_API_TOKEN", upstreamToken)
	t.Setenv("JIRA_PROJECT", "")
	t.Setenv("LLM_PROVIDER", "")
	t.Setenv("LLM_API_KEY", "")
	ws := newConnectionTestServer(t)
	port, err := ws.Start()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	resp, err := http.Get(base + "/api/jira/test-connection")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET route status=%d", resp.StatusCode)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/jira/test-connection", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Jira-Connection-Test", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var result jira.ConnectionCheck
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || result.Authenticated || result.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	if strings.Contains(string(body), upstreamToken) || strings.Contains(string(body), marker) || strings.Contains(string(body), listener.Addr().String()) {
		t.Fatalf("sensitive upstream data leaked: %s", body)
	}
	acceptCredentials.Store(true)
	req, err = http.NewRequest(http.MethodPost, base+"/api/jira/test-connection", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Jira-Connection-Test", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK || !result.Authenticated || !result.Reachable || result.State != "ok" || result.HTTPStatus != http.StatusOK {
		t.Fatalf("successful current-user response did not verify connection: %+v", result)
	}
	for _, private := range []string{upstreamToken, "private-account-marker", "private-display-marker", listener.Addr().String()} {
		if strings.Contains(string(body), private) {
			t.Fatal("successful check exposed private upstream fields")
		}
	}
}
