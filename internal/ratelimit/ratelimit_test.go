package ratelimit

import (
	"context"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		MinSendInterval:       10 * time.Second,
		MaxSendsPerHour:       3,
		NonContactMinInterval: 20 * time.Second,
		NonContactMaxPerHour:  2,
		MaxUsyncPerMinute:     2,
	}
}

func newTestLimiter(t *testing.T, cfg Config) (*Limiter, func(d time.Duration)) {
	t.Helper()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	l := New(cfg)
	l.clock = func() time.Time { return now }
	return l, func(d time.Duration) { now = now.Add(d) }
}

func TestAllowSend_FirstSendAlwaysAllowed(t *testing.T) {
	l, _ := newTestLimiter(t, testConfig())
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("first send should be allowed, got %+v", d)
	}
}

func TestAllowSend_ContactIntervalFloor(t *testing.T) {
	l, advance := newTestLimiter(t, testConfig())

	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("send 1 should pass: %+v", d)
	}
	d := l.AllowSend(true)
	if d.Allowed {
		t.Fatal("send 2 immediately after should be blocked by interval")
	}
	if d.RetryAfter != 10*time.Second {
		t.Errorf("RetryAfter = %s, want 10s", d.RetryAfter)
	}
	advance(9 * time.Second)
	if l.AllowSend(true).Allowed {
		t.Fatal("send at +9s should still be blocked")
	}
	advance(1 * time.Second)
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("send at +10s should pass: %+v", d)
	}
}

func TestAllowSend_NonContactStricterInterval(t *testing.T) {
	l, advance := newTestLimiter(t, testConfig())

	if !l.AllowSend(false).Allowed {
		t.Fatal("non-contact send 1 should pass")
	}
	advance(10 * time.Second)
	d := l.AllowSend(false)
	if d.Allowed {
		t.Fatal("non-contact send at +10s should be blocked by the 20s floor")
	}
	if d.RetryAfter != 10*time.Second {
		t.Errorf("RetryAfter = %s, want 10s", d.RetryAfter)
	}
	advance(10 * time.Second)
	if !l.AllowSend(false).Allowed {
		t.Fatal("non-contact send at +20s should pass")
	}
}

func TestAllowSend_GlobalHourlyCap(t *testing.T) {
	l, advance := newTestLimiter(t, testConfig()) // cap 3/hr

	for i := 0; i < 3; i++ {
		if d := l.AllowSend(true); !d.Allowed {
			t.Fatalf("send %d should pass: %+v", i+1, d)
		}
		advance(10 * time.Second)
	}
	d := l.AllowSend(true)
	if d.Allowed {
		t.Fatal("4th send within the hour should hit the hourly cap")
	}
	if d.Reason != "hourly send cap reached" {
		t.Errorf("Reason = %q, want hourly cap", d.Reason)
	}
	advance(time.Hour - 30*time.Second + time.Second)
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("send after oldest expired should pass: %+v", d)
	}
}

func TestAllowSend_NonContactHourlyCapIndependentOfContacts(t *testing.T) {
	cfg := testConfig() // non-contact cap 2/hr, global cap 3/hr
	l, advance := newTestLimiter(t, cfg)

	if !l.AllowSend(false).Allowed {
		t.Fatal("nc send 1 should pass")
	}
	advance(20 * time.Second)
	if !l.AllowSend(false).Allowed {
		t.Fatal("nc send 2 should pass")
	}
	advance(20 * time.Second)
	d := l.AllowSend(false)
	if d.Allowed {
		t.Fatal("nc send 3 should hit the non-contact hourly cap")
	}
	if d.Reason != "hourly non-contact send cap reached" {
		t.Errorf("Reason = %q, want non-contact cap", d.Reason)
	}
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("contact send should still pass with global room: %+v", d)
	}
}

func TestAllowSend_DeniedDoesNotConsumeBudget(t *testing.T) {
	l, advance := newTestLimiter(t, testConfig())

	if !l.AllowSend(true).Allowed {
		t.Fatal("send 1 should pass")
	}
	for i := 0; i < 5; i++ {
		if l.AllowSend(true).Allowed {
			t.Fatal("send during interval should be blocked")
		}
	}
	advance(10 * time.Second)
	if !l.AllowSend(true).Allowed {
		t.Fatal("send 2 should pass")
	}
	advance(10 * time.Second)
	if !l.AllowSend(true).Allowed {
		t.Fatal("send 3 should pass (denials must not have consumed budget)")
	}
}

func TestAllowUsync_BurstGuard(t *testing.T) {
	l, advance := newTestLimiter(t, testConfig()) // 2/min

	if !l.AllowUsync().Allowed {
		t.Fatal("usync 1 should pass")
	}
	if !l.AllowUsync().Allowed {
		t.Fatal("usync 2 should pass")
	}
	d := l.AllowUsync()
	if d.Allowed {
		t.Fatal("usync 3 within the minute should be blocked")
	}
	if d.Reason != "usync burst guard (is_on_whatsapp) reached" {
		t.Errorf("Reason = %q, want usync burst guard", d.Reason)
	}
	advance(time.Minute + time.Second)
	if !l.AllowUsync().Allowed {
		t.Fatal("usync after a minute should pass")
	}
}

func TestBypassContext(t *testing.T) {
	ctx := context.Background()
	if BypassFromContext(ctx) {
		t.Fatal("plain context should not carry bypass")
	}
	if !BypassFromContext(WithBypass(ctx)) {
		t.Fatal("WithBypass context should report bypass")
	}
}

func TestPruneBefore(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	in := []time.Time{
		base,
		base.Add(1 * time.Minute),
		base.Add(2 * time.Minute),
	}
	out := pruneBefore(append([]time.Time(nil), in...), base.Add(90*time.Second))
	if len(out) != 1 {
		t.Fatalf("expected 1 survivor, got %d", len(out))
	}
	if !out[0].Equal(base.Add(2 * time.Minute)) {
		t.Errorf("survivor = %s, want base+2m", out[0])
	}
}

func TestConcurrentSends(t *testing.T) {
	l, _ := newTestLimiter(t, testConfig())

	const n = 50
	results := make(chan bool, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			results <- bool(l.AllowSend(true).Allowed)
		}()
	}
	close(start)

	allowed := 0
	for i := 0; i < n; i++ {
		if <-results {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("expected exactly 1 send admitted at a frozen clock, got %d", allowed)
	}
}
