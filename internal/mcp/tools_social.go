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
}

// registerSocialActionTools wires tools that produce WhatsApp side effects
// (status read receipts, transient availability for a presence query).
func (s *Server) registerSocialActionTools() {
	s.registerMarkStatusViewed()
	s.registerGetPresence()
	s.registerSetAutoResponder()
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
	Enabled bool   `json:"enabled"`
	Message string `json:"message,omitempty"`
}

func (s *Server) registerSetAutoResponder() {
	tool := mcp.NewTool("set_auto_responder",
		mcp.WithDescription("Enable or disable the auto-responder (out-of-office reply). When enabled, the daemon automatically replies with `message` to every incoming 1:1 text message that is not from the paired account. Replies are event-driven: WhatsApp push delivers the incoming message and the daemon replies immediately — no polling. Group chats, newsletters, status broadcasts, and media-only messages are ignored. Set enabled=false to turn it off. Returns {enabled, message}."),
		mcp.WithBoolean("enabled", mcp.Required(), mcp.Description("true to enable automatic replies, false to disable")),
		mcp.WithString("message", mcp.Description("auto-reply text sent to qualifying incoming messages; required when enabling")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setAutoResponderArgs) (*mcp.CallToolResult, error) {
		if a.Enabled && strings.TrimSpace(a.Message) == "" {
			return errorResult(ErrInvalidParameter("message", "non-empty reply text when enabled", "empty string")), nil
		}
		if err := s.client.Store().SetAutoResponder(ctx, a.Enabled, a.Message); err != nil {
			return s.toolErr("set_auto_responder", "", "", "set_auto_responder", err), nil
		}
		return s.toolOK("set_auto_responder", "", "", "set_auto_responder", "", map[string]any{
			"enabled":  a.Enabled,
			"message":  a.Message,
		})
	}))
}

// -- get_auto_responder -------------------------------------------------------

type getAutoResponderArgs struct{}

func (s *Server) registerGetAutoResponder() {
	tool := mcp.NewTool("get_auto_responder",
		mcp.WithDescription("Return the current auto-responder configuration: whether it is enabled and the reply message. Read-only; no side effects. Returns {enabled, message}."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ getAutoResponderArgs) (*mcp.CallToolResult, error) {
		enabled, message, err := s.client.Store().GetAutoResponder(ctx)
		if err != nil {
			return s.toolErr("get_auto_responder", "", "", "get_auto_responder", err), nil
		}
		return s.toolOK("get_auto_responder", "", "", "get_auto_responder", "", map[string]any{
			"enabled": enabled,
			"message": message,
		})
	}))
}
