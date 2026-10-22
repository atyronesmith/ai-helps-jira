package jira

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

type connectionRoundTripper func(*http.Request) (*http.Response, error)

func (f connectionRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newConnectionClient(base string, rt http.RoundTripper) *Client {
	return &Client{http: &http.Client{Transport: rt}, base: base, email: "user@example.test", token: "secret-token"}
}

func TestCheckConnectionSuccessAndRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/myself" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "user@example.test" || pass != "secret-token" {
			t.Errorf("unexpected basic auth")
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"accountId":"private-account","accountType":"atlassian","active":true,"displayName":"private name"}`)
	}))
	defer server.Close()

	result := newConnectionClient(server.URL+"///", http.DefaultTransport).CheckConnection(context.Background())
	if !result.OK || !result.Reachable || !result.Authenticated || result.State != "ok" || result.HTTPStatus != 200 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.CheckedAt.Location() != time.UTC || result.LatencyMS < 0 || result.Message != "Jira connection and authentication verified. Project permissions were not tested." {
		t.Fatalf("bad metadata: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	for _, private := range []string{"private-account", "private name", "user@example.test", "secret-token"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("result exposed %q: %s", private, encoded)
		}
	}
}

func TestCheckConnectionStatusClassification(t *testing.T) {
	cases := []struct {
		status int
		state  string
	}{
		{http.StatusUnauthorized, "authentication"},
		{http.StatusForbidden, "forbidden"},
		{http.StatusNotFound, "not_found"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusMovedPermanently, "redirect"},
		{http.StatusBadGateway, "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			client := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret upstream body")), Request: r}, nil
			}))
			got := client.CheckConnection(context.Background())
			if got.State != tc.state || !got.Reachable || got.HTTPStatus != tc.status || got.Authenticated || got.OK {
				t.Fatalf("unexpected result: %+v", got)
			}
			if strings.Contains(got.Message, "secret") {
				t.Fatalf("upstream body leaked: %+v", got)
			}
		})
	}
}

func TestCheckConnectionInvalidResponses(t *testing.T) {
	cases := []struct {
		name, contentType, body string
	}{
		{"html", "text/html", `{"accountId":"x"}`},
		{"malformed", "application/json", `{`},
		{"empty", "application/json", ``},
		{"unknown_account", "application/json", `{"accountId":"unknown"}`},
		{"missing_account", "application/json", `{"accountType":"atlassian"}`},
		{"anonymous", "application/json", `{"accountId":"x","accountType":"anonymous"}`},
		{"inactive", "application/json", `{"accountId":"x","active":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(r *http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("Content-Type", tc.contentType)
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			}))
			got := client.CheckConnection(context.Background())
			if got.State != "invalid_response" || !got.Reachable || got.HTTPStatus != 200 || got.Authenticated {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func TestCheckConnectionBodyLimitAndReadError(t *testing.T) {
	for name, body := range map[string]io.ReadCloser{
		"oversize":   io.NopCloser(strings.NewReader(`{"accountId":"x","padding":"` + strings.Repeat("a", connectionBodyLimit) + `"}`)),
		"read_error": failingConnectionBody{},
	} {
		t.Run(name, func(t *testing.T) {
			client := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(r *http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("Content-Type", "application/json")
				return &http.Response{StatusCode: 200, Header: h, Body: body, Request: r}, nil
			}))
			if got := client.CheckConnection(context.Background()); got.State != "invalid_response" {
				t.Fatalf("state = %q", got.State)
			}
		})
	}
}

type failingConnectionBody struct{}

func (failingConnectionBody) Read([]byte) (int, error) { return 0, errors.New("private read failure") }
func (failingConnectionBody) Close() error             { return nil }

func TestCheckConnectionDoesNotFollowRedirectOrForwardCredentials(t *testing.T) {
	var secondRequests int
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondRequests++ }))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/capture", http.StatusFound)
	}))
	defer first.Close()

	got := newConnectionClient(first.URL, http.DefaultTransport).CheckConnection(context.Background())
	if got.State != "redirect" || got.HTTPStatus != http.StatusFound || secondRequests != 0 {
		t.Fatalf("result = %+v, second requests = %d", got, secondRequests)
	}
}

type connectionTimeoutError struct{}

func (connectionTimeoutError) Error() string   { return "private timeout details" }
func (connectionTimeoutError) Timeout() bool   { return true }
func (connectionTimeoutError) Temporary() bool { return true }

