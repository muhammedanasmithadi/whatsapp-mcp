package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"github.com/sealjay/mcp-whatsapp/internal/ratelimit"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// MarkRead marks messageIDs as read for the given chat. senderJID is the
// original sender (required by WhatsApp for group reads); empty string is
// treated as the chat JID itself.
func (c *Client) MarkRead(ctx context.Context, chatJID string, messageIDs []string, senderJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if len(messageIDs) == 0 {
		return errors.New("no message IDs specified")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %w", err)
		}
	}

	ids := make([]types.MessageID, len(messageIDs))
	for i, id := range messageIDs {
		ids[i] = types.MessageID(id)
	}

	return c.wa.MarkRead(ctx, ids, time.Now(), chat, sender)
}

// MarkChatRead acks recent incoming messages in a chat so the phone stops
// showing unread badges. Returns the number of messages acked.
func (c *Client) MarkChatRead(ctx context.Context, chatJID string, limit int) (int, error) {
	if !c.wa.IsConnected() {
		return 0, errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return 0, fmt.Errorf("invalid chat JID: %w", err)
	}
	rawIDs, rawSenders, err := c.store.RecentIncomingMessages(ctx, chatJID, limit)
	if err != nil {
		return 0, fmt.Errorf("load recent messages: %w", err)
	}
	if len(rawIDs) == 0 {
		return 0, nil
	}
	// Group by sender JID: MarkRead is batched per (chat, sender).
	bySender := map[string][]types.MessageID{}
	for i, id := range rawIDs {
		bySender[rawSenders[i]] = append(bySender[rawSenders[i]], types.MessageID(id))
	}
	count := 0
	for senderRaw, ids := range bySender {
		var sender types.JID
		if senderRaw != "" {
			parsed, err := types.ParseJID(senderRaw)
			if err == nil {
				sender = parsed
			} else {
				// Sender stored as bare phone number; fall back to default user server.
				sender = types.JID{User: senderRaw, Server: types.DefaultUserServer}
			}
		}
		if err := c.wa.MarkRead(ctx, ids, time.Now(), chat, sender); err != nil {
			return count, fmt.Errorf("mark read (sender=%s): %w", senderRaw, err)
		}
		count += len(ids)
	}
	return count, nil
}

// checkActionRate applies the daemon-side rate limit to non-message WhatsApp
// actions (reactions, replies, edits, revokes, poll votes, typing). It shares
// the same limiter and windows as checkSendRate so an LLM cannot bypass the
// send cap by switching to reactions.
func (c *Client) checkActionRate(ctx context.Context, chatJID types.JID) error {
	if c.limiter == nil {
		return nil
	}
	if ratelimit.BypassFromContext(ctx) {
		c.log.Warnf("RATE LIMIT OVERRIDE: action to %s bypassed the limiter via X-Rate-Limit-Override", c.redactor.JID(chatJID.String()))
		return nil
	}
	isContact := c.IsKnownContact(chatJID)
	if d := c.limiter.AllowSend(isContact); !d.Allowed {
		c.log.Warnf("Rate limited: action to %s denied (%s); retry in %s", c.redactor.JID(chatJID.String()), d.Reason, d.RetryAfter.Round(time.Second))
		return fmt.Errorf(
			"rate limited: %s — retry in %s (set the X-Rate-Limit-Override header to bypass; see the daemon rate-limit docs)",
			d.Reason, d.RetryAfter.Round(time.Second))
	}
	return nil
}

// SendReaction adds (or clears, if emoji is empty) a reaction to a message.
// Rate-limited and cached like other outbound actions.
func (c *Client) SendReaction(ctx context.Context, chatJID, messageID, senderJID, emoji string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkActionRate(ctx, chat); err != nil {
		return err
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %w", err)
		}
	}

	reaction := c.wa.BuildReaction(chat, sender, types.MessageID(messageID), emoji)
	resp, err := c.wa.SendMessage(ctx, chat, reaction)
	if err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}

	// Cache the reaction so local history reflects it.
	c.persistReaction(ctx, chat, resp.ID, messageID, emoji)
	return nil
}

// persistReaction stores an outbound reaction in the message cache so it
// appears in list_messages / get_message_context alongside other history.
func (c *Client) persistReaction(ctx context.Context, chat types.JID, msgID, targetMessageID, emoji string) {
	chatJID := chat.String()
	if !strings.HasSuffix(chatJID, "@g.us") {
		chatJID = c.store.ResolveLIDToJID(chatJID)
	}

	chatName := c.resolveChatNameFallback(ctx, chat, "")

	now := time.Now()
	if err := c.store.StoreChat(chatJID, chatName, now); err != nil {
		c.log.Warnf("Failed to store chat for reaction: %v", err)
	}

	ourJID := ""
	if c.wa.Store.ID != nil {
		ourJID = c.wa.Store.ID.User
	}

	content := "[reaction] " + emoji
	if emoji == "" {
		content = "[reaction cleared]"
	}

	if err := c.store.StoreMessage(ctx, store.Message{
		ID:         msgID,
		ChatJID:    chatJID,
		Sender:     ourJID,
		Content:    content,
		Timestamp:  now,
		IsFromMe:   true,
	}, nil, nil, nil, 0); err != nil {
		c.log.Warnf("Failed to store reaction: %v", err)
		return
	}
	c.log.Infof("[%s] -> %s [reaction %s]: %s", now.Format("2006-01-02 15:04:05"), c.redactor.JID(chatJID), c.redactor.MsgID(msgID), content)
}

