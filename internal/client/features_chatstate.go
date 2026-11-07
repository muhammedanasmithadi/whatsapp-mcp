package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

// SetChatMuted mutes or unmutes a chat. A zero duration while muting means
// "mute forever"; WhatsApp Web offers 8 hours, 1 week, and always.
func (c *Client) SetChatMuted(ctx context.Context, recipient string, muted bool, duration time.Duration) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(recipient)
	if err != nil {
		return err
	}
	if duration < 0 {
		return errors.New("mute duration cannot be negative")
	}
	return c.wa.SendAppState(ctx, appstate.BuildMute(jid.ToNonAD(), muted, duration))
}

// SetChatPinned pins or unpins a chat in WhatsApp's chat list.
func (c *Client) SetChatPinned(ctx context.Context, recipient string, pinned bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(recipient)
	if err != nil {
		return err
	}
	return c.wa.SendAppState(ctx, appstate.BuildPin(jid.ToNonAD(), pinned))
}

// StarMessage stars or unstars a cached message. WhatsApp stores stars in app
// state, so the effect syncs to the phone and other linked devices.
func (c *Client) StarMessage(ctx context.Context, chat string, messageID string, starred bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if messageID == "" {
		return errors.New("message_id required")
	}
	chatJID, err := parseRecipient(chat)
	if err != nil {
		return err
	}
	chatJID = chatJID.ToNonAD()
	cached, err := c.store.GetMessage(ctx, messageID, chatJID.String())
	if err != nil {
		return fmt.Errorf("look up message: %w", err)
	}
	if cached == nil {
		return fmt.Errorf("message %s is not cached in %s; sync the chat first", messageID, chatJID)
	}

	sender := types.NewJID(cached.Sender, types.DefaultUserServer)
	if cached.IsFromMe || cached.Sender == "" {
		if c.wa.Store.ID == nil {
			return errors.New("own JID unavailable")
		}
		sender = c.wa.Store.ID.ToNonAD()
	}
	return c.wa.SendAppState(ctx, appstate.BuildStar(chatJID, sender, types.MessageID(messageID), cached.IsFromMe, starred))
}

// SetDisappearingTimer sets or clears a chat's disappearing-message timer.
// Zero disables disappearing messages. WhatsApp accepts 24h, 7d, and 90d in
// its own UI; custom positive durations are sent as-is.
func (c *Client) SetDisappearingTimer(ctx context.Context, recipient string, duration time.Duration) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if duration < 0 {
		return errors.New("disappearing timer cannot be negative")
	}
	jid, err := parseRecipient(recipient)
	if err != nil {
		return err
	}
	return c.wa.SetDisappearingTimer(ctx, jid.ToNonAD(), duration, time.Now())
}

// PinMessage pins or unpins a specific message in a chat. The message must be
// cached locally (use list_messages first). Pinning syncs to the phone and
// other linked devices through WhatsApp app state.
func (c *Client) PinMessage(ctx context.Context, chatJID, messageID string, pinned bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if messageID == "" {
		return errors.New("message_id required")
	}
	jid, err := parseRecipient(chatJID)
	if err != nil {
		return err
	}
	jid = jid.ToNonAD()
	cached, err := c.store.GetMessage(ctx, messageID, jid.String())
	if err != nil {
		return fmt.Errorf("look up message: %w", err)
	}
	if cached == nil {
		return fmt.Errorf("message %s is not cached in %s; sync the chat first", messageID, jid)
	}
	msgJID := types.NewJID(messageID, jid.Server)
	return c.wa.SendAppState(ctx, appstate.BuildPin(msgJID.ToNonAD(), pinned))
}