type blockingConnectionBody struct{ ctx context.Context }

func (b blockingConnectionBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (blockingConnectionBody) Close() error               { return nil }

func TestCheckConnectionTimeoutAndJarIsolation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"general_timeout", connectionTimeoutError{}},
		{"dns_timeout", &net.DNSError{Err: "timeout", Name: "private-host", IsTimeout: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(*http.Request) (*http.Response, error) { return nil, test.err }))
			if got := client.CheckConnection(context.Background()); got.State != "timeout" {
				t.Fatalf("result = %+v", got)
			}
		})
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	client := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("request made after deadline"); return nil, nil }))
	if got := client.CheckConnection(ctx); got.State != "timeout" {
		t.Fatalf("expired caller deadline result = %+v", got)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client = newConnectionClient("https://jira.example.test", connectionRoundTripper(func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: 200, Header: h, Body: blockingConnectionBody{ctx: r.Context()}, Request: r}, nil
	}))
	if got := client.CheckConnection(ctx); got.State != "timeout" || !got.Reachable || got.HTTPStatus != 200 {
		t.Fatalf("body timeout result = %+v", got)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cookieClient := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("Cookie header forwarded: %q", got)
		}
		return nil, errors.New("private transport error")
	}))
	cookieClient.http.Jar = jar
	probeURL, _ := url.Parse("https://jira.example.test/rest/api/3/myself")
	jar.SetCookies(probeURL, []*http.Cookie{{Name: "session", Value: "private-cookie"}})
	if got := cookieClient.CheckConnection(context.Background()); got.State != "connectivity" {
		t.Fatalf("jar isolation result = %+v", got)
	}
}

func TestCheckConnectionConfigurationValidation(t *testing.T) {
	for _, base := range []string{"", "ftp://jira.example.test", "https:///path", "https://user:pass@jira.example.test", "https://jira.example.test?secret=x", "https://jira.example.test/#fragment", "https://jira.example.test?"} {
		t.Run(fmt.Sprintf("base_%d", len(base)), func(t *testing.T) {
			got := newConnectionClient(base, http.DefaultTransport).CheckConnection(context.Background())
			if got.State != "configuration" || got.Reachable || got.HTTPStatus != 0 {
				t.Fatalf("result = %+v", got)
			}
		})
	}
	client := newConnectionClient("https://jira.example.test", http.DefaultTransport)
	client.token = " "
	if got := client.CheckConnection(context.Background()); got.State != "configuration" || got.Reachable {
		t.Fatalf("missing token result = %+v", got)
	}
	got := CheckConnection(context.Background(), nil)
	if got.State != "configuration" || got.CheckedAt.IsZero() {
		t.Fatalf("nil config result = %+v", got)
	}
}

func TestCheckConnectionFailureClassification(t *testing.T) {
	dnsErr := &net.DNSError{Err: "private-dns-name", Name: "private-host"}
	certErr := &tls.CertificateVerificationError{UnverifiedCertificates: []*x509.Certificate{{}}, Err: errors.New("private certificate detail")}
	cases := []struct {
		name string
		err  error
		ctx  func() (context.Context, context.CancelFunc)
		want string
	}{
		{"dns", dnsErr, func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, "dns"},
		{"tls", certErr, func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, "tls"},
		{"network", errors.New("private network failure"), func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, "connectivity"},
		{"deadline", context.DeadlineExceeded, func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			client := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(*http.Request) (*http.Response, error) { return nil, tc.err }))
			got := client.CheckConnection(ctx)
			if got.State != tc.want || got.Reachable || got.HTTPStatus != 0 {
				t.Fatalf("result = %+v", got)
			}
			for _, secret := range []string{"private-dns-name", "private-host", "private certificate detail", "private network failure", "JIRA_BASE_URL"} {
				if strings.Contains(got.Message, secret) {
					t.Fatalf("error detail leaked: %q", got.Message)
				}
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := newConnectionClient("https://jira.example.test", connectionRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("request made after cancellation")
		return nil, nil
	})).CheckConnection(ctx)
	if got.State != "cancelled" || got.Reachable {
		t.Fatalf("cancelled result = %+v", got)
	}

	cfg := &config.Config{JiraServer: "https://jira.example.test", JiraEmail: "user@example.test", JiraAPIToken: "secret-token"}
	got = CheckConnection(context.Background(), cfg)
	if got.State != "dns" && got.State != "connectivity" {
		t.Fatalf("unreachable config result = %+v", got)
	}
}
