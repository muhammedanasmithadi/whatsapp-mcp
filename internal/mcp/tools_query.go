package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// registerQueryTools wires read-only query tools: list_chats, get_chat,
// list_messages, get_message_context, search_contacts, get_status,
// is_on_whatsapp, list_labels, list_chats_by_label.
func (s *Server) registerQueryTools() {
	s.registerSearchContacts()
	s.registerListMessages()
	s.registerListChats()
	s.registerGetChat()
	s.registerGetMessageContext()
	s.registerGetReadPosition()
	s.registerMarkReadPosition()
	s.registerListMediaMessages()
	s.registerIsOnWhatsApp()
	s.registerGetStatus()
	s.registerPairingStatus()
	s.registerListLabels()
	s.registerListChatsByLabel()
	s.registerGetAuditLog()
}

// -- search_contacts --------------------------------------------------------

type searchContactsArgs struct {
	Query string `json:"query"`
}

func (s *Server) registerSearchContacts() {
	tool := mcp.NewTool("search_contacts",
		mcp.WithDescription(offlineSafePrefix+"Search the cached WhatsApp contact list by case-insensitive substring of name or phone number. Read-only; no side effects. Use is_on_whatsapp to verify whether an unknown phone number is registered. Returns a JSON array of contact objects (each with JID, push name, full name, and phone)."),
		mcp.WithString("query", mcp.Required(), mcp.Description("case-insensitive substring to match against name or phone")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a searchContactsArgs) (*mcp.CallToolResult, error) {
		if a.Query == "" {
			return errorResult(ErrInvalidParameter("query", "non-empty string", "empty string")), nil
		}
		contacts, err := s.client.Store().SearchContacts(ctx, a.Query)
		if err != nil {
			s.auditLog("search_contacts", "", "", "search_contacts", "error", err.Error())
			return errorResult(ErrInternal(err.Error())), nil
		}
		return resultJSON(contacts)
	}))
}

// -- list_messages ----------------------------------------------------------

