package ratelimit

import (
	"context"
	"sync"
	"time"
)

type Config struct {
	MinSendInterval       time.Duration
	MaxSendsPerHour       int
	NonContactMinInterval time.Duration
	NonContactMaxPerHour  int
	MaxUsyncPerMinute     int
}

func DefaultConfig() Config {
	return Config{
		MinSendInterval:       10 * time.Second,
		MaxSendsPerHour:       200,
		NonContactMinInterval: 10 * time.Second,
		NonContactMaxPerHour:  60,
		MaxUsyncPerMinute:     30,
	}
}

type AllowDecision bool

const (
	AllowDenied AllowDecision = false
)

type Decision struct {
	Allowed    AllowDecision
	RetryAfter time.Duration
	Reason     string
}

type Limiter struct {
	mu    sync.Mutex
	cfg   Config
	clock func() time.Time

	sends           []time.Time // all sends within the trailing hour
	nonContactSends []time.Time // non-contact sends within the trailing hour
	usyncs          []time.Time // usync lookups within the trailing minute
	lastSend        time.Time   // zero when no send has been recorded yet
}

func New(cfg Config) *Limiter {
	return &Limiter{cfg: cfg, clock: time.Now}
}

func (l *Limiter) AllowSend(isContact bool) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()

	hourAgo := now.Add(-time.Hour)
	l.sends = pruneBefore(l.sends, hourAgo)
	l.nonContactSends = pruneBefore(l.nonContactSends, hourAgo)

	minInterval := l.cfg.MinSendInterval
	if !isContact && l.cfg.NonContactMinInterval > minInterval {
		minInterval = l.cfg.NonContactMinInterval
	}
	if !l.lastSend.IsZero() {
		if elapsed := now.Sub(l.lastSend); elapsed < minInterval {
			return Decision{Allowed: AllowDenied, RetryAfter: minInterval - elapsed, Reason: intervalReason(isContact)}
		}
	}

	if l.cfg.MaxSendsPerHour > 0 && len(l.sends) >= l.cfg.MaxSendsPerHour {
		return Decision{Allowed: AllowDenied, RetryAfter: untilOldestExpires(l.sends, now, time.Hour), Reason: "hourly send cap reached"}
	}
	if !isContact && l.cfg.NonContactMaxPerHour > 0 && len(l.nonContactSends) >= l.cfg.NonContactMaxPerHour {
		return Decision{Allowed: AllowDenied, RetryAfter: untilOldestExpires(l.nonContactSends, now, time.Hour), Reason: "hourly non-contact send cap reached"}
	}

	l.sends = append(l.sends, now)
	if !isContact {
		l.nonContactSends = append(l.nonContactSends, now)
	}
	l.lastSend = now
	return Decision{Allowed: true}
}

func (l *Limiter) RefundSend(isContact bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sends = pruneBefore(l.sends, l.clock().Add(-time.Hour))
	if len(l.sends) > 0 {
		l.sends = l.sends[:len(l.sends)-1]
	}
	if !isContact && len(l.nonContactSends) > 0 {
		l.nonContactSends = l.nonContactSends[:len(l.nonContactSends)-1]
	}
	if len(l.sends) == 0 {
		l.lastSend = time.Time{}
	}
}

func (l *Limiter) AllowUsync() Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()

	l.usyncs = pruneBefore(l.usyncs, now.Add(-time.Minute))
	if l.cfg.MaxUsyncPerMinute > 0 && len(l.usyncs) >= l.cfg.MaxUsyncPerMinute {
		return Decision{Allowed: AllowDenied, RetryAfter: untilOldestExpires(l.usyncs, now, time.Minute), Reason: "usync burst guard (is_on_whatsapp) reached"}
	}
	l.usyncs = append(l.usyncs, now)
	return Decision{Allowed: true}
}

func intervalReason(isContact bool) string {
	if isContact {
		return "minimum send interval not elapsed"
	}
	return "minimum non-contact send interval not elapsed"
}

func pruneBefore(s []time.Time, cutoff time.Time) []time.Time {
	out := s[:0]
	for _, t := range s {
		if !t.Before(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

func untilOldestExpires(window []time.Time, now time.Time, dur time.Duration) time.Duration {
	if len(window) == 0 {
		return 0
	}
	d := window[0].Add(dur).Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

type bypassKey struct{}

func WithBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassKey{}, true)
}

func BypassFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(bypassKey{}).(bool)
	return v
}