// SendReply sends a text reply that quotes targetMessageID from chatJID.
// Rate-limited and cached like other outbound actions.
func (c *Client) SendReply(ctx context.Context, chatJID, targetMessageID, targetSenderJID, body string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkActionRate(ctx, chat); err != nil {
		return err
	}

	participant := ""
	if targetSenderJID != "" {
		// ContextInfo.Participant expects a JID string for group quotes.
		if sender, perr := types.ParseJID(targetSenderJID); perr == nil {
			participant = sender.ToNonAD().String()
		} else {
			participant = targetSenderJID
		}
	}

	ctxInfo := &waProto.ContextInfo{
		StanzaID: proto.String(targetMessageID),
	}
	if participant != "" {
		ctxInfo.Participant = proto.String(participant)
	}

	msg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(body),
			ContextInfo: ctxInfo,
		},
		MessageContextInfo: c.ephemeralContextInfo(ctx, chat),
	}

	resp, err := c.wa.SendMessage(ctx, chat, msg)
	if err != nil {
		return fmt.Errorf("send reply: %w", err)
	}

	// Cache the reply so local history stays complete.
	c.persistSent(ctx, chat, resp.ID, body, "", msg)
	return nil
}

// EditMessage edits a previously-sent message. The new body becomes the new
// conversation text. Rate-limited.
func (c *Client) EditMessage(ctx context.Context, chatJID, messageID, newBody string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkActionRate(ctx, chat); err != nil {
		return err
	}

	newContent := &waProto.Message{
		Conversation: proto.String(newBody),
	}
	edit := c.wa.BuildEdit(chat, types.MessageID(messageID), newContent)
	if _, err := c.wa.SendMessage(ctx, chat, edit); err != nil {
		return fmt.Errorf("send edit: %w", err)
	}
	return nil
}

// DeleteMessage revokes a message for everyone. senderJID is required only
// when revoking someone else's message as a group admin; for your own
// messages pass "". Rate-limited because this is destructive.
func (c *Client) DeleteMessage(ctx context.Context, chatJID, messageID, senderJID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkActionRate(ctx, chat); err != nil {
		return err
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %w", err)
		}
	}

	revoke := c.wa.BuildRevoke(chat, sender, types.MessageID(messageID))
	if _, err := c.wa.SendMessage(ctx, chat, revoke); err != nil {
		return fmt.Errorf("send revoke: %w", err)
	}
	return nil
}

// SendTyping sets chat presence to "composing" when active is true, or
// "paused" when false. kind may be "audio" to indicate a voice recording.
// Rate-limited because presence spam can trigger abuse flags.
func (c *Client) SendTyping(ctx context.Context, chatJID string, active bool, kind string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkActionRate(ctx, chat); err != nil {
		return err
	}

	state := types.ChatPresencePaused
	if active {
		state = types.ChatPresenceComposing
	}
	media := types.ChatPresenceMediaText
	if strings.EqualFold(kind, "audio") {
		media = types.ChatPresenceMediaAudio
	}

	return c.wa.SendChatPresence(ctx, chat, state, media)
}

// IsOnWhatsApp checks whether each phone number (digits only) is registered
// on WhatsApp. Returns a map keyed by the input phone string.
func (c *Client) IsOnWhatsApp(ctx context.Context, phones []string) (map[string]bool, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	if len(phones) == 0 {
		return map[string]bool{}, nil
	}

	// usync burst guard — WhatsApp rate-limits contact-discovery (usync)
	// harder than sends, and a parallel fanout that verifies each recipient
	// trips it first. One IsOnWhatsApp call is one usync regardless of batch
	// size. The operator override bypasses it, same as sends.
	if c.limiter != nil && !ratelimit.BypassFromContext(ctx) {
		if d := c.limiter.AllowUsync(); !d.Allowed {
			return nil, fmt.Errorf("rate limited: %s — retry in %s (set the X-Rate-Limit-Override header to bypass)",
				d.Reason, d.RetryAfter.Round(time.Second))
		}
	}

	// WhatsApp expects a leading '+' on phone queries.
	queries := make([]string, len(phones))
	for i, p := range phones {
		if strings.HasPrefix(p, "+") {
			queries[i] = p
		} else {
			queries[i] = "+" + p
		}
	}

	resp, err := c.wa.IsOnWhatsApp(ctx, queries)
	if err != nil {
		return nil, fmt.Errorf("is on whatsapp: %w", err)
	}

	result := make(map[string]bool, len(phones))
	// Pre-seed every input to false so callers always get an entry.
	for _, p := range phones {
		result[p] = false
	}
	for _, r := range resp {
		// Match by the query string used, then fall back to JID.User.
		key := strings.TrimPrefix(r.Query, "+")
		if _, ok := result[key]; ok {
			result[key] = r.IsIn
			continue
		}
		if _, ok := result[r.JID.User]; ok {
			result[r.JID.User] = r.IsIn
		}
	}
	return result, nil
}