type listMessagesArgs struct {
	After             string `json:"after,omitempty"`
	Before            string `json:"before,omitempty"`
	SenderPhoneNumber string `json:"sender_phone_number,omitempty"`
	ChatJID           string `json:"chat_jid,omitempty"`
	Query             string `json:"query,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Page              int    `json:"page,omitempty"`
	Cursor            string `json:"cursor,omitempty"`
	Format            string `json:"format,omitempty"`
	IncludeContext    *bool  `json:"include_context,omitempty"`
	ContextBefore     int    `json:"context_before,omitempty"`
	ContextAfter      int    `json:"context_after,omitempty"`
}

func (s *Server) registerListMessages() {
	tool := mcp.NewTool("list_messages",
		mcp.WithDescription(offlineSafePrefix+"Search and page through cached WhatsApp messages, optionally filtering by chat, sender, time range, and substring; can include surrounding context messages. Read-only; no side effects. Use get_message_context to expand around a single known message ID, or request_sync to backfill missing history. Set format=json to receive a structured result with messages plus next_cursor/has_more for keyset pagination (use the cursor from a prior response to walk backwards without offset drift). Returns a human-readable formatted text block by default, or a JSON object when format=json."),
		mcp.WithString("after", mcp.Description("ISO-8601 UTC lower bound on message timestamp (inclusive)")),
		mcp.WithString("before", mcp.Description("ISO-8601 UTC upper bound on message timestamp (inclusive)")),
		mcp.WithString("sender_phone_number", mcp.Description("filter to messages sent by this phone or JID ("+jidDesc+")")),
		mcp.WithString("chat_jid", mcp.Description("filter to messages in this chat ("+jidDesc+")")),
		mcp.WithString("query", mcp.Description("case-insensitive substring to match within message body")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20), mcp.Description("max messages to return (default 20, capped at 100 server-side)")),
		mcp.WithNumber("page", mcp.DefaultNumber(0), mcp.Description("zero-based page index for paging through results (default 0)")),
		mcp.WithString("cursor", mcp.Description("opaque cursor from a prior format=json response; resume keyset pagination from that message instead of using page/offset")),
		mcp.WithString("format", mcp.Enum("text", "json"), mcp.Description("output format: `text` (default) returns a formatted block; `json` returns {messages, next_cursor, has_more, count}")),
		mcp.WithBoolean("include_context", mcp.DefaultBool(true), mcp.Description("if true, attach a few surrounding messages to each match (defaults to true)")),
		mcp.WithNumber("context_before", mcp.DefaultNumber(1), mcp.Description("messages to include before each match (default 1, capped at 20 server-side)")),
		mcp.WithNumber("context_after", mcp.DefaultNumber(1), mcp.Description("messages to include after each match (default 1, capped at 20 server-side)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listMessagesArgs) (*mcp.CallToolResult, error) {
		// Clamp parameters to server-side upper bounds.
		limit := a.Limit
		if limit > 100 {
			limit = 100
		}
		ctxBefore := a.ContextBefore
		if ctxBefore > 20 {
			ctxBefore = 20
		}
		ctxAfter := a.ContextAfter
		if ctxAfter > 20 {
			ctxAfter = 20
		}

		useJSON := a.Format == "json"
		useCursor := a.Cursor != ""

		if useCursor || useJSON {
			// Keyset pagination path: stable, no offset drift.
			params := store.ListMessagesParams{
				SenderPhone:    a.SenderPhoneNumber,
				ChatJID:        a.ChatJID,
				Query:          a.Query,
				Limit:          limit,
				Cursor:         a.Cursor,
				IncludeContext: false, // context expansion is incompatible with cursor pagination
				ContextBefore:  ctxBefore,
				ContextAfter:   ctxAfter,
			}
			if a.After != "" {
				t, err := parseTimestamp(a.After)
				if err != nil {
					return errorResult(ErrInvalidParameter("after", "valid ISO-8601 UTC timestamp", a.After)), nil
				}
				params.After = t
			}
			if a.Before != "" {
				t, err := parseTimestamp(a.Before)
				if err != nil {
					return errorResult(ErrInvalidParameter("before", "valid ISO-8601 UTC timestamp", a.Before)), nil
				}
				params.Before = t
			}
			result, err := s.client.Store().ListMessagesCursor(ctx, params)
			if err != nil {
				s.auditLog("list_messages", "", "", "list_messages", "error", err.Error())
				return errorResult(ErrInternal(err.Error())), nil
			}
			if useJSON {
				return resultJSON(result)
			}
			// Text format with cursor: still render the page, but no cursor
			// metadata is embeddable in plain text. Switch to format=json to
			// get next_cursor/has_more.
			return mcp.NewToolResultText(s.client.Store().FormatMessagesList(ctx, result.Messages, true)), nil
		}

		// Legacy page/offset path (unchanged behavior).
		params := store.ListMessagesParams{
			SenderPhone:    a.SenderPhoneNumber,
			ChatJID:        a.ChatJID,
			Query:          a.Query,
			Limit:          limit,
			Page:           a.Page,
			IncludeContext: a.IncludeContext == nil || *a.IncludeContext,
			ContextBefore:  ctxBefore,
			ContextAfter:   ctxAfter,
		}
		if a.After != "" {
			t, err := parseTimestamp(a.After)
			if err != nil {
				return errorResult(ErrInvalidParameter("after", "valid ISO-8601 UTC timestamp", a.After)), nil
			}
			params.After = t
		}
		if a.Before != "" {
			t, err := parseTimestamp(a.Before)
			if err != nil {
				return errorResult(ErrInvalidParameter("before", "valid ISO-8601 UTC timestamp", a.Before)), nil
			}
			params.Before = t
		}
		msgs, err := s.client.Store().ListMessages(ctx, params)
		if err != nil {
			s.auditLog("list_messages", "", "", "list_messages", "error", err.Error())
			return errorResult(ErrInternal(err.Error())), nil
		}
		return mcp.NewToolResultText(s.client.Store().FormatMessagesList(ctx, msgs, true)), nil
	}))
}

// -- get_read_position ------------------------------------------------------

type getReadPositionArgs struct {
	ChatJID string `json:"chat_jid"`
}

func (s *Server) registerGetReadPosition() {
	tool := mcp.NewTool("get_read_position",
		mcp.WithDescription("Return the stored read position for a chat: the last message_id and timestamp the local agent has processed, plus when the position was last updated. Read-only; no side effects. Returns sql.ErrNoRows-equivalent (a not-found JSON object) when no position has been recorded for this chat. Use mark_read_position to set or advance it."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getReadPositionArgs) (*mcp.CallToolResult, error) {
		pos, err := s.client.Store().GetReadPosition(ctx, a.ChatJID)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf(`{"chat_jid":%q,"error":"not found"}`, a.ChatJID)), nil
		}
		return resultJSON(pos)
	}))
}

// -- mark_read_position -----------------------------------------------------

type markReadPositionArgs struct {
	ChatJID    string `json:"chat_jid"`
	MessageID  string `json:"message_id"`
	Timestamp  string `json:"timestamp,omitempty"`
}

func (s *Server) registerMarkReadPosition() {
	tool := mcp.NewTool("mark_read_position",
		mcp.WithDescription("Record that the local agent has processed up to a given message in a chat. Upserts a per-chat read position (chat_jid, last_read_message_id, last_read_at) so subsequent list_messages cursor walks can resume from here instead of re-scanning from the top. Read-only from WhatsApp's perspective (no messages are sent or acked); this is purely local bookkeeping."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID to treat as the last-read position (use message_id from list_messages)")),
		mcp.WithString("timestamp", mcp.Description("ISO-8601 UTC timestamp of the last-read message; if omitted, the message's own timestamp is looked up from the cache")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a markReadPositionArgs) (*mcp.CallToolResult, error) {
		var ts time.Time
		if a.Timestamp != "" {
			var err error
			ts, err = parseTimestamp(a.Timestamp)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid 'timestamp': %v", err)), nil
			}
		}
		if err := s.client.Store().SetReadPosition(ctx, a.ChatJID, a.MessageID, ts); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		pos, _ := s.client.Store().GetReadPosition(ctx, a.ChatJID)
		return resultJSON(pos)
	}))
}

// -- list_media_messages ----------------------------------------------------

type listMediaMessagesArgs struct {
	After             string `json:"after,omitempty"`
	Before            string `json:"before,omitempty"`
	SenderPhoneNumber string `json:"sender_phone_number,omitempty"`
	ChatJID           string `json:"chat_jid,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Cursor            string `json:"cursor,omitempty"`
}

