package mcpserver

import (
	"net/http"
	"net/url"
	"strings"
)

// dashboardSameOrigin supplements each manual action's required custom header.
// Non-browser diagnostics may omit Origin. Cross-origin browser requests need a
// preflight for the custom header, which this server does not authorize.
func dashboardSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && strings.EqualFold(u.Host, r.Host)
}
