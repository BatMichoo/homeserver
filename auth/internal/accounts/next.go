package accounts

import (
	"net/url"
	"strings"
)

// SafeNext is where to send someone after logging in: next if it's an
// https address on lanDomain (or a subdomain), or a path on this service;
// anything else is /account. It keeps login from bouncing people to other
// sites.
func SafeNext(next, lanDomain string) string {
	const fallback = "/account"
	if strings.HasPrefix(next, "/") {
		if strings.HasPrefix(next, "//") || strings.HasPrefix(next, `/\`) {
			return fallback
		}
		if _, err := url.ParseRequestURI(next); err != nil {
			return fallback
		}
		return next
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return fallback
	}
	host := strings.ToLower(u.Hostname())
	d := strings.ToLower(lanDomain)
	if d == "" || (host != d && !strings.HasSuffix(host, "."+d)) {
		return fallback
	}
	return next
}
