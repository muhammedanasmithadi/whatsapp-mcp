package daemon

import (
	"sync"
)

type PairState string

const (
	PairStateUnpaired   PairState = "unpaired"
	PairStateAwaitingQR PairState = "awaiting_qr"
	PairStatePaired     PairState = "paired"
	PairStateError      PairState = "error"
)

type PairEvent struct {
	State  PairState `json:"state"`
	QR     string    `json:"qr,omitempty"`
	Paired bool      `json:"paired"`
	Detail string    `json:"detail,omitempty"`
}

type PairCache struct {
	mu          sync.RWMutex
	qr          string
	state       PairState
	detail      string
	subscribers map[chan PairEvent]struct{}
}

func NewPairCache() *PairCache {
	return &PairCache{
		state:       PairStateUnpaired,
		subscribers: make(map[chan PairEvent]struct{}),
	}
}

func (c *PairCache) snapshot() PairEvent {
	return PairEvent{
		State:  c.state,
		QR:     c.qr,
		Paired: c.state == PairStatePaired,
		Detail: c.detail,
	}
}

func (c *PairCache) broadcast() {
	ev := c.snapshot()
	for ch := range c.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (c *PairCache) SetQR(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = code
	c.state = PairStateAwaitingQR
	c.detail = "Scan with WhatsApp -> Linked devices"
	c.broadcast()
}

func (c *PairCache) SetPaired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.state = PairStatePaired
	c.detail = "Paired"
	c.broadcast()
}

func (c *PairCache) SetError(detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = PairStateError
	c.detail = detail
	c.broadcast()
}

func (c *PairCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.state = PairStateUnpaired
	c.detail = "Reset"
	c.broadcast()
}

func (c *PairCache) Paired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state == PairStatePaired
}

func (c *PairCache) QR() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qr
}

func (c *PairCache) State() PairState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *PairCache) Snapshot() PairEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot()
}

func (c *PairCache) Subscribe() <-chan PairEvent {
	ch := make(chan PairEvent, 16)
	c.mu.Lock()
	c.subscribers[ch] = struct{}{}
	ch <- c.snapshot()
	c.mu.Unlock()
	return ch
}

func (c *PairCache) Unsubscribe(ch <-chan PairEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.subscribers {
		if k == ch {
			delete(c.subscribers, k)
			close(k)
			return
		}
	}
}
