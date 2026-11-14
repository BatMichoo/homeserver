package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// verify is nginx's auth_request: 200 with the user in headers, or 401.
// X-User-Name is percent-encoded UTF-8 (headers are ASCII-only); apps
// decode it with url.QueryUnescape.
func (h *web) verify(c *gin.Context) {
	u, _ := h.session(c)
	if u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Header("X-User-Id", u.ID)
	c.Header("X-User-Name", url.QueryEscape(u.Name()))
	c.Status(http.StatusOK)
}

// redirectToLogin is where nginx sends a request to a protected app that
// has no session. A page load goes to the login page (302); an htmx
// request gets 401 with HX-Redirect, so htmx loads the login page as a
// whole page, returning to the page the person was on.
func (h *web) redirectToLogin(c *gin.Context) {
	if c.GetHeader("HX-Request") == "true" {
		back := c.GetHeader("HX-Current-URL")
		if back == "" {
			back = c.GetHeader("X-Original-URL")
		}
		c.Header("HX-Redirect", h.loginURL(back))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Redirect(http.StatusFound, h.loginURL(c.GetHeader("X-Original-URL")))
}

func (h *web) loginURL(next string) string {
	if next == "" {
		return h.publicURL + "/login"
	}
	return h.publicURL + "/login?next=" + url.QueryEscape(next)
}

// OriginCheck refuses a POST unless it comes from a page on lanDomain or
// one of its subdomains (by Origin, else Referer). Every home server app
// is one site to the browser, so SameSite cookies alone don't stop a
// cross-app form.
func OriginCheck(lanDomain string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		from := c.GetHeader("Origin")
		if from == "" {
			from = c.GetHeader("Referer")
		}
		if !onDomain(from, lanDomain) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func onDomain(raw, lanDomain string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	d := strings.ToLower(lanDomain)
	return d != "" && (host == d || strings.HasSuffix(host, "."+d))
}
