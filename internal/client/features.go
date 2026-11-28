package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/ratelimit"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

func (c *Client) MarkRead(ctx context.Context, chatJID string, messageIDs []string, senderJID string) error {
	if !c.socketConnected() {
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

	return c.markRead(ctx, ids, time.Now(), chat, sender)
}

func (c *Client) MarkChatRead(ctx context.Context, chatJID string, limit int) (int, error) {
	if !c.socketConnected() {
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
	bySender := map[string][]types.MessageID{}
	for i, id := range rawIDs {
		bySender[rawSenders[i]] = append(bySender[rawSenders[i]], types.MessageID(id))
	}
	senders := make([]string, 0, len(bySender))
	for senderRaw := range bySender {
		senders = append(senders, senderRaw)
	}
	sort.Strings(senders)

	count := 0
	var firstErr error
	for _, senderRaw := range senders {
		var sender types.JID
		if senderRaw != "" {
			parsed, err := types.ParseJID(senderRaw)
			if err == nil {
				sender = parsed
			} else {
				sender = types.JID{User: senderRaw, Server: types.DefaultUserServer}
			}
		}
		if err := c.markRead(ctx, bySender[senderRaw], time.Now(), chat, sender); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("mark read (sender=%s): %w", senderRaw, err)
			}
			continue
		}
		count += len(bySender[senderRaw])
	}
	return count, firstErr
}

func (c *Client) checkSendWindow(ctx context.Context, chatJID types.JID) error {
	if c.limiter == nil {
		return nil
	}
	if ratelimit.BypassFromContext(ctx) {
		c.log.Warnf("RATE LIMIT OVERRIDE: send to %s bypassed the limiter via X-Rate-Limit-Override", c.redactor.JID(chatJID.String()))
		return nil
	}
	isContact := c.IsKnownContact(chatJID)
	if d := c.limiter.AllowSend(isContact); !d.Allowed {
		c.log.Warnf("Rate limited: send to %s denied (%s); retry in %s", c.redactor.JID(chatJID.String()), d.Reason, d.RetryAfter.Round(time.Second))
		return fmt.Errorf(
			"rate limited: %s — retry in %s (set the X-Rate-Limit-Override header to bypass; see the daemon rate-limit docs)",
			d.Reason, d.RetryAfter.Round(time.Second))
	}
	return nil
}

func (c *Client) checkUsyncWindow(ctx context.Context) error {
	if c.limiter != nil && !ratelimit.BypassFromContext(ctx) {
		if d := c.limiter.AllowUsync(); !d.Allowed {
			return fmt.Errorf("rate limited: %s — retry in %s (set the X-Rate-Limit-Override header to bypass)",
				d.Reason, d.RetryAfter.Round(time.Second))
		}
	}
	return nil
}

func (c *Client) SendReaction(ctx context.Context, chatJID, messageID, senderJID, emoji string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := parseChatJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
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
	resp, err := c.sendMessage(ctx, chat, reaction)
	if err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}

	c.persistReaction(ctx, chat, resp.ID, messageID, emoji)
	return nil
}

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
		ID:        msgID,
		ChatJID:   chatJID,
		Sender:    ourJID,
		Content:   content,
		Timestamp: now,
		IsFromMe:  true,
	}, nil, nil, nil, 0); err != nil {
		c.log.Warnf("Failed to store reaction: %v", err)
		return
	}
	c.log.Infof("[%s] -> %s [reaction %s]: %s", now.Format("2006-01-02 15:04:05"), c.redactor.JID(chatJID), c.redactor.MsgID(msgID), content)
}

func (c *Client) SendReply(ctx context.Context, chatJID, targetMessageID, targetSenderJID, body string) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	chat, err := parseChatJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return "", err
	}

	participant := ""
	if targetSenderJID != "" {
		if sender, perr := types.ParseJID(targetSenderJID); perr == nil {
			participant = sender.ToNonAD().String()
		} else {
			participant = targetSenderJID
		}
	}

	ctxInfo := &waE2E.ContextInfo{
		StanzaID: proto.String(targetMessageID),
	}
	if participant != "" {
		ctxInfo.Participant = proto.String(participant)
	}

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(body),
			ContextInfo: ctxInfo,
		},
		MessageContextInfo: c.ephemeralContextInfo(ctx, chat),
	}

	resp, err := c.sendMessage(ctx, chat, msg)
	if err != nil {
		return "", fmt.Errorf("send reply: %w", err)
	}

	c.persistSent(ctx, chat, resp.ID, body, "", msg)
	return resp.ID, nil
}

func (c *Client) EditMessage(ctx context.Context, chatJID, messageID, newBody string) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	chat, err := parseChatJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return "", err
	}

	newContent := &waE2E.Message{
		Conversation: proto.String(newBody),
	}
	edit := c.wa.BuildEdit(chat, types.MessageID(messageID), newContent)
	resp, err := c.sendMessage(ctx, chat, edit)
	if err != nil {
		return "", fmt.Errorf("send edit: %w", err)
	}
	if err := c.store.UpdateMessage(ctx, messageID, chat.String(), newBody); err != nil {
		c.log.Warnf("Cache edit of %s: %v", messageID, err)
	}
	return resp.ID, nil
}

func (c *Client) DeleteMessage(ctx context.Context, chatJID, messageID, senderJID string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := parseChatJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
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
	if _, err := c.sendMessage(ctx, chat, revoke); err != nil {
		return fmt.Errorf("send revoke: %w", err)
	}
	if err := c.store.RevokeMessage(ctx, messageID, chat.String()); err != nil {
		c.log.Warnf("Cache revoke of %s: %v", messageID, err)
	}
	return nil
}

func (c *Client) ForwardMessage(ctx context.Context, targetChatJID, sourceChatJID, messageID string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if messageID == "" {
		return errors.New("message_id required")
	}

	target, err := parseChatJID(targetChatJID)
	if err != nil {
		return fmt.Errorf("invalid target chat JID: %w", err)
	}
	source, err := parseChatJID(sourceChatJID)
	if err != nil {
		return fmt.Errorf("invalid source chat JID: %w", err)
	}

	cached, err := c.store.GetMessage(ctx, messageID, source.String())
	if err != nil {
		return fmt.Errorf("look up message: %w", err)
	}
	if cached == nil {
		return fmt.Errorf("message %s not found in chat %s", messageID, sourceChatJID)
	}

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(cached.Content),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:        new(messageID),
				Participant:     new(cached.Sender),
				RemoteJID:       new(source.String()),
				ForwardingScore: new(uint32(1)),
				IsForwarded:     new(true),
			},
		},
	}

	if err := c.checkSendWindow(ctx, target); err != nil {
		return err
	}

	resp, err := c.sendMessage(ctx, target, msg)
	if err != nil {
		return fmt.Errorf("send forwarded message: %w", err)
	}

	c.persistSent(ctx, target, resp.ID, cached.Content, "", msg)
	return nil
}

func (c *Client) SendTyping(ctx context.Context, chatJID string, active bool, kind string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
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

func (c *Client) IsOnWhatsApp(ctx context.Context, phones []string) (map[string]bool, error) {
	if !c.socketConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	if len(phones) == 0 {
		return map[string]bool{}, nil
	}

	if err := c.checkUsyncWindow(ctx); err != nil {
		return nil, err
	}

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
	for _, p := range phones {
		result[p] = false
	}
	for _, r := range resp {
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
