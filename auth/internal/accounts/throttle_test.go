package accounts

import (
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
	for i := 0; i < 4; i++ {
		th.Fail("ivan")
		assert.Zero(t, th.Delay("ivan"), "after %d failures", i+1)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for _, w := range want {
		th.Fail("ivan")
		assert.Equal(t, w*time.Second, th.Delay("ivan"))
	}
	assert.Zero(t, th.Delay("maria"), "per username")
	th.Reset("ivan")
	assert.Zero(t, th.Delay("ivan"))
}

func TestThrottleForgets(t *testing.T) {
	c := newClock()
	th := NewThrottle(c.now)
	for i := 0; i < 6; i++ {
		th.Fail("ivan")
	}
	th.Fail("someone-else")
	assert.NotZero(t, th.Delay("ivan"))
	c.add(16 * time.Minute)
	th.Prune()
	assert.Zero(t, th.Len(), "old entries dropped")
	assert.Zero(t, th.Delay("ivan"))
}
