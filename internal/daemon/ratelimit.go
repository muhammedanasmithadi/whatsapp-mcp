package daemon

import (
	"sync"
	"time"
)

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Limiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	clock  clock
	tokens float64
	last   time.Time
}

func NewLimiter(rate float64, burst int) *Limiter {
	return &Limiter{
		rate:  rate,
		burst: float64(burst),
		clock: realClock{},
	}
}

func newLimiterWithClock(rate float64, burst int, c clock) *Limiter {
	l := NewLimiter(rate, burst)
	l.clock = c
	return l
}

func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if l.last.IsZero() {
		l.tokens = l.burst
		l.last = now
	}

	elapsed := now.Sub(l.last).Seconds()
	l.tokens += elapsed * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now

	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
