package jira

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/config"
)

const (
	connectionTimeout   = 10 * time.Second
	connectionBodyLimit = 1 << 20
)

// ConnectionCheck is a privacy-preserving result from a bounded Jira
// connectivity and authentication check.
type ConnectionCheck struct {
	OK            bool      `json:"ok"`
	State         string    `json:"state"`
	Reachable     bool      `json:"reachable"`
	Authenticated bool      `json:"authenticated"`
	Message       string    `json:"message"`
	HTTPStatus    int       `json:"http_status,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
	LatencyMS     int64     `json:"latency_ms"`
}

// CheckConnection constructs the minimal client needed for a connection probe.
// It intentionally avoids NewClient, which logs the configured Jira server.
func CheckConnection(ctx context.Context, cfg *config.Config) ConnectionCheck {
	if cfg == nil {
		return connectionConfigurationFailure("Jira configuration is not available.")
	}
	return (&Client{
		http:  &http.Client{Timeout: connectionTimeout},
		base:  cfg.JiraServer,
		email: cfg.JiraEmail,
		token: cfg.JiraAPIToken,
	}).CheckConnection(ctx)
}

// CheckConnection checks Jira's current-user endpoint using only this client's
// configured base URL and credentials. It does not expose response data or
// upstream error details.
func (c *Client) CheckConnection(ctx context.Context) ConnectionCheck {
	started := time.Now()
	result := ConnectionCheck{State: "configuration"}
	finish := func() ConnectionCheck {
		result.CheckedAt = time.Now().UTC()
		result.LatencyMS = time.Since(started).Milliseconds()
		if result.LatencyMS < 0 {
			result.LatencyMS = 0
		}
		return result
	}
	fail := func(state, message string) ConnectionCheck {
		result.State = state
		result.Message = message
		return finish()
	}

	base, err := url.Parse(strings.TrimSpace(c.base))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return fail("configuration", "Configure JIRA_SERVER as an http or https URL without credentials, query, or fragment.")
	}
	if strings.TrimSpace(c.email) == "" || strings.TrimSpace(c.token) == "" {
		return fail("configuration", "Configure JIRA_EMAIL and JIRA_API_TOKEN before checking the connection.")
	}
	if c.http == nil {
		return fail("configuration", "Jira HTTP client is not configured.")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	baseURL := strings.TrimRight(base.String(), "/")

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fail("timeout", "Jira connection check timed out. Check network connectivity and try again.")
		}
		return fail("cancelled", "Connection check was cancelled.")
	}
	checkCtx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, baseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return fail("configuration", "Jira connection URL is invalid.")
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")

	client := *c.http
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		state, message := classifyConnectionError(err, ctx, checkCtx)
		return fail(state, message)
	}
	defer resp.Body.Close()
	result.Reachable = true
	result.HTTPStatus = resp.StatusCode

	switch resp.StatusCode {
	case http.StatusOK:
		// Continue below and validate a minimal identity response.
	case http.StatusUnauthorized:
		return fail("authentication", "Jira rejected the configured credentials. Check JIRA_EMAIL and JIRA_API_TOKEN.")
	case http.StatusForbidden:
		return fail("forbidden", "Jira denied access to the current-user endpoint. Check the account's Jira access and token scopes.")
	case http.StatusNotFound:
		return fail("not_found", "Jira's current-user endpoint was not found. Check JIRA_SERVER.")
	case http.StatusTooManyRequests:
		return fail("rate_limited", "Jira rate limited the connection check. Try again later.")
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return fail("redirect", "Jira redirected the connection check. Check JIRA_SERVER and proxy settings.")
		}
		return fail("upstream_error", "Jira returned an unexpected response. Check Jira availability and proxy settings.")
	}

	mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		return fail("invalid_response", "Jira returned an invalid current-user response.")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, connectionBodyLimit+1))
	if err != nil {
		if state, message, ok := classifyConnectionInterruption(err, ctx, checkCtx); ok {
			return fail(state, message)
		}
		return fail("invalid_response", "Jira's current-user response could not be read.")
	}
	if len(body) > connectionBodyLimit {
		return fail("invalid_response", "Jira's current-user response exceeded the allowed size.")
	}
	var profile struct {
		AccountID   string `json:"accountId"`
		AccountType string `json:"accountType"`
		Active      *bool  `json:"active"`
	}
	trimmedBody := strings.TrimSpace(string(body))
	if err := json.Unmarshal(body, &profile); err != nil || len(trimmedBody) == 0 || trimmedBody[0] != '{' {
		return fail("invalid_response", "Jira returned an invalid current-user response.")
	}
	if strings.TrimSpace(profile.AccountID) == "" || strings.EqualFold(strings.TrimSpace(profile.AccountID), "unknown") || strings.EqualFold(strings.TrimSpace(profile.AccountType), "anonymous") || (profile.Active != nil && !*profile.Active) {
		return fail("invalid_response", "Jira's current-user response did not confirm an active authenticated account.")
	}
	result.OK = true
	result.Authenticated = true
	result.State = "ok"
	result.Message = "Jira connection and authentication verified. Project permissions were not tested."
	return finish()
}

func connectionConfigurationFailure(message string) ConnectionCheck {
	now := time.Now().UTC()
	return ConnectionCheck{State: "configuration", Message: message, CheckedAt: now, LatencyMS: 0}
}

func classifyConnectionError(err error, callerCtx, checkCtx context.Context) (string, string) {
	if state, message, ok := classifyConnectionInterruption(err, callerCtx, checkCtx); ok {
		return state, message
	}
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	switch {
	case errors.As(err, &dnsErr):
		return "dns", "Could not resolve the Jira host. Check JIRA_SERVER and DNS."
	case errors.As(err, &certErr), errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalidCert):
		return "tls", "Jira's TLS certificate could not be verified. Check the host certificate and system trust settings."
	default:
		return "connectivity", "Could not reach Jira. Check the network and JIRA_SERVER."
	}
}

func classifyConnectionInterruption(err error, callerCtx, checkCtx context.Context) (string, string, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(callerCtx.Err(), context.DeadlineExceeded), errors.Is(checkCtx.Err(), context.DeadlineExceeded):
		return "timeout", "Jira connection check timed out. Check network connectivity and try again.", true
	case errors.Is(err, context.Canceled), errors.Is(callerCtx.Err(), context.Canceled), errors.Is(checkCtx.Err(), context.Canceled):
		return "cancelled", "Connection check was cancelled.", true
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return "timeout", "Jira connection check timed out. Check network connectivity and try again.", true
	}
	return "", "", false
}