func (s *Server) registerListMediaMessages() {
	tool := mcp.NewTool("list_media_messages",
		mcp.WithDescription("List cached messages that have media attached (image, video, audio, document), with the same keyset-pagination contract as list_messages format=json. Use this to discover media message IDs for bulk download_media calls. Returns a JSON object {messages, next_cursor, has_more, count}; each message includes media_type, filename, url, direct_path, and file_length."),
		mcp.WithString("after", mcp.Description("ISO-8601 UTC lower bound on message timestamp (inclusive)")),
		mcp.WithString("before", mcp.Description("ISO-8601 UTC upper bound on message timestamp (inclusive)")),
		mcp.WithString("sender_phone_number", mcp.Description("filter to messages sent by this phone or JID ("+jidDesc+")")),
		mcp.WithString("chat_jid", mcp.Description("filter to messages in this chat ("+jidDesc+")")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20), mcp.Description("max messages to return (default 20, capped at 100 server-side)")),
		mcp.WithString("cursor", mcp.Description("opaque cursor from a prior response; resume keyset pagination from that message")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listMediaMessagesArgs) (*mcp.CallToolResult, error) {
		limit := a.Limit
		if limit > 100 {
			limit = 100
		}
		params := store.ListMessagesParams{
			SenderPhone: a.SenderPhoneNumber,
			ChatJID:     a.ChatJID,
			Limit:       limit,
			Cursor:      a.Cursor,
		}
		if a.After != "" {
			t, err := parseTimestamp(a.After)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid 'after': %v", err)), nil
			}
			params.After = t
		}
		if a.Before != "" {
			t, err := parseTimestamp(a.Before)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid 'before': %v", err)), nil
			}
			params.Before = t
		}
		result, err := s.client.Store().ListMediaMessages(ctx, params)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(result)
	}))
}

