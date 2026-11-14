package accounts

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)} }

func TestThrottle(t *testing.T) {
	c := newClock()
	th := NewThrottle(c.now)
	for i := 0; i < 5; i++ {
		assert.True(t, th.Allow("ivan"), "free try %d", i+1)
		th.Fail("ivan")
	}
	// After 5 failures each further try is only allowed once the backoff
	// (1s, 2s, 4s, 8s, 16s, then 30s) has passed since the last failure.
	for _, wait := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		assert.False(t, th.Allow("ivan"), "inside %ds backoff", wait)
		c.add(wait*time.Second - time.Millisecond)
		assert.False(t, th.Allow("ivan"), "just before %ds", wait)
		c.add(time.Millisecond)
		assert.True(t, th.Allow("ivan"), "after %ds", wait)
		th.Fail("ivan")
	}
	assert.True(t, th.Allow("maria"), "per username")
	th.Reset("ivan")
	assert.True(t, th.Allow("ivan"))
}

// Parallel tries can't share one slot: once backoff applies, only one of
// many simultaneous attempts gets through.
func TestThrottleParallel(t *testing.T) {
	c := newClock()
	th := NewThrottle(c.now)
	for i := 0; i < 5; i++ {
		th.Fail("ivan")
	}
	c.add(time.Minute) // backoff over: exactly one try may go now
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if th.Allow("ivan") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, allowed.Load())
}

func TestThrottleForgets(t *testing.T) {
	c := newClock()
	th := NewThrottle(c.now)
	for i := 0; i < 6; i++ {
		th.Fail("ivan")
	}
	th.Fail("someone-else")
	assert.False(t, th.Allow("ivan"))
	c.add(16 * time.Minute)
	th.Prune()
	assert.Zero(t, th.Len(), "old entries dropped")
	assert.True(t, th.Allow("ivan"))
}
