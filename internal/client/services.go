package client

import (
	"context"
	"time"
)

// SocialProvider is the WhatsApp social surface: statuses (Stories), profile
// photos, contact metadata, and online/last-seen presence. *Client satisfies
// this interface.
type SocialProvider interface {
	ListStatusUpdates(ctx context.Context, opts ListStatusOptions) ([]StatusGroup, int, error)
	MarkStatusViewed(ctx context.Context, sender string, ids []string) (int, error)
	GetProfilePicture(ctx context.Context, jid string, preview bool) (*ProfilePictureResult, error)
	GetContactInfo(ctx context.Context, jid string) (*ContactInfoResult, error)
	GetPresence(ctx context.Context, jid string, wait time.Duration) (*PresenceResult, error)
}

// Sender is the WhatsApp outbound surface used by MCP message/location tools.
// *Client satisfies this interface.
type Sender interface {
	Send(ctx context.Context, recipient, message string) SendResult
	SendLocation(ctx context.Context, recipient string, lat, lng float64, name, address string) SendResult
	SendPresence(ctx context.Context, state string) error
}

// ChatStateProvider is the WhatsApp app-state surface: mute, pin, star,
// disappearing timer. *Client satisfies this interface.
type ChatStateProvider interface {
	SetChatMuted(ctx context.Context, recipient string, muted bool, duration time.Duration) error
	SetChatPinned(ctx context.Context, recipient string, pinned bool) error
	StarMessage(ctx context.Context, chat string, messageID string, starred bool) error
	SetDisappearingTimer(ctx context.Context, recipient string, duration time.Duration) error
}

// ChannelProvider is the WhatsApp Channels (newsletter) surface. *Client
// satisfies this interface.
type ChannelProvider interface {
	ListChannels(ctx context.Context) ([]*ChannelInfo, error)
	GetChannelInfo(ctx context.Context, input string) (*ChannelInfo, error)
	ListChannelMessages(ctx context.Context, input string, count int, before int64) ([]ChannelMessage, error)
	FollowChannel(ctx context.Context, input string) (*ChannelInfo, error)
	UnfollowChannel(ctx context.Context, input string) error
	SetChannelMuted(ctx context.Context, input string, muted bool) error
}

// PrivacyProvider is the WhatsApp privacy/blocklist surface. *Client satisfies
// this interface.
type PrivacyProvider interface {
	BlockContact(ctx context.Context, jid string) error
	UnblockContact(ctx context.Context, jid string) error
	GetBlocklist(ctx context.Context) (string, error)
	SetPrivacySetting(ctx context.Context, name, value string) (string, error)
	GetPrivacySettings(ctx context.Context) (string, error)
	SetStatusMessage(ctx context.Context, text string) error
}

// Connected reports whether the WhatsApp session is currently online.
type Connected interface {
	IsConnected() bool
}

// ensure *Client satisfies all provider interfaces.
var (
	_ SocialProvider    = (*Client)(nil)
	_ Sender            = (*Client)(nil)
	_ ChatStateProvider = (*Client)(nil)
	_ ChannelProvider   = (*Client)(nil)
	_ PrivacyProvider   = (*Client)(nil)
	_ Connected         = (*Client)(nil)
)

// WhatsAppClient is the aggregate of all WhatsApp protocol surfaces the MCP
// layer depends on. It exists so the MCP server can be constructed against an
// interface rather than the concrete *Client type (Dependency Inversion).
type WhatsAppClient interface {
	Connected
	Sender
	SocialProvider
	ChatStateProvider
	ChannelProvider
	PrivacyProvider

	// Contact discovery
	IsOnWhatsApp(ctx context.Context, phones []string) (map[string]bool, error)

	// Message history
	MarkRead(ctx context.Context, chatJID string, messageIDs []string, senderJID string) error

	// Group management
	CreateGroup(ctx context.Context, name string, participants []string) (string, string, error)
	LeaveGroup(ctx context.Context, chatJID string) error
	JoinGroupWithLink(ctx context.Context, linkOrCode string) (string, error)
	GetGroupInfoJSON(ctx context.Context, chatJID string) (string, error)
	SetGroupName(ctx context.Context, chatJID, name string) error
	SetGroupTopic(ctx context.Context, chatJID, topic string) error
	SetGroupAnnounce(ctx context.Context, chatJID string, announceOnly bool) error
	SetGroupLocked(ctx context.Context, chatJID string, locked bool) error

	// Media
	Download(ctx context.Context, messageID, chatJID, outputPath string) DownloadResult
}

// ensure *Client satisfies the aggregate interface.
var _ WhatsAppClient = (*Client)(nil)
