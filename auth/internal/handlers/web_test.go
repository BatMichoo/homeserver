package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/auth/internal/accounts"
	"github.com/BatMichoo/homeserver/auth/internal/db/dbtest"
	"github.com/PuerkitoBio/goquery"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const origin = "https://auth.homeserver.lan"

type server struct {
	t   *testing.T
	r   *gin.Engine
	acc *accounts.Service
}

func newServer(t *testing.T) *server {
	t.Helper()
	store := dbtest.New(t)
	acc := accounts.New(store, 720*time.Hour)
	r, err := NewWebRouter(Deps{DB: store, Accounts: acc, LanDomain: "homeserver.lan", PublicURL: origin})
	require.NoError(t, err)
	return &server{t: t, r: r, acc: acc}
}

// do sends a request; form values make it a POST with Origin set.
func (s *server) do(method, path string, form url.Values, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	s.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.r.ServeHTTP(w, req)
	return w
}

func cookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (s *server) register(username string) *http.Cookie {
	s.t.Helper()
	w := s.do("POST", "/register", url.Values{"username": {username}, "password": {"secret1"}, "confirm_password": {"secret1"}}, nil)
	require.Equal(s.t, http.StatusSeeOther, w.Code, w.Body.String())
	c := cookie(w, "homeserver_session")
	require.NotNil(s.t, c)
	return c
}

func doc(t *testing.T, w *httptest.ResponseRecorder) *goquery.Document {
	t.Helper()
	d, err := goquery.NewDocumentFromReader(w.Body)
	require.NoError(t, err)
	return d
}

func TestRegisterLoginLogout(t *testing.T) {
	s := newServer(t)
	c := s.register("ivan")
	raw := s.do("POST", "/login", url.Values{"username": {"ivan"}, "password": {"secret1"}}, nil).Header().Get("Set-Cookie")
	for _, part := range []string{"homeserver_session=", "Domain=homeserver.lan", "Path=/", "Secure", "HttpOnly", "SameSite=Lax", "Max-Age=2592000"} {
		assert.Contains(t, raw, part)
	}

	w := s.do("GET", "/verify", nil, nil, c)
	assert.Equal(t, 200, w.Code)
	assert.Len(t, w.Header().Get("X-User-Id"), 36)
	assert.Equal(t, "ivan", w.Header().Get("X-User-Name"))

	w = s.do("POST", "/logout", url.Values{}, nil, c)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/login", w.Header().Get("Location"))
	cleared := cookie(w, "homeserver_session")
	require.NotNil(t, cleared)
	assert.True(t, cleared.MaxAge < 0)
	assert.Equal(t, 401, s.do("GET", "/verify", nil, nil, c).Code)
}

func TestLoginRedirectsToSafeNext(t *testing.T) {
	s := newServer(t)
	s.register("ivan")
	login := func(next string) string {
		w := s.do("POST", "/login", url.Values{"username": {"ivan"}, "password": {"secret1"}, "next": {next}}, nil)
		require.Equal(t, http.StatusSeeOther, w.Code)
		return w.Header().Get("Location")
	}
	assert.Equal(t, "https://expenser.homeserver.lan/house", login("https://expenser.homeserver.lan/house"))
	assert.Equal(t, "/account", login("https://evil.com"))
	assert.Equal(t, "/account", login(""))
}

func TestLoginPageKeepsNext(t *testing.T) {
	s := newServer(t)
	w := s.do("GET", "/login?next="+url.QueryEscape("https://expenser.homeserver.lan/car"), nil, nil)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "https://expenser.homeserver.lan/car", doc(t, w).Find(`input[name="next"]`).AttrOr("value", ""))
}

func TestLoggedInSkipsLogin(t *testing.T) {
	s := newServer(t)
	c := s.register("ivan")
	w := s.do("GET", "/login?next="+url.QueryEscape("https://expenser.homeserver.lan/car"), nil, nil, c)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "https://expenser.homeserver.lan/car", w.Header().Get("Location"))
	w = s.do("GET", "/register", nil, nil, c)
	assert.Equal(t, "/account", w.Header().Get("Location"))
}

