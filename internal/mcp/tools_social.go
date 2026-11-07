package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

// registerSocialReadTools wires read-oriented profile/status tools. These are
// always registered (including read-only mode): they do not send messages or
// change WhatsApp state.
func (s *Server) registerSocialReadTools() {
	s.registerListStatusUpdates()
	s.registerGetProfilePicture()
	s.registerGetContactInfo()
	s.registerGetAutoResponder()
	s.registerSetProfilePicture()
	s.registerSetStatusPrivacy()
}

// registerSocialActionTools wires tools that produce WhatsApp side effects
// (status read receipts, transient availability for a presence query).
func (s *Server) registerSocialActionTools() {
	s.registerMarkStatusViewed()
	s.registerGetPresence()
	s.registerSetAutoResponder()
	s.registerSendStatus()
	s.registerDeleteStatus()
	s.registerGetStatusViewers()
}

// -- list_status_updates ----------------------------------------------------

type listStatusUpdatesArgs struct {
	Sender         string `json:"sender,omitempty"`
	Hours          int    `json:"hours,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeExpired bool   `json:"include_expired,omitempty"`
}

func (s *Server) registerListStatusUpdates() {
	tool := mcp.NewTool("list_status_updates",
		mcp.WithDescription("List cached WhatsApp Status (Stories) updates — the temporary photo/video/text posts contacts share — grouped by sender, newest first. Read-only; does not mark them viewed. Each update includes message_id, timestamp, caption, media_type, expiry, and whether it has expired. To view a photo or video, call download_media with chat_jid='status@broadcast' and that message_id. WhatsApp keeps statuses for 24 hours and delivers them to linked devices as messages; this bridge caches every status it receives while connected. Use mark_status_viewed to send read receipts."),
		mcp.WithString("sender", mcp.Description("optional phone number or JID to show only that contact's statuses")),
		mcp.WithNumber("hours", mcp.Description("look-back window in hours (default 24, max 168)")),
		mcp.WithNumber("limit", mcp.Description("maximum status items to scan (default 200, max 1000)")),
		mcp.WithBoolean("include_expired", mcp.Description("include statuses older than 24 hours (default false)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listStatusUpdatesArgs) (*mcp.CallToolResult, error) {
		if a.Hours < 0 {
			return errorResult(ErrInvalidParameter("hours", "0-168", fmt.Sprintf("%d", a.Hours))), nil
		}
		groups, total, err := s.client.ListStatusUpdates(ctx, client.ListStatusOptions{
			Hours:          a.Hours,
			Sender:         a.Sender,
			IncludeExpired: a.IncludeExpired,
			Limit:          a.Limit,
		})
		if err != nil {
			return s.toolErr("list_status_updates", "", "", "read_statuses", err), nil
		}
		if groups == nil {
			groups = []client.StatusGroup{}
		}
		window := a.Hours
		if window <= 0 {
			window = 24
		}
		payload := map[string]any{
			"groups":         groups,
			"total":          total,
			"window_hours":   window,
			"status_ttl_hrs": 24,
			"download_hint":  "download_media(chat_jid='status@broadcast', message_id=<update.message_id>)",
		}
		if total == 0 {
			payload["note"] = "No cached status updates in this window. Statuses arrive live while the bridge is connected; WhatsApp expires them after 24 hours."
		}
		return s.toolOK("list_status_updates", "", "", "read_statuses", "", payload)
	}))
}

// -- mark_status_viewed -----------------------------------------------------

type markStatusViewedArgs struct {
	Sender     string   `json:"sender"`
	MessageIDs []string `json:"message_ids,omitempty"`
}

func (s *Server) registerMarkStatusViewed() {
	tool := mcp.NewTool("mark_status_viewed",
		mcp.WithDescription("Send read receipts for a contact's WhatsApp Status updates, adding the paired account to the poster's viewer list (subject to the account's read-receipt privacy setting). Use message_ids from list_status_updates; omit them to mark every unexpired cached status from that sender. This is a visible, non-reversible action. Returns the number of receipts sent."),
		mcp.WithString("sender", mcp.Required(), mcp.Description(recipientDesc+" whose status to view")),
		mcp.WithArray("message_ids", mcp.Description("optional status message IDs; omit to mark all unexpired statuses from this sender")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a markStatusViewedArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("sender", a.Sender); r != nil {
			return r, nil
		}
		count, err := s.client.MarkStatusViewed(ctx, a.Sender, a.MessageIDs)
		if err != nil {
			return s.toolErr("mark_status_viewed", a.Sender, "", "mark_status_viewed", err), nil
		}
		return s.toolOK("mark_status_viewed", a.Sender, "", "mark_status_viewed", fmt.Sprintf("%d receipts", count), map[string]any{"success": true, "sender": a.Sender, "viewed": count})
	}))
}

// -- get_profile_picture ----------------------------------------------------

type getProfilePictureArgs struct {
	JID     string `json:"jid"`
	Preview bool   `json:"preview,omitempty"`
}

func (s *Server) registerGetProfilePicture() {
	tool := mcp.NewTool("get_profile_picture",
		mcp.WithDescription("Fetch a contact's or group's WhatsApp profile photo, cache it locally, and return the file path plus an inline image block (images up to 5 MB). Read-only on WhatsApp. WhatsApp privacy settings may hide the photo; that returns a clean unauthorized or not-found error. Set preview=true for the small thumbnail instead of the full-resolution image. Returns JSON with jid, url, id, type, preview, path, and bytes."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc+" or a group JID")),
		mcp.WithBoolean("preview", mcp.Description("fetch the thumbnail instead of the full image (default false)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getProfilePictureArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		result, err := s.client.GetProfilePicture(ctx, a.JID, a.Preview)
		if err != nil {
			switch {
			case errors.Is(err, client.ErrProfilePictureNotSet):
				return errorResult(ErrNotFound("profile picture", a.JID)), nil
			case errors.Is(err, client.ErrProfilePictureHidden):
				return errorResult(ErrUnauthorized("profile picture is hidden by the contact's privacy settings")), nil
			default:
				return s.toolErr("get_profile_picture", a.JID, "", "get_profile_picture", err), nil
			}
		}

		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return s.toolErr("get_profile_picture", a.JID, "", "get_profile_picture", fmt.Errorf("marshal: %w", err)), nil
		}
		contents := []mcp.Content{mcp.NewTextContent(string(b))}
		if inlined, ok := readInlineMedia(result.Path, "image"); ok {
			contents = append(contents, inlined)
		}
		return &mcp.CallToolResult{Content: contents}, nil
	}))
}

// -- get_contact_info -------------------------------------------------------

type getContactInfoArgs struct {
	JID string `json:"jid"`
}

func (s *Server) registerGetContactInfo() {
	tool := mcp.NewTool("get_contact_info",
		mcp.WithDescription("Fetch live WhatsApp profile metadata for a contact: About/status text, profile-picture ID, LID, linked-device list, and business verification, merged with the locally cached name and phone. Read-only; performs one usync lookup and shares the 30/minute usync budget with is_on_whatsapp. Use search_contacts to find a JID by name first, get_profile_picture for the actual image, and list_status_updates for Stories."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getContactInfoArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		info, err := s.client.GetContactInfo(ctx, a.JID)
		if err != nil {
			return s.toolErr("get_contact_info", a.JID, "", "get_contact_info", err), nil
		}
		return s.toolOK("get_contact_info", a.JID, "", "get_contact_info", "", info)
	}))
}

// -- get_presence -----------------------------------------------------------

type getPresenceArgs struct {
	JID  string `json:"jid"`
	Wait int    `json:"wait_seconds,omitempty"`
}

func (s *Server) registerGetPresence() {
	tool := mcp.NewTool("get_presence",
		mcp.WithDescription("Query a contact's current WhatsApp online/last-seen state. Subscribes to their presence and waits up to wait_seconds (default 5) for WhatsApp's response. WhatsApp only delivers presence while the querying device is available, so this briefly marks the paired account online and then restores its previous availability; contacts may see a short online blip. Privacy-gated: contacts who hide online/last-seen return answered=false or last_seen_known=false. Repeat the call to refresh. Returns JSON with online, last_seen, answered, and a note."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc+" whose presence to query")),
		mcp.WithNumber("wait_seconds", mcp.Description("how long to wait for WhatsApp's presence response, default 5, max 15")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getPresenceArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		wait := time.Duration(a.Wait) * time.Second
		if wait <= 0 {
			wait = 5 * time.Second
		}
		if wait > 15*time.Second {
			wait = 15 * time.Second
		}
		result, err := s.client.GetPresence(ctx, a.JID, wait)
		if err != nil {
			return s.toolErr("get_presence", a.JID, "", "get_presence", err), nil
		}
		return s.toolOK("get_presence", a.JID, "", "get_presence", "", result)
	}))
}

// -- set_auto_responder -------------------------------------------------------

type setAutoResponderArgs struct {
	Enabled      bool   `json:"enabled"`
	Message      string `json:"message,omitempty"`
	StartTime    string `json:"start_time,omitempty"`
	EndTime      string `json:"end_time,omitempty"`
	CooldownMinutes int   `json:"cooldown_minutes,omitempty"`
}

func (s *Server) registerSetAutoResponder() {
	tool := mcp.NewTool("set_auto_responder",
		mcp.WithDescription("Enable or disable the auto-responder (out-of-office reply). When enabled, the daemon automatically replies with `message` to every incoming 1:1 text message that is not from the paired account. Replies are event-driven: WhatsApp push delivers the incoming message and the daemon replies immediately — no polling. Group chats, newsletters, status broadcasts, and media-only messages are ignored. Optionally restrict replies to a time window (start_time, end_time in HH:MM format) and set a cooldown in minutes between replies to the same contact. Set enabled=false to turn it off. Returns {enabled, message}."),
		mcp.WithBoolean("enabled", mcp.Required(), mcp.Description("true to enable automatic replies, false to disable")),
		mcp.WithString("message", mcp.Description("auto-reply text sent to qualifying incoming messages; required when enabling")),
		mcp.WithString("start_time", mcp.Description("optional start time in HH:MM format; auto-responder only active after this time")),
		mcp.WithString("end_time", mcp.Description("optional end time in HH:MM format; auto-responder only active before this time")),
		mcp.WithNumber("cooldown_minutes", mcp.Description("optional cooldown in minutes between replies to the same contact")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setAutoResponderArgs) (*mcp.CallToolResult, error) {
		if a.Enabled && strings.TrimSpace(a.Message) == "" {
			return errorResult(ErrInvalidParameter("message", "non-empty reply text when enabled", "empty string")), nil
		}
		return s.handleTool(ctx, "set_auto_responder", "", "", "set_auto_responder", func() (any, string, error) {
			if err := s.client.Store().SetAutoResponderWithConfig(ctx, a.Enabled, a.Message, a.StartTime, a.EndTime, a.CooldownMinutes); err != nil {
				return nil, "", err
			}
			return map[string]any{"enabled": a.Enabled, "message": a.Message}, "", nil
		})
	}))
}

// -- get_auto_responder -------------------------------------------------------

type getAutoResponderArgs struct{}

func (s *Server) registerGetAutoResponder() {
	tool := mcp.NewTool("get_auto_responder",
		mcp.WithDescription("Return the current auto-responder configuration: whether it is enabled, the reply message, start time, end time, and cooldown minutes. Read-only; no side effects. Returns {enabled, message, start_time, end_time, cooldown_minutes}."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ getAutoResponderArgs) (*mcp.CallToolResult, error) {
		enabled, message, startTime, endTime, cooldownMinutes, err := s.client.Store().GetAutoResponderWithConfig(ctx)
		if err != nil {
			return s.toolErr("get_auto_responder", "", "", "get_auto_responder", err), nil
		}
		return s.toolOK("get_auto_responder", "", "", "get_auto_responder", "", map[string]any{
			"enabled":         enabled,
			"message":         message,
			"start_time":      startTime,
			"end_time":        endTime,
			"cooldown_minutes": cooldownMinutes,
		})
	}))
}

// -- send_status --------------------------------------------------------------

type sendStatusArgs struct {
	MediaPath string `json:"media_path"`
	Caption   string `json:"caption,omitempty"`
}

func (s *Server) registerSendStatus() {
	tool := mcp.NewTool("send_status",
		mcp.WithDescription("Post a WhatsApp Status (Story) update as a media message to status@broadcast. media_path must be an image or video under the configured media root. caption is the optional status text. Returns {success, chat_jid, message_id}."),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("absolute path to an image or video file under the media root")),
		mcp.WithString("caption", mcp.Description("optional status caption text")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendStatusArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("media_path", a.MediaPath); r != nil {
			return r, nil
		}
		r := s.client.SendStatus(ctx, a.MediaPath, a.Caption)
		if !r.Success {
			return s.toolErr("send_status", "", "", "send_status", fmt.Errorf("send status: %s", r.Message)), nil
		}
		return s.toolOK("send_status", "", "", "send_status", "", map[string]any{"success": true, "message_id": r.ID})
	}))
}

// -- delete_status ------------------------------------------------------------

type deleteStatusArgs struct {
	MessageID string `json:"message_id"`
}

func (s *Server) registerDeleteStatus() {
	tool := mcp.NewTool("delete_status",
		mcp.WithDescription("Delete a Status (Story) update by revoking its message from status@broadcast. message_id is from list_status_updates. Returns {success, message_id}."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID of the status to delete")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a deleteStatusArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
		if err := s.client.DeleteStatus(ctx, a.MessageID); err != nil {
			return s.toolErr("delete_status", "", a.MessageID, "delete_status", err), nil
		}
		return s.toolOK("delete_status", "", a.MessageID, "delete_status", "", map[string]any{"success": true, "message_id": a.MessageID})
	}))
}

// -- get_status_viewers -------------------------------------------------------

type getStatusViewersArgs struct {
	MessageID string `json:"message_id"`
}

func (s *Server) registerGetStatusViewers() {
	tool := mcp.NewTool("get_status_viewers",
		mcp.WithDescription("Return viewers of a Status (Story) update. WhatsApp does not expose status viewer lists to linked devices through a public protocol endpoint, so this returns an empty list with a note. message_id is from list_status_updates. Returns {viewers, note}."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID of the status to check viewers for")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getStatusViewersArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
		viewers, err := s.client.GetStatusViewers(ctx, a.MessageID)
		if err != nil {
			return s.toolErr("get_status_viewers", "", a.MessageID, "get_status_viewers", err), nil
		}
		return s.toolOK("get_status_viewers", "", a.MessageID, "get_status_viewers", "", map[string]any{"viewers": viewers, "note": "WhatsApp does not expose status viewer lists to linked devices through a public endpoint"})
	}))
}

// -- set_profile_picture ------------------------------------------------------

type setProfilePictureArgs struct {
	MediaPath string `json:"media_path"`
}

func (s *Server) registerSetProfilePicture() {
	tool := mcp.NewTool("set_profile_picture",
		mcp.WithDescription("Set the paired account's WhatsApp profile picture. media_path must be an image file under the configured media root. Returns {success} on success."),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("absolute path to an image file under the media root")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setProfilePictureArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("media_path", a.MediaPath); r != nil {
			return r, nil
		}
		if err := s.client.SetProfilePicture(ctx, a.MediaPath); err != nil {
			return s.toolErr("set_profile_picture", "", "", "set_profile_picture", err), nil
		}
		return s.toolOK("set_profile_picture", "", "", "set_profile_picture", "", map[string]any{"success": true})
	}))
}

// -- set_status_privacy_setting ---------------------------------------------

type setStatusPrivacyArgs struct {
	Mode      string   `json:"mode"`
	UserJIDs  []string `json:"user_jids,omitempty"`
}

func (s *Server) registerSetStatusPrivacy() {
	tool := mcp.NewTool("set_status_privacy_setting",
		mcp.WithDescription("Set the privacy mode for the paired account's Status (Stories) updates. mode can be 'all' (everyone), 'contacts' (my contacts), 'close_friends' (close friends only), or 'custom' (custom list). For 'custom', user_jids should contain the JIDs of allowed contacts. Returns {success, mode}."),
		mcp.WithString("mode", mcp.Required(), mcp.Description("privacy mode: all, contacts, close_friends, or custom")),
		mcp.WithArray("user_jids", mcp.Description("for 'custom' mode: list of contact JIDs allowed to view statuses")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setStatusPrivacyArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("mode", a.Mode); r != nil {
			return r, nil
		}
		if err := s.client.SetStatusPrivacy(ctx, a.Mode, a.UserJIDs); err != nil {
			return s.toolErr("set_status_privacy_setting", "", "", "set_status_privacy_setting", err), nil
		}
		return s.toolOK("set_status_privacy_setting", "", "", "set_status_privacy_setting", "", map[string]any{"success": true, "mode": a.Mode})
	}))
}

// -- forward_message --------------------------------------------------------

type forwardMessageArgs struct {
	ChatJID       string `json:"chat_jid"`
	SourceChatJID string `json:"source_chat_jid"`
	MessageID     string `json:"message_id"`
}

func (s *Server) registerForwardMessage() {
	tool := mcp.NewTool("forward_message",
		mcp.WithDescription("Forward a message from source_chat_jid to chat_jid. The original message must be cached locally (use list_messages first). The forwarded message carries context info linking back to the original. Returns {success, chat_jid, message_id}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description("target chat JID to forward to")),
		mcp.WithString("source_chat_jid", mcp.Required(), mcp.Description("source chat JID where the message is located")),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("message ID to forward")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a forwardMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("source_chat_jid", a.SourceChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
		if err := s.client.ForwardMessage(ctx, a.ChatJID, a.SourceChatJID, a.MessageID); err != nil {
			return s.toolErr("forward_message", a.ChatJID, a.MessageID, "forward_message", err), nil
		}
		return s.toolOK("forward_message", a.ChatJID, a.MessageID, "forward_message", "", map[string]any{"success": true})
	}))
}
