package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

// registerChannelReadTools wires WhatsApp Channels (protocol name:
// newsletters). All are read-only and safe in read-only mode.
func (s *Server) registerChannelReadTools() {
	s.registerListChannels()
	s.registerGetChannelInfo()
	s.registerListChannelMessages()
}

func (s *Server) registerChannelActionTools() {
	s.registerFollowChannel()
	s.registerUnfollowChannel()
	s.registerMuteChannel()
}

// -- list_channels ----------------------------------------------------------

func (s *Server) registerListChannels() {
	tool := mcp.NewTool("list_channels",
		mcp.WithDescription("List the WhatsApp Channels (broadcast channels) the paired account follows, with name, description, subscriber count, invite code, verification state, and mute state. Read-only; works only while connected. Use get_channel_info for one channel, list_channel_messages to read posts, and follow_channel / unfollow_channel to change subscriptions. Returns a JSON array; empty when the account follows no channels."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		channels, err := s.client.ListChannels(ctx)
		if err != nil {
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		if channels == nil {
			channels = []*client.ChannelInfo{}
		}
		return resultJSON(channels)
	})
}

// -- get_channel_info -------------------------------------------------------

type getChannelInfoArgs struct {
	Channel string `json:"channel"`
}

func (s *Server) registerGetChannelInfo() {
	tool := mcp.NewTool("get_channel_info",
		mcp.WithDescription("Fetch metadata for one WhatsApp Channel by channel JID (…@newsletter) or invite link/code (https://whatsapp.com/channel/<code>). Read-only. Returns the channel's name, description, subscriber count, invite code, picture, verification state, and settings."),
		mcp.WithString("channel", mcp.Required(), mcp.Description("channel JID or invite link/code")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getChannelInfoArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("channel", a.Channel); r != nil {
			return r, nil
		}
		info, err := s.client.GetChannelInfo(ctx, a.Channel)
		if err != nil {
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		return resultJSON(info)
	}))
}

// -- list_channel_messages --------------------------------------------------

type listChannelMessagesArgs struct {
	Channel        string `json:"channel"`
	Limit          int    `json:"limit,omitempty"`
	BeforeServerID int64  `json:"before_server_id,omitempty"`
}

func (s *Server) registerListChannelMessages() {
	tool := mcp.NewTool("list_channel_messages",
		mcp.WithDescription("Read recent posts from a WhatsApp Channel by JID or invite link/code, newest first. Read-only. Each post includes server_id, message_id, timestamp, text, media_type/URL when present, view count, forward count, and reaction counts. Pass before_server_id with the oldest server_id from a prior page to walk backwards. Note that channel media URLs are CDN links; the bridge does not decrypt channel media. Returns a JSON array."),
		mcp.WithString("channel", mcp.Required(), mcp.Description("channel JID or invite link/code")),
		mcp.WithNumber("limit", mcp.Description("number of posts to fetch (default 20, max 100)")),
		mcp.WithNumber("before_server_id", mcp.Description("return posts older than this server ID (pagination cursor)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listChannelMessagesArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("channel", a.Channel); r != nil {
			return r, nil
		}
		msgs, err := s.client.ListChannelMessages(ctx, a.Channel, a.Limit, a.BeforeServerID)
		if err != nil {
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		if msgs == nil {
			msgs = []client.ChannelMessage{}
		}
		return resultJSON(msgs)
	}))
}

// -- follow_channel / unfollow_channel --------------------------------------

type channelMutationArgs struct {
	Channel string `json:"channel"`
}

func (s *Server) registerFollowChannel() {
	tool := mcp.NewTool("follow_channel",
		mcp.WithDescription("Follow a WhatsApp Channel by JID or invite link/code. The channel then appears in the paired account's channel list and its new posts arrive as events. Reversible with unfollow_channel. Returns the channel metadata."),
		mcp.WithString("channel", mcp.Required(), mcp.Description("channel JID or invite link/code")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a channelMutationArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("channel", a.Channel); r != nil {
			return r, nil
		}
		info, err := s.client.FollowChannel(ctx, a.Channel)
		if err != nil {
			s.auditLog("follow_channel", a.Channel, "", "follow_channel", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("follow_channel", a.Channel, "", "follow_channel", "success", "")
		return resultJSON(info)
	}))
}

func (s *Server) registerUnfollowChannel() {
	tool := mcp.NewTool("unfollow_channel",
		mcp.WithDescription("Stop following a WhatsApp Channel by JID or invite link/code. Removes it from the paired account's channel list. Reversible with follow_channel. Returns {success:true}."),
		mcp.WithString("channel", mcp.Required(), mcp.Description("channel JID or invite link/code")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a channelMutationArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("channel", a.Channel); r != nil {
			return r, nil
		}
		if err := s.client.UnfollowChannel(ctx, a.Channel); err != nil {
			s.auditLog("unfollow_channel", a.Channel, "", "unfollow_channel", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("unfollow_channel", a.Channel, "", "unfollow_channel", "success", "")
		return resultJSON(map[string]any{"success": true, "channel": a.Channel})
	}))
}

// -- mute_channel -----------------------------------------------------------

type muteChannelArgs struct {
	Channel string `json:"channel"`
	Muted   bool   `json:"muted"`
}

func (s *Server) registerMuteChannel() {
	tool := mcp.NewTool("mute_channel",
		mcp.WithDescription("Mute or unmute a WhatsApp Channel by JID or invite link/code. Muting stops notifications for new posts without unfollowing. Set muted=false to restore notifications. Returns {success, channel, muted}."),
		mcp.WithString("channel", mcp.Required(), mcp.Description("channel JID or invite link/code")),
		mcp.WithBoolean("muted", mcp.Required(), mcp.Description("true to mute, false to unmute")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a muteChannelArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("channel", a.Channel); r != nil {
			return r, nil
		}
		if err := s.client.SetChannelMuted(ctx, a.Channel, a.Muted); err != nil {
			s.auditLog("mute_channel", a.Channel, "", "mute_channel", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("mute_channel", a.Channel, "", "mute_channel", "success", "")
		return resultJSON(map[string]any{"success": true, "channel": a.Channel, "muted": a.Muted})
	}))
}
