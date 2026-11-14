package handlers

import (
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BatMichoo/homeserver/auth/internal/accounts"
	"github.com/BatMichoo/homeserver/auth/internal/i18n"
	"github.com/BatMichoo/homeserver/auth/internal/static"
	"github.com/BatMichoo/homeserver/auth/internal/templates"
	"github.com/BatMichoo/theme"
	"github.com/gin-gonic/gin"
)

// SessionCookie holds the session token, for every app on the LAN domain.
const SessionCookie = "homeserver_session"

type web struct {
	acc       *accounts.Service
	set       *template.Template
	lanDomain string
	publicURL string
}

// pageData is what the auth page templates get.
type pageData struct {
	Lang     string
	User     *accounts.User // nil when logged out
	Next     string         // where to go after logging in
	Username string         // to refill the form
	Form     string         // which form on the account page the message is for
	Error    string
	OK       string
}

// NewWebRouter is the whole service: health check, pages, forward auth.
func NewWebRouter(d Deps) (*gin.Engine, error) {
	set, err := theme.AddTemplates(template.New("").Funcs(template.FuncMap{"t": i18n.T}))
	if err != nil {
		return nil, err
	}
	if set, err = set.ParseFS(templates.FS, "pages/*.html"); err != nil {
		return nil, err
	}
	h := &web{acc: d.Accounts, set: set, lanDomain: d.LanDomain, publicURL: d.PublicURL}

	r := NewRouter(d)
	r.Use(OriginCheck(d.LanDomain))
	r.GET(theme.DefaultPrefix+"/*file", gin.WrapH(http.StripPrefix(theme.DefaultPrefix, theme.Handler())))
	r.GET("/static/auth/*file", gin.WrapH(http.StripPrefix("/static/auth", http.FileServer(http.FS(static.FS)))))

	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusSeeOther, "/account") })
	r.GET("/login", h.getLogin)
	r.POST("/login", h.postLogin)
	r.GET("/register", h.getRegister)
	r.POST("/register", h.postRegister)
	r.POST("/logout", h.logout)
	r.GET("/account", h.getAccount)
	r.POST("/account", h.postProfile)
	r.POST("/account/password", h.postPassword)
	r.POST("/account/sessions", h.postSessions)
	r.POST("/lang", h.postLang)
	r.GET("/verify", h.verify)
	r.GET("/redirect-to-login", h.redirectToLogin)
	return r, nil
}

func lang(c *gin.Context) string {
	v, _ := c.Cookie(i18n.CookieName)
	return i18n.Lang(v)
}

// session is the logged-in user and their token, or nil. A session whose
// expiry moved forward gets its cookie refreshed.
func (h *web) session(c *gin.Context) (*accounts.User, string) {
	token, err := c.Cookie(SessionCookie)
	if err != nil || token == "" {
		return nil, ""
	}
	u, renewed, err := h.acc.Verify(c.Request.Context(), token)
	if err != nil {
		if !errors.Is(err, accounts.ErrNoSession) {
			log.Printf("verify session: %v", err)
		}
		return nil, ""
	}
	if !renewed.IsZero() {
		h.setSessionCookie(c, token, int(h.acc.TTL().Seconds()))
	}
	return &u, token
}

func (h *web) setSessionCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: SessionCookie, Value: token, Domain: h.lanDomain, Path: "/",
		MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (h *web) startSession(c *gin.Context, u accounts.User) bool {
	token, _, err := h.acc.StartSession(c.Request.Context(), u.ID, c.Request.UserAgent())
	if err != nil {
		log.Printf("start session: %v", err)
		return false
	}
	h.setSessionCookie(c, token, int(h.acc.TTL().Seconds()))
	return true
}

// render draws a page in the theme's shell.
func (h *web) render(c *gin.Context, status int, content, titleKey string, d pageData) {
	app := i18n.T(d.Lang, "app")
	page := theme.Page{
		Lang:         d.Lang,
		Title:        i18n.T(d.Lang, titleKey) + " · " + app,
		Mode:         theme.ModeFromRequest(c.Request),
		CookieDomain: h.lanDomain,
		Header: theme.Header{
			AppName: app, Lang: d.Lang, LoggedIn: d.User != nil,
			LanguageURL: "/lang", AccountURL: "/account", LogoutURL: "/logout",
		},
		Footer:  theme.Footer{Year: time.Now().Year(), AppName: app, Lang: d.Lang},
		Content: content,
		Data:    d,
		CSS:     []string{"/static/auth/auth.css?v=" + theme.AssetVersion()},
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Status(status)
	if err := h.set.ExecuteTemplate(c.Writer, "theme/page", page); err != nil {
		log.Printf("render %s: %v", content, err)
	}
}

// errorText is the translated message for a service error.
func errorText(l string, err error) string {
	var in *accounts.InputError
	switch {
	case errors.As(err, &in):
		return i18n.T(l, "error."+in.Key)
	case errors.Is(err, accounts.ErrUsernameTaken):
		return i18n.T(l, "error.username_taken")
	case errors.Is(err, accounts.ErrInvalidCredentials):
		return i18n.T(l, "login.error")
	case errors.Is(err, accounts.ErrThrottled):
		return i18n.T(l, "error.throttled")
	}
	log.Printf("auth: %v", err)
	return i18n.T(l, "error.generic")
}

func (h *web) getLogin(c *gin.Context) {
	if u, _ := h.session(c); u != nil {
		c.Redirect(http.StatusSeeOther, accounts.SafeNext(c.Query("next"), h.lanDomain))
		return
	}
	h.render(c, http.StatusOK, "auth/login", "login.title", pageData{Lang: lang(c), Next: c.Query("next")})
}

func (h *web) postLogin(c *gin.Context) {
	l := lang(c)
	username, next := c.PostForm("username"), c.PostForm("next")
	u, err := h.acc.Login(c.Request.Context(), username, c.PostForm("password"))
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, accounts.ErrInvalidCredentials):
			status = http.StatusUnauthorized
		case errors.Is(err, accounts.ErrThrottled):
			status = http.StatusTooManyRequests
		}
		h.render(c, status, "auth/login", "login.title", pageData{Lang: l, Next: next, Username: username, Error: errorText(l, err)})
		return
	}
	if !h.startSession(c, u) {
		h.render(c, http.StatusInternalServerError, "auth/login", "login.title", pageData{Lang: l, Next: next, Username: username, Error: i18n.T(l, "error.generic")})
		return
	}
	c.Redirect(http.StatusSeeOther, accounts.SafeNext(next, h.lanDomain))
}

