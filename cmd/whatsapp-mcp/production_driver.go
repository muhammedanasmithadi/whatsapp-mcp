package main

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// pairingState is the explicit pairing lifecycle state for productionDriver.
type pairingState int

const (
	pairingStateIdle pairingState = iota
	pairingStatePairing
)

// handlerInstallState tracks whether the logged-out handler has been installed.
type handlerInstallState int

const (
	handlerNotInstalled handlerInstallState = iota
	handlerInstalled
)

// productionDriver adapts *client.Client to the daemon.pairDriver interface.
// It does not install any internal state — the Client itself owns all
// whatsmeow-facing state.
type productionDriver struct {
	c *client.Client

	// pairingMu guards pairingState and pairingAttempts.
	pairingMu       sync.Mutex
	pairing         pairingState
	pairingAttempts int

	// handlerState ensures the logged-out event handler is installed only once.
	handlerState handlerInstallState
}

func newProductionDriver(c *client.Client) *productionDriver {
	return &productionDriver{c: c}
}

func (p *productionDriver) IsLoggedIn() bool { return p.c.IsLoggedIn() }

func (p *productionDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func()) error {
	p.pairingMu.Lock()
	if p.pairing == pairingStatePairing {
		p.pairingMu.Unlock()
		// A pairing flow is already in progress; don't start a second one.
		return nil
	}
	p.pairing = pairingStatePairing
	p.pairingAttempts++
	attempts := p.pairingAttempts
	p.pairingMu.Unlock()

	defer func() {
		p.pairingMu.Lock()
		p.pairing = pairingStateIdle
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

	success := make(chan struct{})
	go func() {
		defer close(success)
		for item := range qrCh {
			switch item.Event {
			case "code":
				onQR(item.Code)
			case "success":
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

	// Wait for the pairing outcome: the QR channel closes on success or on
	// timeout/expiry, so a closed channel with no success event means retry.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-success:
		return nil
	case <-time.After(500 * time.Millisecond):
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
	if p.handlerState != handlerInstalled {
		p.c.AddLoggedOutHandler(func(_ *events.LoggedOut) { onLoggedOut() })
		p.c.StartEventHandler()
		p.handlerState = handlerInstalled
	}
	if p.c.IsConnected() {
		// The pairing flow already connected this client; just install
		// the post-pair handlers and return.
		go p.syncAppState()
		return nil
	}
	if err := p.c.Connect(ctx, client.ConnectOpts{}); err != nil {
		return err
	}
	go p.syncAppState()
	return nil
}

// syncAppState repairs the app-state cache in the background. A freshly
// linked device has no app-state snapshot, and every mutation that writes one
// (pins, stars, status privacy, archive, mute) fails until the snapshot
// arrives. whatsmeow never fetches it on its own, so the bridge asks for the
// patches it writes. onlyIfNotSynced keeps a boot on an already-synced device
// cheap.
func (p *productionDriver) syncAppState() {
	syncCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, patch := range []appstate.WAPatchName{appstate.WAPatchRegularLow, appstate.WAPatchCriticalUnblockLow} {
		if err := p.c.WA().FetchAppState(syncCtx, patch, false, true); err != nil {
			p.c.WA().Log.Warnf("app-state sync for %s: %v", patch, err)
			continue
		}
		p.c.WA().Log.Infof("app-state synced: %s", patch)
	}
}

func (p *productionDriver) Logout(ctx context.Context) error { return p.c.Logout(ctx) }

// PairPhone lets the daemon's /pair/code endpoint trigger the WhatsApp
// "link with phone number" flow on the underlying client.
func (p *productionDriver) PairPhone(ctx context.Context, phone string) (string, error) {
	return p.c.PairPhone(ctx, phone)
}

// RequestHistorySync asks WhatsApp to backfill messages for a chat.
func (p *productionDriver) RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time, anchor client.AnchorMode, count int, useLID client.LIDMode) (string, error) {
	return p.c.RequestHistorySync(ctx, chatJID, fromTimestamp, anchor, count, useLID)
}

// ListChatJIDs returns all cached chat JIDs.
func (p *productionDriver) ListChatJIDs() ([]string, error) {
	return p.c.Store().ListChatJIDs()
}

// GetPendingScheduledMessages returns all pending scheduled messages.
func (p *productionDriver) GetPendingScheduledMessages(ctx context.Context) ([]store.ScheduledMessage, error) {
	return p.c.Store().GetPendingScheduledMessages(ctx)
}

// NextScheduledMessageTime returns the earliest pending scheduled time.
func (p *productionDriver) NextScheduledMessageTime() (time.Time, bool, error) {
	return p.c.Store().NextScheduledMessageTime()
}

// ClaimScheduledMessage atomically moves a row to 'sending'.
func (p *productionDriver) ClaimScheduledMessage(id string) (bool, error) {
	return p.c.Store().ClaimScheduledMessage(id)
}

// MarkMessageSent marks a scheduled message as sent.
func (p *productionDriver) MarkMessageSent(id, messageID string, sentAt time.Time) error {
	return p.c.Store().MarkMessageSent(id, messageID, sentAt)
}

// MarkMessageFailed marks a scheduled message as failed.
func (p *productionDriver) MarkMessageFailed(id, reason string) error {
	return p.c.Store().MarkMessageFailed(id, reason)
}

// SendMessage sends a WhatsApp message.
func (p *productionDriver) SendScheduledMessage(ctx context.Context, chatJID, content, mediaPath string) (*client.SendResult, error) {
	var r client.SendResult
	if mediaPath != "" {
		r = p.c.SendMediaWithOptions(ctx, client.SendMediaOptions{Recipient: chatJID, Caption: content, MediaPath: mediaPath})
	} else {
		r = p.c.Send(ctx, chatJID, content)
	}
	return &r, nil
}

func (p *productionDriver) RequeueScheduledMessage(id string, attempts int, nextAttempt time.Time) error {
	return p.c.Store().RequeueScheduledMessage(id, attempts, nextAttempt)
}

func (p *productionDriver) Download(ctx context.Context, messageID, chatJID string) client.DownloadResult {
	return p.c.Download(ctx, messageID, chatJID, "")
}

func (p *productionDriver) SendMessage(ctx context.Context, chatJID string, content string) (*client.SendResult, error) {
	r := p.c.Send(ctx, chatJID, content)
	return &r, nil
}

func (p *productionDriver) Disconnect() { p.c.Disconnect() }
