package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// ChannelInfo is WhatsApp's metadata for a channel (newsletter in the
// protocol). The SDK type already carries JSON tags; this alias documents the
// domain name used by the MCP tools.
type ChannelInfo = types.NewsletterMetadata

// ChannelMessage is a JSON-friendly view of one channel post. The raw protobuf
// is deliberately not exposed: it is large and contains encryption material.
type ChannelMessage struct {
	ServerID      int64          `json:"server_id"`
	MessageID     string         `json:"message_id"`
	Type          string         `json:"type,omitempty"`
	Timestamp     time.Time      `json:"timestamp"`
	Text          string         `json:"text,omitempty"`
	MediaType     string         `json:"media_type,omitempty"`
	URL           string         `json:"url,omitempty"`
	Views         int            `json:"views"`
	Forwards      int            `json:"forwards"`
	Reactions     map[string]int `json:"reactions,omitempty"`
	ReactionTotal int            `json:"reaction_total,omitempty"`
}

// topReactions keeps responses bounded: popular channels can carry thousands
// of distinct emoji codes per post. The total is preserved separately.
func topReactions(counts map[string]int, limit int) map[string]int {
	if len(counts) == 0 {
		return nil
	}
	type reaction struct {
		code  string
		count int
	}
	list := make([]reaction, 0, len(counts))
	for code, count := range counts {
		list = append(list, reaction{code, count})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].code < list[j].code
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make(map[string]int, len(list))
	for _, r := range list {
		out[r.code] = r.count
	}
	return out
}

// ListChannels returns the channels the paired account follows.
func (c *Client) ListChannels(ctx context.Context) ([]*ChannelInfo, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	return c.wa.GetSubscribedNewsletters(ctx)
}

// resolveChannelJID accepts a channel JID, an invite link, or a bare invite
// code and returns the channel JID. For invite inputs it performs a metadata
// lookup first.
func (c *Client) resolveChannelJID(ctx context.Context, input string) (types.JID, *ChannelInfo, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return types.JID{}, nil, errors.New("channel JID or invite link required")
	}
	if strings.Contains(input, "@") {
		jid, err := types.ParseJID(input)
		if err != nil {
			return types.JID{}, nil, fmt.Errorf("parse channel JID: %w", err)
		}
		if jid.Server != types.NewsletterServer {
			return types.JID{}, nil, fmt.Errorf("not a channel JID: %s", input)
		}
		return jid, nil, nil
	}

	code := input
	if i := strings.LastIndex(input, "/channel/"); i >= 0 {
		code = input[i+len("/channel/"):]
	}
	code = strings.Trim(code, "/ ")
	if code == "" {
		return types.JID{}, nil, fmt.Errorf("could not extract channel invite code from %q", input)
	}
	info, err := c.wa.GetNewsletterInfoWithInvite(ctx, code)
	if err != nil {
		return types.JID{}, nil, fmt.Errorf("resolve channel invite: %w", err)
	}
	if info == nil || info.ID.IsEmpty() {
		return types.JID{}, nil, fmt.Errorf("channel invite %q did not resolve", code)
	}
	return info.ID, info, nil
}

// GetChannelInfo returns channel metadata. input may be a channel JID or an
// invite link/code.
func (c *Client) GetChannelInfo(ctx context.Context, input string) (*ChannelInfo, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("channel JID or invite link required")
	}
	jid, info, err := c.resolveChannelJID(ctx, input)
	if err != nil {
		return nil, err
	}
	if info != nil {
		return info, nil
	}
	return c.wa.GetNewsletterInfo(ctx, jid)
}

// ListChannelMessages returns recent posts from one channel, newest first.
func (c *Client) ListChannelMessages(ctx context.Context, input string, count int, before int64) ([]ChannelMessage, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	if count <= 0 {
		count = 20
	}
	if count > 100 {
		count = 100
	}
	jid, _, err := c.resolveChannelJID(ctx, input)
	if err != nil {
		return nil, err
	}
	params := &whatsmeow.GetNewsletterMessagesParams{Count: count}
	if before > 0 {
		params.Before = types.MessageServerID(before)
	}
	raw, err := c.wa.GetNewsletterMessages(ctx, jid, params)
	if err != nil {
		return nil, fmt.Errorf("get channel messages: %w", err)
	}
	out := make([]ChannelMessage, 0, len(raw))
	for _, m := range raw {
		if m == nil {
			continue
		}
		item := ChannelMessage{
			ServerID:  int64(m.MessageServerID),
			MessageID: string(m.MessageID),
			Type:      m.Type,
			Timestamp: m.Timestamp,
			Text:      extractTextContent(m.Message),
			Views:     m.ViewsCount,
			Forwards:  m.ForwardsCount,
			Reactions: topReactions(m.ReactionCounts, 12),
		}
		for _, n := range m.ReactionCounts {
			item.ReactionTotal += n
		}
		if m.Message != nil {
			mediaType, _, url, _, _, _, _, _ := extractMediaInfo(m.Message)
			item.MediaType = mediaType
			item.URL = url
		}
		out = append(out, item)
	}
	return out, nil
}

// FollowChannel follows a channel by JID or invite link/code.
func (c *Client) FollowChannel(ctx context.Context, input string) (*ChannelInfo, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	jid, info, err := c.resolveChannelJID(ctx, input)
	if err != nil {
		return nil, err
	}
	if info == nil && jid.IsEmpty() {
		return nil, errors.New("channel could not be resolved")
	}
	if err := c.wa.FollowNewsletter(ctx, jid); err != nil {
		return nil, fmt.Errorf("follow channel: %w", err)
	}
	if info != nil {
		return info, nil
	}
	return c.wa.GetNewsletterInfo(ctx, jid)
}

// UnfollowChannel stops following a channel.
func (c *Client) UnfollowChannel(ctx context.Context, input string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, _, err := c.resolveChannelJID(ctx, input)
	if err != nil {
		return err
	}
	return c.wa.UnfollowNewsletter(ctx, jid)
}

// SetChannelMuted mutes or unmutes a channel.
func (c *Client) SetChannelMuted(ctx context.Context, input string, muted bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, _, err := c.resolveChannelJID(ctx, input)
	if err != nil {
		return err
	}
	return c.wa.NewsletterToggleMute(ctx, jid, muted)
}