// -- list_chats -------------------------------------------------------------

type listChatsArgs struct {
	Query              string `json:"query,omitempty"`
	Limit              int    `json:"limit,omitempty"`
	Page               int    `json:"page,omitempty"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
	SortBy             string `json:"sort_by,omitempty"`
}

func (s *Server) registerListChats() {
	tool := mcp.NewTool("list_chats",
		mcp.WithDescription(offlineSafePrefix+"List cached WhatsApp chats (1:1 and group), optionally filtered by name substring and sorted by recency or alphabetic name. Read-only; no side effects. Use get_chat for a single chat by JID, list_groups for groups only. Returns a JSON array of chat objects (each with JID, name, last-message metadata when requested)."),
		mcp.WithString("query", mcp.Description("case-insensitive substring to match against chat name")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20), mcp.Description("max chats to return (default 20)")),
		mcp.WithNumber("page", mcp.DefaultNumber(0), mcp.Description("zero-based page index for paging through results (default 0)")),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true), mcp.Description("if true, include each chat's most recent message in the result (defaults to true)")),
		mcp.WithString("sort_by", mcp.DefaultString("last_active"), mcp.Enum("last_active", "name"), mcp.Description("sort order: `last_active` (most-recent first, default) or `name` (alphabetic)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listChatsArgs) (*mcp.CallToolResult, error) {
		sortBy := a.SortBy
		if sortBy == "" {
			sortBy = "last_active"
		}
		include := a.IncludeLastMessage == nil || *a.IncludeLastMessage
		chats, err := s.client.Store().ListChats(ctx, a.Query, a.Limit, a.Page, include, sortBy)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(chats)
	}))
}

// -- get_chat ---------------------------------------------------------------

type getChatArgs struct {
	ChatJID            string `json:"chat_jid"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty"`
}

func (s *Server) registerGetChat() {
	tool := mcp.NewTool("get_chat",
		mcp.WithDescription(offlineSafePrefix+"Fetch metadata for a single cached chat by JID. Read-only; no side effects. Use list_chats to discover chat JIDs, or get_group_info for live group metadata. Returns a JSON object describing the chat (JID, name, last-message metadata when requested), or the JSON literal `null` when the chat is not in the cache."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("include_last_message", mcp.DefaultBool(true), mcp.Description("if true, include the chat's most recent message in the result (defaults to true)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getChatArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return errorResult(ErrInvalidParameter("chat_jid", "non-empty string", "empty string")), nil
		}
		include := a.IncludeLastMessage == nil || *a.IncludeLastMessage
		chat, err := s.client.Store().GetChat(ctx, a.ChatJID, include)
		if err != nil {
			s.auditLog("get_chat", a.ChatJID, "", "get_chat", "error", err.Error())
			return errorResult(ErrInternal(err.Error())), nil
		}
		if chat == nil {
			return mcp.NewToolResultText("null"), nil
		}
		return resultJSON(chat)
	}))
}

// -- get_message_context ----------------------------------------------------

type getMessageContextArgs struct {
	MessageID string `json:"message_id"`
	Before    int    `json:"before,omitempty"`
	After     int    `json:"after,omitempty"`
}

