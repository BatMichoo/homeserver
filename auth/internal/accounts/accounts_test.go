package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/auth/internal/db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const ttl = 720 * time.Hour

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	c := newClock()
	s := New(dbtest.New(t), ttl)
	s.now = c.now
	s.throttle = NewThrottle(c.now)
	return s, c
}

var ctx = context.Background()

func TestRegisterRules(t *testing.T) {
	s, _ := newService(t)
	for _, c := range []struct{ user, pw, confirm string }{
		{"iv", "secret1", "secret1"},
		{strings.Repeat("a", 51), "secret1", "secret1"},
		{"ivan", "12345", "12345"},
		{"ivan", "secret1", "secret2"},
		{"   ", "secret1", "secret1"},
	} {
		_, err := s.Register(ctx, c.user, c.pw, c.confirm)
		assert.ErrorIs(t, err, ErrInvalidInput, "%+v", c)
	}
	u, err := s.Register(ctx, "Ivan", "secret1", "secret1")
	require.NoError(t, err)
	assert.Len(t, u.ID, 36)
	_, err = s.Register(ctx, "Ivan", "secret1", "secret1")
	assert.ErrorIs(t, err, ErrUsernameTaken)
	_, err = s.Register(ctx, "ivan", "secret1", "secret1")
	assert.NoError(t, err, "usernames are case-sensitive")
}

func TestLoginWrongPasswordAndUnknownUser(t *testing.T) {
	s, _ := newService(t)
	_, err := s.Register(ctx, "ivan", "secret1", "secret1")
	require.NoError(t, err)
	_, err = s.Login(ctx, "ivan", "wrong")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = s.Login(ctx, "nobody", "secret1")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	u, err := s.Login(ctx, "ivan", "secret1")
	require.NoError(t, err)
	assert.Equal(t, "ivan", u.Username)
}

func TestLoginThrottles(t *testing.T) {
	s, c := newService(t)
	_, err := s.Register(ctx, "ivan", "secret1", "secret1")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, err = s.Login(ctx, "ivan", "wrong")
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	}
	_, err = s.Login(ctx, "ivan", "secret1")
	assert.ErrorIs(t, err, ErrThrottled, "even the right password waits out the backoff")
	c.add(time.Second)
	_, err = s.Login(ctx, "ivan", "secret1")
	require.NoError(t, err)
	assert.True(t, s.throttle.Allow("ivan"), "success resets")
}

// Overlong usernames can't exist, are refused like a wrong password, and
// aren't remembered (they'd only fill memory).
func TestLoginLongUsernameNotTracked(t *testing.T) {
	s, _ := newService(t)
	for i := 0; i < 3; i++ {
		_, err := s.Login(ctx, strings.Repeat("x", 51+i), "pw")
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	}
	assert.Zero(t, s.throttle.Len())
}

func TestPasswordHashIsBcryptDefaultCost(t *testing.T) {
	s, _ := newService(t)
	u, err := s.Register(ctx, "ivan", "secret1", "secret1")
	require.NoError(t, err)
	_, hash, err := s.store.UserByID(ctx, u.ID)
	require.NoError(t, err)
	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, bcrypt.DefaultCost, cost)
}

func TestStartSessionStoresHashOnly(t *testing.T) {
	s, _ := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	token, expires, err := s.StartSession(ctx, u.ID, "curl")
	require.NoError(t, err)
	assert.Len(t, token, 43)
	assert.Equal(t, s.now().Add(ttl), expires)
	var stored []byte
	var agent string
	require.NoError(t, s.store.(sqlStore).DB().QueryRowContext(ctx, "SELECT token_hash, user_agent FROM sessions").Scan(&stored, &agent))
	sum := sha256.Sum256([]byte(token))
	assert.Equal(t, sum[:], stored)
	assert.Equal(t, "curl", agent)
}

