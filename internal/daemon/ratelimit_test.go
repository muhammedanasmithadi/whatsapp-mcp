package daemon

import (
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestRateLimit_AllowBurstThenDeny(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	l := newLimiterWithClock(1.0, 3, clk)

	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("request %d should be allowed (within burst)", i)
		}
	}
	if l.Allow() {
		t.Fatal("4th request should be denied (burst exhausted)")
	}

	clk.Advance(1 * time.Second)
	if !l.Allow() {
		t.Fatal("after 1s, one request should be allowed")
	}
	if l.Allow() {
		t.Fatal("only one token should have refilled after 1s")
	}

	clk.Advance(5 * time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("after 5s refill, request %d should be allowed", i)
		}
	}
	if l.Allow() {
		t.Fatal("tokens should be capped at burst")
	}
}

func TestRateLimit_PairPageSurvivesFiveSecondAutoRefresh(t *testing.T) {
	clk := &fakeClock{now: time.Now()}
	l := newLimiterWithClock(15.0/60.0, 5, clk) // matches pairGetLimiter

	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("burst request %d should be allowed", i)
		}
	}

	for i := 0; i < 30; i++ {
		clk.Advance(5 * time.Second)
		if !l.Allow() {
			t.Fatalf("refresh %d at 5s cadence should never be permanently rate-limited", i)
		}
	}
}