func (s *Server) registerGetMessageContext() {
	tool := mcp.NewTool("get_message_context",
		mcp.WithDescription(offlineSafePrefix+"Fetch a specific cached message and the surrounding messages in its chat. Read-only; no side effects. Use list_messages for searching across many chats. Returns a JSON object with the target message and arrays of messages before and after it."),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID of the target message (use `message_id` from list_messages)")),
		mcp.WithNumber("before", mcp.DefaultNumber(5), mcp.Description("messages to fetch before the target (default 5; non-positive values fall back to the default)")),
		mcp.WithNumber("after", mcp.DefaultNumber(5), mcp.Description("messages to fetch after the target (default 5; non-positive values fall back to the default)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getMessageContextArgs) (*mcp.CallToolResult, error) {
		before, after := a.Before, a.After
		if before <= 0 {
			before = 5
		}
		if after <= 0 {
			after = 5
		}
		ctxResult, err := s.client.Store().GetMessageContext(ctx, a.MessageID, before, after)
		if err != nil {
			s.auditLog("get_message_context", "", a.MessageID, "get_message_context", "error", err.Error())
			return errorResult(ErrInternal(err.Error())), nil
		}
		return resultJSON(ctxResult)
	}))
}

// -- is_on_whatsapp ---------------------------------------------------------

type isOnWhatsAppArgs struct {
	Phones []string `json:"phones"`
}

func (s *Server) registerIsOnWhatsApp() {
	tool := mcp.NewTool("is_on_whatsapp",
		mcp.WithDescription("Query WhatsApp servers to check which of the supplied phone numbers are registered on WhatsApp; the queried users are not notified. Read-only with no chat side effects. Use before send_message when you only have a phone number and need to confirm the contact exists. Returns a JSON object keyed by input phone, each value `{is_in: bool, jid: string, verified_name?: string}` (or similar)."),
		mcp.WithArray("phones", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("phone numbers to check; digits only with no `+` prefix, spaces, or punctuation (e.g. `447700900000`); must be non-empty")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, req mcp.CallToolRequest, a isOnWhatsAppArgs) (*mcp.CallToolResult, error) {
		if len(a.Phones) == 0 {
			return errorResult(ErrInvalidParameter("phones", "non-empty array", "empty array")), nil
		}
		ctx = withRateLimitOverride(ctx, req)
		m, err := s.client.IsOnWhatsApp(ctx, a.Phones)
		if err != nil {
			s.auditLog("is_on_whatsapp", "", "", "is_on_whatsapp", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error checking WhatsApp registration", true)), nil
		}
		return resultJSON(m)
	}))
}

// -- get_status -------------------------------------------------------------

func (s *Server) registerGetStatus() {
	tool := mcp.NewTool("get_status",
		mcp.WithDescription("Report whether the embedded WhatsApp bridge is connected and which account it is paired with. Read-only; no side effects. Call this first when other tools fail with auth or connection errors. Returns a JSON object `{connected, paired, own_jid?, own_phone?, hint?}` — `hint` includes the URL of the local pairing UI when not yet paired."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status := map[string]any{
			"connected": s.client.IsConnected(),
		}
		if wa := s.client.WA(); wa != nil && wa.Store != nil && wa.Store.ID != nil {
			status["own_jid"] = wa.Store.ID.String()
			status["own_phone"] = wa.Store.ID.User
			status["paired"] = true
		} else {
			status["paired"] = false
			status["hint"] = "Not paired. Open /pair on this server in a browser to scan QR (default: http://127.0.0.1:8765/pair)."
		}
		return resultJSON(status)
	})
}

// -- pairing_status ---------------------------------------------------------

