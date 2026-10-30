// Package daemon runs the long-lived whatsapp-mcp process: it owns the
// HTTP listener, mounts the MCP endpoint, serves the /pair UI, and drives
// the pairing state machine against a whatsmeow client.
package daemon

import (
	"sync"
)

// PairState is the coarse pairing state surfaced to UIs and SSE subscribers.
type PairState string

const (
	PairStateUnpaired   PairState = "unpaired"    // no session yet
	PairStateAwaitingQR PairState = "awaiting_qr" // QR (or phone code) available
	PairStatePaired     PairState = "paired"      // device linked
	PairStateError      PairState = "error"       // pairing failed
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
// channel and a flag indicating whether the device is currently paired.
// Safe for concurrent readers and a single writer (the pairing goroutine).
// It also fans state changes out to SSE subscribers for event-driven UX.
type PairCache struct {
	mu          sync.RWMutex
	qr          string
	paired      bool
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
		Paired: c.paired,
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
			// Slow subscriber: drop rather than block the writer.
		}
	}
}

// SetQR stores the latest pairing code. Clears the paired flag.
func (c *PairCache) SetQR(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = code
	c.paired = false
	c.state = PairStateAwaitingQR
	c.detail = "Scan with WhatsApp -> Linked devices"
	c.broadcast()
}

// SetPaired flips the paired flag on and clears any pending QR.
func (c *PairCache) SetPaired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.paired = true
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

// Reset drops the paired flag and clears any pending QR. Used when a
// user-driven re-pair starts.
func (c *PairCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = ""
	c.paired = false
	c.state = PairStateUnpaired
	c.detail = "Reset"
	c.broadcast()
}

// Paired reports whether the device is currently paired.
func (c *PairCache) Paired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paired
}

// QR returns the latest cached pairing code, or "" if none has been emitted
// since the last Reset or SetPaired.
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

// Subscribe returns a channel that receives state transitions. The caller
// must call Unsubscribe when done to avoid leaking the channel.
func (c *PairCache) Subscribe() <-chan PairEvent {
	ch := make(chan PairEvent, 16)
	c.mu.Lock()
	c.subscribers[ch] = struct{}{}
	// Send current snapshot immediately so late subscribers catch up.
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