func TestLoginErrorSameForUnknownAndWrongPassword(t *testing.T) {
	s := newServer(t)
	s.register("ivan")
	msg := func(user string) string {
		w := s.do("POST", "/login", url.Values{"username": {user}, "password": {"nope"}}, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Nil(t, cookie(w, "homeserver_session"))
		return strings.TrimSpace(doc(t, w).Find(".form-error").Text())
	}
	a, b := msg("ivan"), msg("nobody")
	assert.NotEmpty(t, a)
	assert.Equal(t, a, b)
}

func TestRegisterErrors(t *testing.T) {
	s := newServer(t)
	s.register("ivan")
	for _, f := range []url.Values{
		{"username": {"ivan"}, "password": {"secret1"}, "confirm_password": {"secret1"}},
		{"username": {"iv"}, "password": {"secret1"}, "confirm_password": {"secret1"}},
		{"username": {"maria"}, "password": {"secret1"}, "confirm_password": {"other12"}},
	} {
		w := s.do("POST", "/register", f, nil)
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.NotEmpty(t, strings.TrimSpace(doc(t, w).Find(".form-error").Text()))
	}
}

func TestVerifyEncodesName(t *testing.T) {
	s := newServer(t)
	c := s.register("ivan")
	w := s.do("GET", "/verify", nil, nil, c)
	require.NoError(t, s.acc.UpdateProfile(context.Background(), w.Header().Get("X-User-Id"), "Иван", ""))
	w = s.do("GET", "/verify", nil, nil, c)
	assert.Equal(t, "%D0%98%D0%B2%D0%B0%D0%BD", w.Header().Get("X-User-Name"))
}

func TestVerifyNoCookie(t *testing.T) {
	s := newServer(t)
	w := s.do("GET", "/verify", nil, nil)
	assert.Equal(t, 401, w.Code)
	assert.Empty(t, w.Body.String())
	w = s.do("GET", "/verify", nil, nil, &http.Cookie{Name: "homeserver_session", Value: "made-up"})
	assert.Equal(t, 401, w.Code)
}

func TestRedirectToLogin(t *testing.T) {
	s := newServer(t)
	w := s.do("GET", "/redirect-to-login", nil, map[string]string{"X-Original-URL": "https://expenser.homeserver.lan/house?m=2026-11"})
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, origin+"/login?next=https%3A%2F%2Fexpenser.homeserver.lan%2Fhouse%3Fm%3D2026-11", w.Header().Get("Location"))

	w = s.do("GET", "/redirect-to-login", nil, map[string]string{
		"X-Original-URL": "https://expenser.homeserver.lan/house/list",
		"HX-Request":     "true",
		"HX-Current-URL": "https://expenser.homeserver.lan/car",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, origin+"/login?next=https%3A%2F%2Fexpenser.homeserver.lan%2Fcar", w.Header().Get("HX-Redirect"))

	w = s.do("GET", "/redirect-to-login", nil, nil)
	assert.Equal(t, origin+"/login", w.Header().Get("Location"), "no URL: plain login")
}

func TestOriginCheck(t *testing.T) {
	s := newServer(t)
	c := s.register("ivan")
	post := func(h map[string]string) int {
		return s.do("POST", "/logout", url.Values{}, h, c).Code
	}
	assert.Equal(t, 403, post(map[string]string{"Origin": "https://evil.com"}))
	assert.Equal(t, 403, post(map[string]string{"Origin": ""}))
	assert.Equal(t, 403, post(map[string]string{"Origin": "null"}))
	assert.Equal(t, 403, post(map[string]string{"Origin": "https://homeserver.lan.evil.com"}))
	assert.Equal(t, 403, post(map[string]string{"Origin": "", "Referer": "https://evil.com/x"}))
	assert.Equal(t, http.StatusSeeOther, post(map[string]string{"Origin": "", "Referer": "https://expenser.homeserver.lan/x"}))
	c = s.register("maria")
	assert.Equal(t, http.StatusSeeOther, s.do("POST", "/logout", url.Values{}, map[string]string{"Origin": "https://homeserver.lan"}, c).Code)
}

func TestLangCookie(t *testing.T) {
	s := newServer(t)
	w := s.do("POST", "/lang", url.Values{"lang": {"bg"}}, map[string]string{"HX-Request": "true"})
	assert.Equal(t, "true", w.Header().Get("HX-Refresh"))
	raw := w.Header().Get("Set-Cookie")
	assert.Contains(t, raw, "homeserver_lang=bg")
	assert.Contains(t, raw, "Domain=homeserver.lan")
	assert.NotContains(t, raw, "HttpOnly")
	w = s.do("POST", "/lang", url.Values{"lang": {"de"}}, map[string]string{"HX-Request": "true"})
	assert.Equal(t, "en", cookie(w, "homeserver_lang").Value)

	en := doc(t, s.do("GET", "/login", nil, nil)).Find("h1").Text()
	bg := doc(t, s.do("GET", "/login", nil, nil, &http.Cookie{Name: "homeserver_lang", Value: "bg"})).Find("h1").Text()
	assert.NotEmpty(t, en)
	assert.NotEqual(t, en, bg)
}

func TestAccountPages(t *testing.T) {
	s := newServer(t)
	w := s.do("GET", "/account", nil, nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/login?next=%2Faccount", w.Header().Get("Location"))

	c := s.register("ivan")
	other := cookie(s.do("POST", "/login", url.Values{"username": {"ivan"}, "password": {"secret1"}}, nil), "homeserver_session")
	d := doc(t, s.do("GET", "/account", nil, nil, c))
	assert.Contains(t, d.Find("#content").Text(), "ivan")
	assert.Equal(t, 1, d.Find(`form[action="/logout"]`).Length(), "theme header logout")

	w = s.do("POST", "/account", url.Values{"display_name": {"Иван"}, "email": {"ivan@example.bg"}}, nil, c)
	assert.Equal(t, 200, w.Code)
	d = doc(t, w)
	assert.NotEmpty(t, d.Find(".form-ok").Text())
	assert.Equal(t, "Иван", d.Find(`input[name="display_name"]`).AttrOr("value", ""))

	w = s.do("POST", "/account", url.Values{"email": {"nope"}}, nil, c)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w = s.do("POST", "/account/password", url.Values{"current_password": {"wrong"}, "new_password": {"secret2"}, "confirm_password": {"secret2"}}, nil, c)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.NotEmpty(t, doc(t, w).Find(".form-error").Text())
	w = s.do("POST", "/account/password", url.Values{"current_password": {"secret1"}, "new_password": {"secret2"}, "confirm_password": {"secret2"}}, nil, c)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 401, s.do("GET", "/verify", nil, nil, other).Code, "other session ended")
	assert.Equal(t, 200, s.do("GET", "/verify", nil, nil, c).Code)

	other = cookie(s.do("POST", "/login", url.Values{"username": {"ivan"}, "password": {"secret2"}}, nil), "homeserver_session")
	w = s.do("POST", "/account/sessions", url.Values{}, nil, c)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, 401, s.do("GET", "/verify", nil, nil, other).Code)
}

func TestThemeServed(t *testing.T) {
	s := newServer(t)
	w := s.do("GET", "/static/theme/css/theme.css", nil, nil)
	assert.Equal(t, 200, w.Code)
}
