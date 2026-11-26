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
// chatRowJID returns the key the chats table uses for a JID: the phone form
// for LID-addressed chats, and the JID itself for groups.
func (c *Client) chatRowJID(jid types.JID) string {
	if jid.Server == types.HiddenUserServer {
		return c.store.ResolveLIDToJID(jid.String())
	}
	return jid.String()
}

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
	if err := c.wa.SendAppState(ctx, appstate.BuildMute(jid.ToNonAD(), muted, duration)); err != nil {
		return err
	}
	// WhatsApp accepted the mute. Record it in our own chat row so get_chat and
	// list_chats report it immediately instead of waiting for an app-state sync
	// to echo the value back.
	until := time.Time{}
	if muted && duration > 0 {
		until = time.Now().Add(duration)
	}
	if err := c.store.SetChatMuteState(c.chatRowJID(jid), until); err != nil {
		c.log.Warnf("Record mute state for %s: %v", jid, err)
	}
	return nil
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
	if err := c.wa.SendAppState(ctx, appstate.BuildPin(jid.ToNonAD(), pinned)); err != nil {
		return err
	}
	if err := c.store.SetChatPinnedState(c.chatRowJID(jid), pinned); err != nil {
		c.log.Warnf("Record pin state for %s: %v", jid, err)
	}
	return nil
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
	if err := c.wa.SendAppState(ctx, appstate.BuildStar(chatJID, sender, types.MessageID(messageID), cached.IsFromMe, starred)); err != nil {
		return err
	}
	// whatsmeow keeps no app-state values, so mirror the flag we just asked
	// WhatsApp to accept; otherwise a star has no read-back at all.
	if err := c.store.SetMessageStarred(ctx, messageID, c.chatRowJID(chatJID), starred); err != nil {
		c.log.Warnf("Record star for %s: %v", messageID, err)
	}
	return nil
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

// ArchiveChat archives a chat in WhatsApp's app state so the phone and other
// linked devices see it, and records the flag locally so get_chat agrees.
func (c *Client) ArchiveChat(ctx context.Context, chatJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(chatJID)
	if err != nil {
		return err
	}
	jid = jid.ToNonAD()
	rowJID := c.chatRowJID(jid)
	last := c.lastChatMessageTime(ctx, rowJID)
	if err := c.wa.SendAppState(ctx, appstate.BuildArchive(jid, true, last, nil)); err != nil {
		return fmt.Errorf("archive chat: %w", err)
	}
	if err := c.store.ArchiveChat(rowJID); err != nil {
		c.log.Warnf("Record archive for %s: %v", rowJID, err)
	}
	return nil
}

// UnarchiveChat reverses ArchiveChat on both WhatsApp and the local row.
func (c *Client) UnarchiveChat(ctx context.Context, chatJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(chatJID)
	if err != nil {
		return err
	}
	jid = jid.ToNonAD()
	rowJID := c.chatRowJID(jid)
	last := c.lastChatMessageTime(ctx, rowJID)
	if err := c.wa.SendAppState(ctx, appstate.BuildArchive(jid, false, last, nil)); err != nil {
		return fmt.Errorf("unarchive chat: %w", err)
	}
	if err := c.store.UnarchiveChat(rowJID); err != nil {
		c.log.Warnf("Record unarchive for %s: %v", rowJID, err)
	}
	return nil
}

// DeleteChat removes a chat from WhatsApp's chat list and clears this bridge's
// cached rows for it. deleteMedia asks the phone to drop the media files too.
// The message history disappears from this bridge immediately; WhatsApp keeps
// it until its own retention window clears on the phone.
func (c *Client) DeleteChat(ctx context.Context, chatJID string, deleteMedia bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(chatJID)
	if err != nil {
		return err
	}
	jid = jid.ToNonAD()
	rowJID := c.chatRowJID(jid)
	last := c.lastChatMessageTime(ctx, rowJID)
	if err := c.wa.SendAppState(ctx, appstate.BuildDeleteChat(jid, last, nil, deleteMedia)); err != nil {
		return fmt.Errorf("delete chat: %w", err)
	}
	if err := c.store.DeleteChat(ctx, rowJID); err != nil {
		c.log.Warnf("Clear cached chat %s: %v", rowJID, err)
	}
	return nil
}

// lastChatMessageTime is the range anchor WhatsApp's chat actions expect. A
// zero time makes whatsmeow use now.
func (c *Client) lastChatMessageTime(ctx context.Context, rowJID string) time.Time {
	chat, err := c.store.GetChat(ctx, rowJID, false)
	if err != nil || chat == nil {
		return time.Time{}
	}
	return chat.LastMessageTime
}