func (h *web) getRegister(c *gin.Context) {
	if u, _ := h.session(c); u != nil {
		c.Redirect(http.StatusSeeOther, accounts.SafeNext(c.Query("next"), h.lanDomain))
		return
	}
	h.render(c, http.StatusOK, "auth/register", "register.title", pageData{Lang: lang(c), Next: c.Query("next")})
}

func (h *web) postRegister(c *gin.Context) {
	l := lang(c)
	username, next := c.PostForm("username"), c.PostForm("next")
	u, err := h.acc.Register(c.Request.Context(), username, c.PostForm("password"), c.PostForm("confirm_password"))
	if err != nil {
		status := http.StatusUnprocessableEntity
		if !errors.Is(err, accounts.ErrInvalidInput) && !errors.Is(err, accounts.ErrUsernameTaken) {
			status = http.StatusInternalServerError
		}
		h.render(c, status, "auth/register", "register.title", pageData{Lang: l, Next: next, Username: username, Error: errorText(l, err)})
		return
	}
	if !h.startSession(c, u) {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	c.Redirect(http.StatusSeeOther, accounts.SafeNext(next, h.lanDomain))
}

func (h *web) logout(c *gin.Context) {
	if token, err := c.Cookie(SessionCookie); err == nil && token != "" {
		if err := h.acc.EndSession(c.Request.Context(), token); err != nil {
			log.Printf("end session: %v", err)
		}
	}
	h.setSessionCookie(c, "", -1)
	c.Redirect(http.StatusSeeOther, "/login")
}

// account runs fn for the logged-in user, or sends them to log in.
func (h *web) account(c *gin.Context, fn func(u *accounts.User, token string)) {
	u, token := h.session(c)
	if u == nil {
		c.Redirect(http.StatusSeeOther, "/login?next="+url.QueryEscape("/account"))
		return
	}
	fn(u, token)
}

func (h *web) getAccount(c *gin.Context) {
	h.account(c, func(u *accounts.User, _ string) {
		h.render(c, http.StatusOK, "auth/account", "account.title", pageData{Lang: lang(c), User: u})
	})
}

// accountResult draws the account page after a form, with its message.
func (h *web) accountResult(c *gin.Context, u *accounts.User, form string, err error, okKey string) {
	l := lang(c)
	d := pageData{Lang: l, User: u, Form: form}
	status := http.StatusOK
	if err != nil {
		d.Error = errorText(l, err)
		status = http.StatusUnprocessableEntity
		if !errors.Is(err, accounts.ErrInvalidInput) && !errors.Is(err, accounts.ErrInvalidCredentials) {
			status = http.StatusInternalServerError
		}
	} else {
		d.OK = i18n.T(l, okKey)
	}
	h.render(c, status, "auth/account", "account.title", d)
}

func (h *web) postProfile(c *gin.Context) {
	h.account(c, func(u *accounts.User, _ string) {
		name, email := c.PostForm("display_name"), c.PostForm("email")
		err := h.acc.UpdateProfile(c.Request.Context(), u.ID, name, email)
		if err == nil {
			u.DisplayName, u.Email = strings.TrimSpace(name), strings.TrimSpace(email)
		}
		h.accountResult(c, u, "profile", err, "account.saved")
	})
}

func (h *web) postPassword(c *gin.Context) {
	h.account(c, func(u *accounts.User, token string) {
		err := h.acc.ChangePassword(c.Request.Context(), u.ID, token,
			c.PostForm("current_password"), c.PostForm("new_password"), c.PostForm("confirm_password"))
		if errors.Is(err, accounts.ErrInvalidCredentials) {
			err = &accounts.InputError{Key: "current_password"}
		}
		h.accountResult(c, u, "password", err, "account.password_changed")
	})
}

func (h *web) postSessions(c *gin.Context) {
	h.account(c, func(u *accounts.User, token string) {
		h.accountResult(c, u, "sessions", h.acc.EndOtherSessions(c.Request.Context(), u.ID, token), "account.sessions_ended")
	})
}

// postLang saves the language for every app (the theme header's EN/BG).
func (h *web) postLang(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: i18n.CookieName, Value: i18n.Lang(c.PostForm("lang")), Domain: h.lanDomain, Path: "/",
		MaxAge: 365 * 24 * 3600, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	if c.GetHeader("HX-Request") == "true" {
		c.Header("HX-Refresh", "true")
		c.Status(http.StatusOK)
		return
	}
	c.Redirect(http.StatusSeeOther, accounts.SafeNext(c.GetHeader("Referer"), h.lanDomain))
}
