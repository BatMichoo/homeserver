package accounts

import (
	"sync"
	"time"
)

const (
	throttleFree   = 5                // failures before any wait
	throttleWindow = 15 * time.Minute // failures older than this are forgotten
	throttleMax    = 30 * time.Second
)

// Throttle slows down guessing: after 5 failed logins for a username
// within 15 minutes, each further try waits 1s, 2s, 4s… up to 30s. It lives
// in memory (the service runs as one instance).
type Throttle struct {
	mu      sync.Mutex
	entries map[string]throttleEntry
	now     func() time.Time
}

type throttleEntry struct {
	failures int
	last     time.Time
}

// NewThrottle makes a throttle reading the time from now.
func NewThrottle(now func() time.Time) *Throttle {
	return &Throttle{entries: map[string]throttleEntry{}, now: now}
}

// current returns username's entry, dropping it if it's too old. Callers
// hold mu.
func (t *Throttle) current(username string) throttleEntry {
	e, ok := t.entries[username]
	if ok && t.now().Sub(e.last) > throttleWindow {
		delete(t.entries, username)
		return throttleEntry{}
	}
	return e
}

// Delay is how long to wait before checking a login for username.
func (t *Throttle) Delay(username string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	over := t.current(username).failures - throttleFree
	if over < 0 {
		return 0
	}
	if over >= 5 {
		return throttleMax
	}
	return min(time.Second<<over, throttleMax)
}

// Fail records a failed login.
func (t *Throttle) Fail(username string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.current(username)
	e.failures++
	e.last = t.now()
	t.entries[username] = e
}

// Reset forgets username's failures (after a successful login).
func (t *Throttle) Reset(username string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, username)
}

// Prune drops every entry older than the window, so guesses at many
// usernames don't pile up in memory.
func (t *Throttle) Prune() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.entries {
		if t.now().Sub(e.last) > throttleWindow {
			delete(t.entries, k)
		}
	}
}

// Len is how many usernames are being tracked.
func (t *Throttle) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}
