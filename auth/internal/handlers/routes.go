// Package handlers is the auth service's HTTP layer: request parsing,
// validation and rendering, over the accounts service.
package handlers

import (
	"context"
	"net/http"

	"github.com/BatMichoo/homeserver/auth/internal/accounts"
	"github.com/gin-gonic/gin"
)

// Pinger reports whether the database answers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps is everything the handlers need, built once in main.
type Deps struct {
	DB        Pinger
	Accounts  *accounts.Service
	LanDomain string // cookies are set for it; logins only return to it
	PublicURL string // e.g. https://auth.homeserver.lan
}

// NewRouter is the base router with the health check; NewWebRouter adds
// the pages.
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		if err := d.DB.Ping(c.Request.Context()); err != nil {
			c.String(http.StatusServiceUnavailable, "database unavailable")
			return
		}
		c.String(http.StatusOK, "ok")
	})
	return r
}
