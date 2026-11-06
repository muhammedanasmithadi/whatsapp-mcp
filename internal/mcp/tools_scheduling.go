package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// registerSchedulingReadTools wires the read-only view of the local outbox.
// Registered in read-only mode too: listing queued messages changes nothing.
func (s *Server) registerSchedulingReadTools() {
	s.registerListScheduledMessages()
}

// registerSchedulingActionTools wires queue mutations. These do not talk to
// WhatsApp at enqueue time; the daemon scheduler sends them when due.
func (s *Server) registerSchedulingActionTools() {
	s.registerScheduleMessage()
	s.registerCancelScheduledMessage()
}

// -- schedule_message -------------------------------------------------------

type scheduleMessageArgs struct {
	ChatJID      string `json:"chat_jid"`
	Message      string `json:"message"`
	ScheduledAt  string `json:"scheduled_at,omitempty"`
	DelaySeconds int64  `json:"delay_seconds,omitempty"`
}

const maxScheduledMessageBytes = 65536
const maxScheduleHorizon = 365 * 24 * time.Hour

func newScheduledID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "sched_" + hex.EncodeToString(b[:]), nil
}

func (s *Server) registerScheduleMessage() {
	tool := mcp.NewTool("schedule_message",
		mcp.WithDescription("Queue a WhatsApp text message to be sent later by the paired account. Provide exactly one of scheduled_at (RFC 3339 with offset, e.g. 2026-11-07T09:00:00+05:30) or delay_seconds (1 to 31536000). The daemon sends it with a timer at the due time; the queue survives restarts. The message is sent even if the MCP client that queued it is gone. If WhatsApp is disconnected when it comes due, the send fails and is recorded as failed — reconnect first for reliable delivery. Use list_scheduled_messages to inspect the queue and cancel_scheduled_message to remove a pending item before it sends. Returns the stored scheduled message with its id and status."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("message", mcp.Required(), mcp.Description("message body text")),
		mcp.WithString("scheduled_at", mcp.Description("absolute send time as RFC 3339 with offset (e.g. 2026-11-07T09:00:00+05:30)")),
		mcp.WithNumber("delay_seconds", mcp.Description("relative delay in seconds (alternative to scheduled_at)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a scheduleMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if strings.TrimSpace(a.Message) == "" {
			return errorResult(ErrInvalidParameter("message", "non-empty string", "empty string")), nil
		}
		if len(a.Message) > maxScheduledMessageBytes {
			return errorResult(ErrInvalidParameter("message", "at most 65536 bytes", fmt.Sprintf("%d bytes", len(a.Message)))), nil
		}
		if (a.ScheduledAt == "") == (a.DelaySeconds == 0) {
			return errorResult(ErrInvalidParameter("scheduled_at / delay_seconds", "exactly one of scheduled_at or delay_seconds", "both set or both empty")), nil
		}

		var when time.Time
		if a.ScheduledAt != "" {
			t, err := parseTimestamp(a.ScheduledAt)
			if err != nil {
				return errorResult(ErrInvalidParameter("scheduled_at", "RFC 3339 timestamp with offset", a.ScheduledAt)), nil
			}
			when = t
		} else {
			if a.DelaySeconds < 1 {
				return errorResult(ErrInvalidParameter("delay_seconds", "at least 1", fmt.Sprintf("%d", a.DelaySeconds))), nil
			}
			if a.DelaySeconds > int64(maxScheduleHorizon/time.Second) {
				return errorResult(ErrInvalidParameter("delay_seconds", "at most 31536000 (365 days)", fmt.Sprintf("%d", a.DelaySeconds))), nil
			}
			when = time.Now().Add(time.Duration(a.DelaySeconds) * time.Second)
		}
		if !when.After(time.Now()) {
			return errorResult(ErrInvalidParameter("scheduled_at", "a time in the future", when.Format(time.RFC3339))), nil
		}
		if when.After(time.Now().Add(maxScheduleHorizon)) {
			return errorResult(ErrInvalidParameter("scheduled_at", "at most 365 days from now", when.Format(time.RFC3339))), nil
		}

		chatJID := normalizeRecipientToChatJID(a.ChatJID)
		if chatJID == "" {
			return errorResult(ErrInvalidParameter("chat_jid", "a phone number or WhatsApp JID", a.ChatJID)), nil
		}
		// scheduled_messages has a foreign key to chats. Ensure a brand-new
		// recipient gets a row without clobbering an existing chat's name,
		// timestamp, or archive flag.
		if err := s.scheduling.EnsureChat(chatJID, s.messages.FindChatName(chatJID)); err != nil {
			return s.toolErr("schedule_message", chatJID, "", "schedule", err), nil
		}

		id, err := newScheduledID()
		if err != nil {
			return s.toolErr("schedule_message", chatJID, "", "schedule", fmt.Errorf("generate id: %w", err)), nil
		}
		if err := s.scheduling.StoreScheduledMessage(id, chatJID, a.Message, when); err != nil {
			return s.toolErr("schedule_message", chatJID, id, "schedule", err), nil
		}
		return s.toolOK("schedule_message", chatJID, id, "schedule", when.Format(time.RFC3339), store.ScheduledMessage{
			ID:          id,
			ChatJID:     chatJID,
			Content:     a.Message,
			ScheduledAt: when,
			Status:      "pending",
		})
	}))
}

