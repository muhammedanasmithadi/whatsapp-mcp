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

type pairingState int

const (
	pairingStateIdle pairingState = iota
	pairingStatePairing
)

type productionDriver struct {
	c *client.Client

	pairingMu       sync.Mutex
	pairing         pairingState
	pairingAttempts int

	handlerInstalled bool
}

func newProductionDriver(c *client.Client) *productionDriver {
	return &productionDriver{c: c}
}

func (p *productionDriver) IsLoggedIn() bool { return p.c.IsLoggedIn() }

func (p *productionDriver) StartPairing(ctx context.Context, onQR func(string), onSuccess func()) error {
	p.pairingMu.Lock()
	if p.pairing == pairingStatePairing {
		p.pairingMu.Unlock()
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
				return
			}
		}
	}()
	// Must call Connect AFTER QRChannel per whatsmeow docs.
	if err := p.c.Connect(ctx, client.ConnectOpts{AllowUnpaired: true}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-success:
		return nil
	case <-time.After(500 * time.Millisecond):
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff):
	}

	return p.StartPairing(ctx, onQR, onSuccess)
}

func (p *productionDriver) Connect(ctx context.Context, onLoggedOut func()) error {
	if !p.handlerInstalled {
		p.c.AddLoggedOutHandler(func(_ *events.LoggedOut) { onLoggedOut() })
		p.c.StartEventHandler()
		p.handlerInstalled = true
	}
	if p.c.IsConnected() {
		go p.syncAppState()
		return nil
	}
	if err := p.c.Connect(ctx, client.ConnectOpts{}); err != nil {
		return err
	}
	go p.syncAppState()
	return nil
}

// linked device has no app-state snapshot, and every mutation that writes one
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

func (p *productionDriver) PairPhone(ctx context.Context, phone string) (string, error) {
	return p.c.PairPhone(ctx, phone)
}

func (p *productionDriver) RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time, anchor client.AnchorMode, count int, useLID client.LIDMode) (string, error) {
	return p.c.RequestHistorySync(ctx, chatJID, fromTimestamp, anchor, count, useLID)
}

func (p *productionDriver) ListChatJIDs() ([]string, error) {
	return p.c.Store().ListChatJIDs()
}

func (p *productionDriver) GetPendingScheduledMessages(ctx context.Context) ([]store.ScheduledMessage, error) {
	return p.c.Store().GetPendingScheduledMessages(ctx)
}

func (p *productionDriver) NextScheduledMessageTime() (time.Time, bool, error) {
	return p.c.Store().NextScheduledMessageTime()
}

func (p *productionDriver) ClaimScheduledMessage(id string) (bool, error) {
	return p.c.Store().ClaimScheduledMessage(id)
}

func (p *productionDriver) MarkMessageSent(id, messageID string, sentAt time.Time) error {
	return p.c.Store().MarkMessageSent(id, messageID, sentAt)
}

func (p *productionDriver) MarkMessageFailed(id, reason string) error {
	return p.c.Store().MarkMessageFailed(id, reason)
}

func (p *productionDriver) SendScheduledMessage(ctx context.Context, chatJID, content, mediaPath string) (*client.SendResult, error) {
	var r client.SendResult
	if mediaPath != "" {
		r = p.c.SendMediaWithOptions(ctx, client.SendMediaOptions{Recipient: chatJID, Caption: content, MediaPath: mediaPath})
	} else {
		r = p.c.Send(ctx, chatJID, content)
	}
	return &r, nil
}

func (p *productionDriver) IsConnected() bool { return p.c.IsConnected() }

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
