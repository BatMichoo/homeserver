// Package accounts is the auth service's logic: users, passwords and
// sessions.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BatMichoo/homeserver/auth/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// User is an account (see model.User).
type User = model.User

// InputError is a value that breaks a rule; Key names the rule (for a
// translated message). errors.Is(err, ErrInvalidInput) holds for it.
type InputError struct{ Key string }

func (e *InputError) Error() string        { return "invalid input: " + e.Key }
func (e *InputError) Is(target error) bool { return target == ErrInvalidInput }

var (
	ErrInvalidCredentials = errors.New("wrong username or password")
	ErrInvalidInput       = errors.New("invalid input")
	ErrUnknownUser        = model.ErrUnknownUser
	ErrUsernameTaken      = model.ErrUsernameTaken
	ErrNoSession          = model.ErrNoSession
)

// Rules for accounts (the same as Expenser's).
const (
	minUsername    = 3
	maxUsername    = 50
	minPassword    = 6
	maxDisplayName = 60
	maxEmail       = 254
	// slideEvery: a session's expiry moves forward at most this often.
	slideEvery = time.Hour
)

// Store is where users and sessions live (*db.DB).
type Store interface {
	CreateUser(ctx context.Context, username, passwordHash string) (User, error)
	UserByUsername(ctx context.Context, username string) (User, string, error)
	UserByID(ctx context.Context, id string) (User, string, error)
	UpdateProfile(ctx context.Context, id, displayName, email string) error
	SetPasswordHash(ctx context.Context, id, hash string) error
	CreateSession(ctx context.Context, tokenHash []byte, userID, userAgent string, now, expires time.Time) error
	SessionUser(ctx context.Context, tokenHash []byte, now time.Time) (User, time.Time, time.Time, error)
	TouchSession(ctx context.Context, tokenHash []byte, now, expires time.Time) error
	DeleteSession(ctx context.Context, tokenHash []byte) error
	DeleteUserSessions(ctx context.Context, userID string, except []byte) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)
}

// Service is the accounts logic over a Store.
type Service struct {
	store    Store
	ttl      time.Duration
	throttle *Throttle
	now      func() time.Time
	sleep    func(time.Duration)
}

// New makes the service; sessions last ttl since their last use.
func New(store Store, ttl time.Duration) *Service {
	return &Service{
		store:    store,
		ttl:      ttl,
		throttle: NewThrottle(time.Now),
		now:      time.Now,
		sleep:    time.Sleep,
	}
}

// TTL is how long a session lasts since its last extension.
func (s *Service) TTL() time.Duration { return s.ttl }

// Register creates an account.
func (s *Service) Register(ctx context.Context, username, password, confirm string) (User, error) {
	username = strings.TrimSpace(username)
	if n := utf8.RuneCountInString(username); n < minUsername || n > maxUsername {
		return User{}, &InputError{"username_length"}
	}
	if err := checkNewPassword(password, confirm); err != nil {
		return User{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	return s.store.CreateUser(ctx, username, hash)
}

// Login checks a username and password. After repeated failures for a
// username it waits first (see Throttle). Unknown usernames and wrong
// passwords give the same error, and take as long.
func (s *Service) Login(ctx context.Context, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if d := s.throttle.Delay(username); d > 0 {
		s.sleep(d)
	}
	u, hash, err := s.store.UserByUsername(ctx, username)
	if errors.Is(err, ErrUnknownUser) {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		s.throttle.Fail(username)
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		s.throttle.Fail(username)
		return User{}, ErrInvalidCredentials
	}
	s.throttle.Reset(username)
	return u, nil
}

// StartSession creates a session and returns its token (for the cookie).
func (s *Service) StartSession(ctx context.Context, userID, userAgent string) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	expires := now.Add(s.ttl)
	if err := s.store.CreateSession(ctx, hashToken(token), userID, userAgent, now, expires); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, expires, nil
}

// Verify finds the user of a session. When the session's expiry moved
// forward (at most hourly), renewedUntil is the new expiry, so the cookie
// can be refreshed; otherwise it's zero. ErrNoSession if the token is
// unknown or expired.
func (s *Service) Verify(ctx context.Context, token string) (User, time.Time, error) {
	if token == "" {
		return User{}, time.Time{}, ErrNoSession
	}
	now := s.now()
	h := hashToken(token)
	u, _, lastSeen, err := s.store.SessionUser(ctx, h, now)
	if err != nil {
		return User{}, time.Time{}, err
	}
	if now.Sub(lastSeen) < slideEvery {
		return u, time.Time{}, nil
	}
	expires := now.Add(s.ttl)
	if err := s.store.TouchSession(ctx, h, now, expires); err != nil {
		return User{}, time.Time{}, fmt.Errorf("extend session: %w", err)
	}
	return u, expires, nil
}

// EndSession ends one session (logout).
func (s *Service) EndSession(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashToken(token))
}

// EndOtherSessions ends every session of userID except keepToken's.
func (s *Service) EndOtherSessions(ctx context.Context, userID, keepToken string) error {
	return s.store.DeleteUserSessions(ctx, userID, hashToken(keepToken))
}

// UpdateProfile sets the display name and email ("" clears them).
func (s *Service) UpdateProfile(ctx context.Context, userID, displayName, email string) error {
	displayName = strings.TrimSpace(displayName)
	email = strings.TrimSpace(email)
	if utf8.RuneCountInString(displayName) > maxDisplayName {
		return &InputError{"display_name_long"}
	}
	if email != "" && (len(email) > maxEmail || !strings.Contains(email, "@")) {
		return &InputError{"email_invalid"}
	}
	return s.store.UpdateProfile(ctx, userID, displayName, email)
}

// ChangePassword sets a new password after checking the current one, and
// ends the user's other sessions.
func (s *Service) ChangePassword(ctx context.Context, userID, keepToken, current, next, confirm string) error {
	_, hash, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return ErrInvalidCredentials
	}
	if err := checkNewPassword(next, confirm); err != nil {
		return err
	}
	newHash, err := hashPassword(next)
	if err != nil {
		return err
	}
	if err := s.store.SetPasswordHash(ctx, userID, newHash); err != nil {
		return err
	}
	return s.EndOtherSessions(ctx, userID, keepToken)
}

// ResetPassword gives a user a new random password and ends all their
// sessions (for when they've forgotten it; there's no email reset).
func (s *Service) ResetPassword(ctx context.Context, username string) (string, error) {
	u, _, err := s.store.UserByUsername(ctx, username)
	if err != nil {
		return "", err
	}
	password, err := randomPassword(16)
	if err != nil {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	if err := s.store.SetPasswordHash(ctx, u.ID, hash); err != nil {
		return "", err
	}
	if err := s.store.DeleteUserSessions(ctx, u.ID, nil); err != nil {
		return "", err
	}
	return password, nil
}

// Sweep deletes expired sessions and forgets old failed logins.
func (s *Service) Sweep(ctx context.Context) (int64, error) {
	s.throttle.Prune()
	return s.store.DeleteExpiredSessions(ctx, s.now())
}

func checkNewPassword(password, confirm string) error {
	if utf8.RuneCountInString(password) < minPassword {
		return &InputError{"password_short"}
	}
	if len(password) > 72 { // bcrypt's limit
		return &InputError{"password_long"}
	}
	if password != confirm {
		return &InputError{"password_mismatch"}
	}
	return nil
}

func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// dummyHash is compared against for unknown usernames, so they take as
// long as wrong passwords.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)
	return h
})

const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[k.Int64()]
	}
	return string(b), nil
}