// -- list_scheduled_messages ------------------------------------------------

type listScheduledMessagesArgs struct {
	Status string `json:"status,omitempty"`
}

func (s *Server) registerListScheduledMessages() {
	tool := mcp.NewTool("list_scheduled_messages",
		mcp.WithDescription("List messages queued by schedule_message, newest scheduled time first. Filter with status=pending (default), sent, failed, or all. Each item includes id, chat_jid, message content, scheduled_at, status, and (for completed items) sent_at, sent_message_id, or the failure reason. Read-only; works while disconnected."),
		mcp.WithString("status", mcp.Description("pending (default), sent, failed, or all")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listScheduledMessagesArgs) (*mcp.CallToolResult, error) {
		status := strings.ToLower(strings.TrimSpace(a.Status))
		switch status {
		case "":
			status = "pending"
		case "all":
			status = ""
		case "pending", "sent", "failed":
		default:
			return errorResult(ErrInvalidParameter("status", "pending, sent, failed, or all", a.Status)), nil
		}
		msgs, err := s.scheduling.ListScheduledMessages(status)
		if err != nil {
			return s.toolErr("list_scheduled_messages", "", "", "list_scheduled_messages", err), nil
		}
		out := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			item := map[string]any{
				"id":           m.ID,
				"chat_jid":     m.ChatJID,
				"message":      m.Content,
				"scheduled_at": m.ScheduledAt,
				"status":       m.Status,
			}
			if !m.SentAt.IsZero() {
				item["sent_at"] = m.SentAt
			}
			if m.SentMessageID != "" {
				item["sent_message_id"] = m.SentMessageID
			}
			if m.Error != "" {
				item["error"] = m.Error
			}
			out = append(out, item)
		}
		return s.toolOK("list_scheduled_messages", "", "", "list_scheduled_messages", "", out)
	}))
}

// -- cancel_scheduled_message ----------------------------------------------

type cancelScheduledMessageArgs struct {
	ID string `json:"id"`
}

func (s *Server) registerCancelScheduledMessage() {
	tool := mcp.NewTool("cancel_scheduled_message",
		mcp.WithDescription("Cancel a pending message queued by schedule_message. Only items with status=pending can be cancelled; sent or failed items are history and cannot be removed. Returns {cancelled:true,id} on success or a not_found error when the ID is unknown or no longer pending."),
		mcp.WithString("id", mcp.Required(), mcp.Description("scheduled message ID returned by schedule_message")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a cancelScheduledMessageArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("id", a.ID); r != nil {
			return r, nil
		}
		ok, err := s.scheduling.CancelScheduledMessage(a.ID)
		if err != nil {
			return s.toolErr("cancel_scheduled_message", "", a.ID, "cancel_schedule", err), nil
		}
		if !ok {
			return errorResult(ErrNotFound("pending scheduled message", a.ID)), nil
		}
		return s.toolOK("cancel_scheduled_message", "", a.ID, "cancel_schedule", "", map[string]any{"cancelled": true, "id": a.ID})
	}))
}