func TestVerifySlides(t *testing.T) {
	s, c := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	token, _, err := s.StartSession(ctx, u.ID, "")
	require.NoError(t, err)

	got, renewed, err := s.Verify(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	assert.True(t, renewed.IsZero(), "no slide within the hour")

	c.add(61 * time.Minute)
	_, renewed, err = s.Verify(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, c.now().Add(ttl), renewed)
	_, renewed, err = s.Verify(ctx, token)
	require.NoError(t, err)
	assert.True(t, renewed.IsZero(), "slid once")

	var expires time.Time
	require.NoError(t, s.store.(sqlStore).DB().QueryRowContext(ctx, "SELECT expires_at FROM sessions").Scan(&expires))
	assert.True(t, expires.Equal(c.now().Add(ttl)))
}

func TestVerifyExpiredAndUnknown(t *testing.T) {
	s, c := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	token, _, _ := s.StartSession(ctx, u.ID, "")
	c.add(ttl + time.Minute)
	_, _, err := s.Verify(ctx, token)
	assert.ErrorIs(t, err, ErrNoSession)
	_, _, err = s.Verify(ctx, "not-a-token")
	assert.ErrorIs(t, err, ErrNoSession)
	_, _, err = s.Verify(ctx, "")
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestSweep(t *testing.T) {
	s, c := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	_, _, _ = s.StartSession(ctx, u.ID, "")
	c.add(ttl / 2)
	live, _, _ := s.StartSession(ctx, u.ID, "")
	c.add(ttl/2 + time.Minute)
	n, err := s.Sweep(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	_, _, err = s.Verify(ctx, live)
	assert.NoError(t, err)
}

func TestEndSessions(t *testing.T) {
	s, _ := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	a, _, _ := s.StartSession(ctx, u.ID, "")
	b, _, _ := s.StartSession(ctx, u.ID, "")
	cc, _, _ := s.StartSession(ctx, u.ID, "")
	require.NoError(t, s.EndSession(ctx, a))
	_, _, err := s.Verify(ctx, a)
	assert.ErrorIs(t, err, ErrNoSession)
	require.NoError(t, s.EndOtherSessions(ctx, u.ID, b))
	_, _, err = s.Verify(ctx, b)
	assert.NoError(t, err)
	_, _, err = s.Verify(ctx, cc)
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	s, _ := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	keep, _, _ := s.StartSession(ctx, u.ID, "")
	other, _, _ := s.StartSession(ctx, u.ID, "")
	assert.ErrorIs(t, s.ChangePassword(ctx, u.ID, keep, "wrong", "secret2", "secret2"), ErrInvalidCredentials)
	assert.ErrorIs(t, s.ChangePassword(ctx, u.ID, keep, "secret1", "12345", "12345"), ErrInvalidInput)
	assert.ErrorIs(t, s.ChangePassword(ctx, u.ID, keep, "secret1", "secret2", "secret3"), ErrInvalidInput)
	require.NoError(t, s.ChangePassword(ctx, u.ID, keep, "secret1", "secret2", "secret2"))
	_, _, err := s.Verify(ctx, keep)
	assert.NoError(t, err)
	_, _, err = s.Verify(ctx, other)
	assert.ErrorIs(t, err, ErrNoSession)
	_, err = s.Login(ctx, "ivan", "secret2")
	assert.NoError(t, err)
}

func TestResetPassword(t *testing.T) {
	s, _ := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	tok, _, _ := s.StartSession(ctx, u.ID, "")
	pw, err := s.ResetPassword(ctx, "ivan")
	require.NoError(t, err)
	assert.Len(t, pw, 16)
	_, _, err = s.Verify(ctx, tok)
	assert.ErrorIs(t, err, ErrNoSession)
	_, err = s.Login(ctx, "ivan", pw)
	assert.NoError(t, err)
	_, err = s.ResetPassword(ctx, "nobody")
	assert.ErrorIs(t, err, ErrUnknownUser)
}

func TestProfileRules(t *testing.T) {
	s, _ := newService(t)
	u, _ := s.Register(ctx, "ivan", "secret1", "secret1")
	assert.ErrorIs(t, s.UpdateProfile(ctx, u.ID, strings.Repeat("и", 61), ""), ErrInvalidInput)
	assert.ErrorIs(t, s.UpdateProfile(ctx, u.ID, "", "not-an-email"), ErrInvalidInput)
	assert.ErrorIs(t, s.UpdateProfile(ctx, u.ID, "", strings.Repeat("a", 250)+"@x.bg"), ErrInvalidInput)
	require.NoError(t, s.UpdateProfile(ctx, u.ID, "  Иван  ", " ivan@example.bg "))
	got, _, err := s.store.UserByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Иван", got.DisplayName)
	assert.Equal(t, "ivan@example.bg", got.Email)
	assert.Equal(t, "Иван", got.Name())
	require.NoError(t, s.UpdateProfile(ctx, u.ID, "", ""))
	got, _, _ = s.store.UserByID(ctx, u.ID)
	assert.Equal(t, "ivan", got.Name())
}

// sqlStore lets tests look at the rows behind the store.
type sqlStore interface{ DB() *sql.DB }
