package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) registerChatStateTools() {
	s.registerMuteChat()
	s.registerPinChat()
	s.registerStarMessage()
	s.registerSetDisappearingTimer()
}

// -- mute_chat --------------------------------------------------------------

type muteChatArgs struct {
	ChatJID         string `json:"chat_jid"`
	Muted           bool   `json:"muted"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
}

func (s *Server) registerMuteChat() {
	tool := mcp.NewTool("mute_chat",
		mcp.WithDescription("Mute or unmute a WhatsApp chat. Changes sync to the phone and other linked devices through WhatsApp app state. When muting, duration_seconds sets a timed mute (8 hours = 28800, 1 week = 604800); omit it or pass 0 to mute until manually unmuted. Set muted=false to unmute. Returns {success, chat_jid, muted, duration_seconds}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithBoolean("muted", mcp.Required(), mcp.Description("true to mute, false to unmute")),
		mcp.WithNumber("duration_seconds", mcp.Description("timed mute length in seconds; 0 or omitted means forever")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a muteChatArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if a.DurationSeconds < 0 {
			return errorResult(ErrInvalidParameter("duration_seconds", "0 or greater", fmt.Sprintf("%d", a.DurationSeconds))), nil
		}
		var duration time.Duration
		if a.DurationSeconds > 0 {
			duration = time.Duration(a.DurationSeconds) * time.Second
		}
		if err := s.client.SetChatMuted(ctx, a.ChatJID, a.Muted, duration); err != nil {
			s.auditLog("mute_chat", a.ChatJID, "", "mute_chat", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("mute_chat", a.ChatJID, "", "mute_chat", "success", fmt.Sprintf("muted=%v duration=%s", a.Muted, duration))
		return resultJSON(map[string]any{
			"success":          true,
			"chat_jid":         a.ChatJID,
			"muted":            a.Muted,
			"duration_seconds": a.DurationSeconds,
		})
	}))
}

// -- pin_chat ---------------------------------------------------------------

type pinChatArgs struct {
	ChatJID string `json:"chat_jid"`
	Pinned  bool   `json:"pinned"`
}

func (s *Server) registerPinChat() {
	tool := mcp.NewTool("pin_chat",
		mcp.WithDescription("Pin or unpin a WhatsApp chat in the chat list. Changes sync to the phone and other linked devices through WhatsApp app state. WhatsApp allows a limited number of pinned chats. Returns {success, chat_jid, pinned}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithBoolean("pinned", mcp.Required(), mcp.Description("true to pin, false to unpin")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a pinChatArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetChatPinned(ctx, a.ChatJID, a.Pinned); err != nil {
			s.auditLog("pin_chat", a.ChatJID, "", "pin_chat", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("pin_chat", a.ChatJID, "", "pin_chat", "success", fmt.Sprintf("pinned=%v", a.Pinned))
		return resultJSON(map[string]any{"success": true, "chat_jid": a.ChatJID, "pinned": a.Pinned})
	}))
}

// -- star_message -----------------------------------------------------------

type starMessageArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	Starred   *bool  `json:"starred,omitempty"`
}

func (s *Server) registerStarMessage() {
	tool := mcp.NewTool("star_message",
		mcp.WithDescription("Star or unstar a cached WhatsApp message. Stars sync to the phone and other linked devices through WhatsApp app state, and starred messages appear in WhatsApp's Starred list. The message must already be cached locally (use request_sync or list_messages first). starred defaults to true; set false to unstar. Returns {success, chat_jid, message_id, starred}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("message ID from list_messages or get_message_context")),
		mcp.WithBoolean("starred", mcp.Description("true to star (default), false to unstar")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a starMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("message_id", a.MessageID); r != nil {
			return r, nil
		}
		starred := true
		if a.Starred != nil {
			starred = *a.Starred
		}
		if err := s.client.StarMessage(ctx, a.ChatJID, a.MessageID, starred); err != nil {
			s.auditLog("star_message", a.ChatJID, a.MessageID, "star_message", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("star_message", a.ChatJID, a.MessageID, "star_message", "success", fmt.Sprintf("starred=%v", starred))
		return resultJSON(map[string]any{"success": true, "chat_jid": a.ChatJID, "message_id": a.MessageID, "starred": starred})
	}))
}

// -- set_disappearing_timer -------------------------------------------------

type setDisappearingTimerArgs struct {
	ChatJID string `json:"chat_jid"`
	Seconds int64  `json:"seconds"`
}

func (s *Server) registerSetDisappearingTimer() {
	tool := mcp.NewTool("set_disappearing_timer",
		mcp.WithDescription("Set or clear disappearing messages for a WhatsApp chat. Changes sync to the phone and other linked devices. Use 86400 (24 hours), 604800 (7 days), or 7776000 (90 days) to match WhatsApp's UI presets; 0 disables disappearing messages. WhatsApp announces the change in the chat. Returns {success, chat_jid, seconds}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithNumber("seconds", mcp.Required(), mcp.Description("disappearing-message timer in seconds; 0 disables")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setDisappearingTimerArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if a.Seconds < 0 {
			return errorResult(ErrInvalidParameter("seconds", "0 or greater", fmt.Sprintf("%d", a.Seconds))), nil
		}
		if err := s.client.SetDisappearingTimer(ctx, a.ChatJID, time.Duration(a.Seconds)*time.Second); err != nil {
			s.auditLog("set_disappearing_timer", a.ChatJID, "", "set_disappearing_timer", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "", true)), nil
		}
		s.auditLog("set_disappearing_timer", a.ChatJID, "", "set_disappearing_timer", "success", fmt.Sprintf("%ds", a.Seconds))
		return resultJSON(map[string]any{"success": true, "chat_jid": a.ChatJID, "seconds": a.Seconds})
	}))
}
