package main

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// productionDriver adapts *client.Client to the daemon.pairDriver interface.
// It does not install any internal state — the Client itself owns all
// whatsmeow-facing state.
type productionDriver struct {
	c *client.Client

	// pairingMu guards pairingActive and pairingAttempts.
	pairingMu        sync.Mutex
	pairingActive    bool
	pairingAttempts  int

	// handlerInstalled ensures the logged-out event handler is installed only once.
	handlerInstalled bool
}

func newProductionDriver(c *client.Client) *productionDriver {
	return &productionDriver{c: c}
}

func (p *productionDriver) IsLoggedIn() bool { return p.c.IsLoggedIn() }

func (p *productionDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func()) error {
	p.pairingMu.Lock()
	if p.pairingActive {
		p.pairingMu.Unlock()
		// A pairing flow is already in progress; don't start a second one.
		return nil
	}
	p.pairingActive = true
	p.pairingAttempts++
	attempts := p.pairingAttempts
	p.pairingMu.Unlock()

	defer func() {
		p.pairingMu.Lock()
		p.pairingActive = false
		p.pairingMu.Unlock()
	}()

	// Back off between retries so a repeatedly-expiring QR doesn't spin.
	backoff := time.Duration(attempts) * 2 * time.Second
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}

	qrCh, err := p.c.QRChannel(ctx)
	if err != nil {
		return err
	}

	success := false
	go func() {
		for item := range qrCh {
			switch item.Event {
			case "code":
				onQR(item.Code)
			case "success":
				success = true
				onSuccess()
				return
			case "timeout":
				// QR code scan timed out — channel will close; we'll retry below.
				return
			}
		}
	}()
	// Must call Connect AFTER QRChannel per whatsmeow docs.
	if err := p.c.Connect(ctx, client.ConnectOpts{AllowUnpaired: true}); err != nil {
		return err
	}

	// Wait briefly to let the success callback fire if pairing completes.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}

	if success {
		return nil
	}

	// Pairing didn't complete (QR expired, timed out, or channel closed).
	// Auto-retry with backoff unless the context is cancelled.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff):
	}

	// Recursive retry — depth is bounded by backoff cap and context cancellation.
	return p.StartPairing(ctx, onQR, onSuccess)
}

func (p *productionDriver) Connect(ctx context.Context, onLoggedOut func()) error {
	// Install the logged-out handler only once; retries of StartPairing
	// call Connect again and must not stack up handlers.
	if !p.handlerInstalled {
		p.c.AddLoggedOutHandler(func(_ *events.LoggedOut) { onLoggedOut() })
		p.c.StartEventHandler()
		p.handlerInstalled = true
	}
	if p.c.IsConnected() {
		// The pairing flow already connected this client; just install
		// the post-pair handlers and return.
		return nil
	}
	return p.c.Connect(ctx, client.ConnectOpts{})
}

func (p *productionDriver) Logout(ctx context.Context) error { return p.c.Logout(ctx) }

// PairPhone lets the daemon's /pair/code endpoint trigger the WhatsApp
// "link with phone number" flow on the underlying client.
func (p *productionDriver) PairPhone(ctx context.Context, phone string) (string, error) {
	return p.c.PairPhone(ctx, phone)
}

func (p *productionDriver) Disconnect() { p.c.Disconnect() }
