package daemon

import (
	"sync"
)

// PairState is the coarse pairing state surfaced to UIs and SSE subscribers.
type PairState string

const (
	PairStateUnpaired   PairState = "unpaired"
	PairStateAwaitingQR PairState = "awaiting_qr"
	PairStatePaired     PairState = "paired"
	PairStateError      PairState = "error"
)

// PairEvent is a single state transition broadcast to SSE subscribers and
// the /pair page. QR carries the raw pairing payload when available.
type PairEvent struct {
	State  PairState `json:"state"`
	QR     string    `json:"qr,omitempty"`
	Paired bool      `json:"paired"`
	Detail string    `json:"detail,omitempty"`
}

// PairCache holds the latest QR code bytes emitted by whatsmeow's pairing
// channel and the current pairing state.
type PairCache struct {
	mu          sync.RWMutex
	qr          string
	state       PairState
	detail      string
	subscribers map[chan PairEvent]struct{}
}

// NewPairCache returns an empty cache in the "unpaired, no QR yet" state.
func NewPairCache() *PairCache {
	return &PairCache{
		state:       PairStateUnpaired,
		subscribers: make(map[chan PairEvent]struct{}),
	}
}

// snapshot returns a copy of the current state under the lock.
func (c *PairCache) snapshot() PairEvent {
	return PairEvent{
		State:  c.state,
		QR:     c.qr,
		Paired: c.state == PairStatePaired,
		Detail: c.detail,
	}
}

// broadcast sends the current snapshot to all subscribers. Caller must hold mu.
func (c *PairCache) broadcast() {
	ev := c.snapshot()
	for ch := range c.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SetQR stores the latest pairing code.
func (c *PairCache) SetQR(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = code
	c.state = PairStateAwaitingQR
	c.detail = "Scan with WhatsApp -> Linked devices"
	c.broadcast()
}

// SetPaired flips the paired state on and clears any pending QR.
func (c *PairCache) SetPaired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.state = PairStatePaired
	c.detail = "Paired"
	c.broadcast()
}

// SetError marks the pairing as failed with a human-readable detail.
func (c *PairCache) SetError(detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = PairStateError
	c.detail = detail
	c.broadcast()
}

// Reset drops to unpaired and clears any pending QR.
func (c *PairCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.state = PairStateUnpaired
	c.detail = "Reset"
	c.broadcast()
}

// Paired reports whether the device is currently paired.
func (c *PairCache) Paired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state == PairStatePaired
}

// QR returns the latest cached pairing code, or "" if none.
func (c *PairCache) QR() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qr
}

// State returns the current pairing state.
func (c *PairCache) State() PairState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Snapshot returns a copy of the current pairing state.
func (c *PairCache) Snapshot() PairEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot()
}

// Subscribe returns a channel that receives state transitions.
func (c *PairCache) Subscribe() <-chan PairEvent {
	ch := make(chan PairEvent, 16)
	c.mu.Lock()
	c.subscribers[ch] = struct{}{}
	ch <- c.snapshot()
	c.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
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