// registerPairingStatus exposes the live device-pairing state as a structured
// "setup_state" envelope. Where get_status is human-oriented, this returns a
// machine-stable shape keyed by `state` so a polling supervisor (e.g. the Den
// daemon) can surface the WhatsApp linking QR to its own clients.
//
// The bound client is consulted only once the cache reports a paired device,
// keeping the awaiting_qr / error envelopes free of any whatsmeow dependency.
func (s *Server) registerPairingStatus() {
	tool := mcp.NewTool("pairing_status",
		mcp.WithDescription("Report the WhatsApp device-pairing state as a structured `setup_state` envelope for programmatic supervisors that surface the linking QR to their own clients (e.g. a polling daemon). Read-only; no side effects. Returns a JSON object `{type:\"setup_state\", state, …}` where `state` is `ready` (paired and connected; adds own_jid/own_phone), `awaiting_qr` (unpaired; adds qr_payload when a pairing code is cached), or `error` (pairing cache unavailable)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var ownJID, ownPhone string
		ready := false
		// Short-circuits before touching s.client unless we are paired, so a
		// nil cache or unpaired state never dereferences the WhatsApp client.
		if s.cache != nil && s.cache.Paired() && s.client.IsConnected() {
			if wa := s.client.WA(); wa != nil && wa.Store != nil && wa.Store.ID != nil {
				ownJID = wa.Store.ID.String()
				ownPhone = wa.Store.ID.User
				ready = true
			}
		}

		resp := map[string]any{"type": "setup_state"}
		switch {
		case ready:
			resp["state"] = "ready"
			resp["own_jid"] = ownJID
			resp["own_phone"] = ownPhone
		case s.cache == nil:
			resp["state"] = "error"
			resp["detail"] = "pairing cache unavailable"
		case s.cache.QR() != "":
			resp["state"] = "awaiting_qr"
			resp["qr_payload"] = s.cache.QR()
			resp["detail"] = "Scan with WhatsApp → Linked devices"
		default:
			resp["state"] = "awaiting_qr"
			resp["detail"] = "Generating pairing code…"
		}
		return resultJSON(resp)
	})
}

// -- list_labels ----------------------------------------------------------

func (s *Server) registerListLabels() {
	tool := mcp.NewTool("list_labels",
		mcp.WithDescription("List all WhatsApp Business labels known to the paired account. Labels are a WhatsApp Business feature; personal accounts may have none. Read-only; no side effects. Returns a JSON array of label objects (each with label_id, name, color)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		labels, err := s.client.Store().ListLabels()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(labels)
	})
}

// -- list_chats_by_label --------------------------------------------------

type listChatsByLabelArgs struct {
	LabelID string `json:"label_id"`
}

func (s *Server) registerListChatsByLabel() {
	tool := mcp.NewTool("list_chats_by_label",
		mcp.WithDescription("List all chat JIDs that have the given WhatsApp Business label. Read-only; no side effects. Returns a JSON array of chat JID strings."),
		mcp.WithString("label_id", mcp.Required(), mcp.Description("WhatsApp Business label ID (use `list_labels` to discover label IDs)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, a listChatsByLabelArgs) (*mcp.CallToolResult, error) {
		if a.LabelID == "" {
			return mcp.NewToolResultError("label_id is required"), nil
		}
		jids, err := s.client.Store().ListChatsByLabel(a.LabelID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(jids)
	}))
}

// -- get_audit_log ----------------------------------------------------------

type getAuditLogArgs struct {
	Limit int `json:"limit,omitempty"`
}

func (s *Server) registerGetAuditLog() {
	tool := mcp.NewTool("get_audit_log",
		mcp.WithDescription("Returns the most recent audit log entries of agent-initiated WhatsApp actions. Each entry includes the tool name, action, result, timestamp, and any associated chat/message IDs. Read-only; no side effects. Useful for auditing what the AI agent has done on your WhatsApp account."),
		mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("maximum number of entries to return (default 50)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, a getAuditLogArgs) (*mcp.CallToolResult, error) {
		limit := int(a.Limit)
		if limit <= 0 {
			limit = 50
		}
		entries, err := s.client.Store().ListAuditLog(limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return resultJSON(entries)
	}))
}
