package accounts

import (
	"sync"
	"time"
)

const (
	throttleFree   = 5                // failures before any backoff
	throttleWindow = 15 * time.Minute // failures older than this are forgotten
	throttleMax    = 30 * time.Second
)

// Throttle slows down guessing: after 5 failed logins for a username
// within 15 minutes, the next try is allowed only 1s after the last
// failure, then 2s, 4s… up to 30s. A try reserves its slot, so parallel
// attempts can't share one. It lives in memory (one instance).
type Throttle struct {
	mu      sync.Mutex
	entries map[string]throttleEntry
	now     func() time.Time
}

type throttleEntry struct {
	failures int
	last     time.Time // last failure
	until    time.Time // no try before this
}

// NewThrottle makes a throttle reading the time from now.
func NewThrottle(now func() time.Time) *Throttle {
	return &Throttle{entries: map[string]throttleEntry{}, now: now}
}

// backoff is the wait after f failures.
func backoff(f int) time.Duration {
	over := f - throttleFree
	switch {
	case over < 0:
		return 0
	case over >= 5:
		return throttleMax
	}
	return min(time.Second<<over, throttleMax)
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

// Allow reports whether a login for username may be checked now. Once
// backoff applies, an allowed try holds the slot until its result (Fail
// or Reset), so simultaneous tries are refused.
func (t *Throttle) Allow(username string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.current(username)
	if now.Before(e.until) {
		return false
	}
	if d := backoff(e.failures); d > 0 {
		e.until = now.Add(d)
		t.entries[username] = e
	}
	return true
}

// Fail records a failed login.
func (t *Throttle) Fail(username string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.current(username)
	e.failures++
	e.last = now
	e.until = now.Add(backoff(e.failures))
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
