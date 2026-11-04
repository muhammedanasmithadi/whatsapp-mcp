package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// newTestServerWithStore builds an MCP server backed by a real store seeded
// with a handful of messages, so the query tools can be exercised end to end.
func newTestServerWithStore(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()

	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()

	// Seed two chats with messages spanning several days, including one media
	// message for list_media_messages coverage.
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	insertMsg := func(id, chatJID, sender, content string, ts time.Time, isFromMe bool, mediaType string) {
		_, err := s.DB().ExecContext(ctx,
			"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
			chatJID, "Test Chat", ts)
		if err != nil {
			t.Fatalf("upsert chat %s: %v", chatJID, err)
		}
		_, err = s.DB().ExecContext(ctx, `
			INSERT OR REPLACE INTO messages
			(id, chat_jid, sender, content, timestamp, is_from_me, media_type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, chatJID, sender, content, ts, isFromMe, mediaType)
		if err != nil {
			t.Fatalf("insert message %s: %v", id, err)
		}
	}

	alice := "447700000001@s.whatsapp.net"
	insertMsg("m1", alice, "447700000001@s.whatsapp.net", "hello", now.Add(-4*time.Hour), false, "")
	insertMsg("m2", alice, "me", "hi", now.Add(-3*time.Hour), true, "")
	insertMsg("m3", alice, "447700000001@s.whatsapp.net", "how are you", now.Add(-2*time.Hour), false, "")
	insertMsg("m4", alice, "me", "see attached", now.Add(-1*time.Hour), true, "image")
	insertMsg("m5", alice, "447700000001@s.whatsapp.net", "nice pic", now, false, "")

	bob := "447700000002@s.whatsapp.net"
	insertMsg("n1", bob, "447700000002@s.whatsapp.net", "yo", now.Add(-5*time.Hour), false, "")
	insertMsg("n2", bob, "me", "hey", now.Add(-4*time.Hour), true, "")

	c, err := client.New(ctx, client.Config{
		Store:     s,
		StoreDir:  dir,
		Logger:    nil, // default stderr logger
	})
	if err != nil {
		s.Close()
		t.Fatalf("new client: %v", err)
	}

	return NewServer(c, nil), s, dir
}

func callListMessagesJSON(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	tool := s.MCP().GetTool("list_messages")
	if tool == nil || tool.Handler == nil {
		t.Fatal("list_messages not registered")
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("list_messages handler: %v", err)
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("list_messages content not text: %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("decode list_messages json %q: %v", text.Text, err)
	}
	return out
}

func callGetReadPosition(t *testing.T, s *Server, chatJID string) (map[string]any, bool) {
	t.Helper()
	tool := s.MCP().GetTool("get_read_position")
	if tool == nil || tool.Handler == nil {
		t.Fatal("get_read_position not registered")
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"chat_jid": chatJID}}})
	if err != nil {
		t.Fatalf("get_read_position handler: %v", err)
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("get_read_position content not text: %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("decode get_read_position json %q: %v", text.Text, err)
	}
	if _, isNotFound := out["error"]; isNotFound {
		return out, false
	}
	return out, true
}

func callMarkReadPosition(t *testing.T, s *Server, chatJID, messageID string) map[string]any {
	t.Helper()
	tool := s.MCP().GetTool("mark_read_position")
	if tool == nil || tool.Handler == nil {
		t.Fatal("mark_read_position not registered")
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"chat_jid": chatJID, "message_id": messageID}}})
	if err != nil {
		t.Fatalf("mark_read_position handler: %v", err)
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("mark_read_position content not text: %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("decode mark_read_position json %q: %v", text.Text, err)
	}
	return out
}

func callListMediaMessages(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	tool := s.MCP().GetTool("list_media_messages")
	if tool == nil || tool.Handler == nil {
		t.Fatal("list_media_messages not registered")
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("list_media_messages handler: %v", err)
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("list_media_messages content not text: %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("decode list_media_messages json %q: %v", text.Text, err)
	}
	return out
}

func messagesList(out map[string]any) []any {
	msgs, _ := out["messages"].([]any)
	return msgs
}

func messageID(m any) string {
	mm, _ := m.(map[string]any)
	id, _ := mm["id"].(string)
	return id
}

// -- list_messages format=json + cursor -------------------------------------

func TestListMessages_JSONCursorRoundTrip(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)

	alice := "447700000001@s.whatsapp.net"

	// Page 1: 2 messages, JSON format.
	p1 := callListMessagesJSON(t, s, map[string]any{
		"chat_jid": alice,
		"limit":    2,
		"format":   "json",
	})
	msgs := messagesList(p1)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages on page 1, got %d", len(msgs))
	}
	if !p1["has_more"].(bool) {
		t.Fatal("expected has_more=true on page 1")
	}
	nextCursor, _ := p1["next_cursor"].(string)
	if nextCursor == "" {
		t.Fatal("expected non-empty next_cursor on page 1")
	}

	// Page 2: resume from the cursor.
	p2 := callListMessagesJSON(t, s, map[string]any{
		"chat_jid": alice,
		"limit":    2,
		"cursor":   nextCursor,
		"format":   "json",
	})
	msgs2 := messagesList(p2)
	if len(msgs2) != 2 {
		t.Fatalf("expected 2 messages on page 2, got %d", len(msgs2))
	}
	// Alice has 5 messages; page 1 took 2, page 2 takes 2, one (m1) remains.
	if !p2["has_more"].(bool) {
		t.Fatal("expected has_more=true on page 2 (one message remains)")
	}
	nextCursor2, _ := p2["next_cursor"].(string)
	if nextCursor2 == "" {
		t.Fatal("expected non-empty next_cursor on page 2")
	}

	// Page 3: fetch the last remaining message.
	p3 := callListMessagesJSON(t, s, map[string]any{
		"chat_jid": alice,
		"limit":    10,
		"cursor":   nextCursor2,
		"format":   "json",
	})
	msgs3 := messagesList(p3)
	if len(msgs3) != 1 {
		t.Fatalf("expected 1 message on page 3, got %d", len(msgs3))
	}
	if p3["has_more"].(bool) {
		t.Fatal("expected has_more=false on page 3")
	}
	if p3["next_cursor"] != nil {
		t.Fatalf("expected nil next_cursor on last page, got %v", p3["next_cursor"])
	}

	// No overlap across all three pages.
	seen := map[string]bool{}
	for _, m := range append(append(msgs, msgs2...), msgs3...) {
		id := messageID(m)
		if seen[id] {
			t.Fatalf("message %q appears on more than one cursor page", id)
		}
		seen[id] = true
	}

	// DESC ordering across both pages.
	all := append(msgs, msgs2...)
	for i := 1; i < len(all); i++ {
		prev := all[i-1].(map[string]any)["timestamp"].(string)
		curr := all[i].(map[string]any)["timestamp"].(string)
		if prev < curr {
			t.Fatalf("timestamps not DESC: %s then %s", prev, curr)
		}
	}
}

func TestListMessages_TextDefaultUnchanged(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)

	alice := "447700000001@s.whatsapp.net"
	// Without format=json we still get a JSON map back because the handler
	// serializes the result; but list_messages in text mode returns formatted
	// text. The test helper always decodes JSON, so here we just verify the
	// legacy path still works by calling without format.
	tool := s.MCP().GetTool("list_messages")
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"chat_jid": alice, "limit": 50}}})
	if err != nil {
		t.Fatalf("list_messages text handler: %v", err)
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("list_messages text content not text: %T", res.Content[0])
	}
	if len(text.Text) == 0 {
		t.Fatal("expected non-empty text output")
	}
}

func TestListMessages_InvalidCursor(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)

	tool := s.MCP().GetTool("list_messages")
	if tool == nil || tool.Handler == nil {
		t.Fatal("list_messages not registered")
	}
	args := map[string]any{
		"chat_jid": "447700000001@s.whatsapp.net",
		"limit":    10,
		"cursor":   "not-a-cursor",
		"format":   "json",
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("list_messages handler: %v", err)
	}
	// The handler returns an error result (IsError=true) for an invalid cursor.
	if !res.IsError {
		t.Fatal("expected error result for invalid cursor")
	}
	if len(res.Content) == 0 {
		t.Fatal("expected error content")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("error content not text: %T", res.Content[0])
	}
	if text.Text == "" {
		t.Fatal("expected non-empty error message")
	}
}

// -- read positions ----------------------------------------------------------

func TestReadPosition_GetThenMark(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)

	alice := "447700000001@s.whatsapp.net"

	// No position yet.
	pos, found := callGetReadPosition(t, s, alice)
	if found {
		t.Fatalf("expected not-found for unset position, got %v", pos)
	}

	// Mark m4 as read.
	out := callMarkReadPosition(t, s, alice, "m4")
	chatJID, _ := out["chat_jid"].(string)
	if chatJID != alice {
		t.Fatalf("expected chat_jid %q, got %q", alice, chatJID)
	}
	lastID, _ := out["last_read_message_id"].(string)
	if lastID != "m4" {
		t.Fatalf("expected last_read_message_id m4, got %q", lastID)
	}

	// Read it back.
	pos2, found := callGetReadPosition(t, s, alice)
	if !found {
		t.Fatal("expected read position after marking")
	}
	lastID2, _ := pos2["last_read_message_id"].(string)
	if lastID2 != "m4" {
		t.Fatalf("expected m4 on read-back, got %q", lastID2)
	}

	// Mark m5 (newer) as read.
	out = callMarkReadPosition(t, s, alice, "m5")
	lastID, _ = out["last_read_message_id"].(string)
	if lastID != "m5" {
		t.Fatalf("expected upserted last_read_message_id m5, got %q", lastID)
	}

	// Invalid timestamp.
	tool := s.MCP().GetTool("mark_read_position")
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"chat_jid": alice, "message_id": "m1", "timestamp": "not-a-time"}}})
	if err != nil {
		t.Fatalf("mark_read_position handler: %v", err)
	}
	text, _ := mcp.AsTextContent(res.Content[0])
	if len(text.Text) == 0 {
		t.Fatal("expected error output for invalid timestamp")
	}
}

// -- list_media_messages -----------------------------------------------------

func TestListMediaMessages_EndToEnd(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)

	alice := "447700000001@s.whatsapp.net"

	out := callListMediaMessages(t, s, map[string]any{
		"chat_jid": alice,
		"limit":    10,
	})
	msgs := messagesList(out)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 media message for Alice, got %d", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if messageID(m) != "m4" {
		t.Fatalf("expected media message m4, got %q", messageID(m))
	}
	if mt, _ := m["media_type"].(string); mt != "image" {
		t.Fatalf("expected media_type image, got %q", mt)
	}
	if out["has_more"].(bool) {
		t.Fatal("expected has_more=false")
	}

	// Global: only m4 has media across both chats.
	global := callListMediaMessages(t, s, map[string]any{"limit": 10})
	if len(messagesList(global)) != 1 {
		t.Fatalf("expected 1 media message globally, got %d", len(messagesList(global)))
	}

	// Bob has no media.
	bobOut := callListMediaMessages(t, s, map[string]any{
		"chat_jid": "447700000002@s.whatsapp.net",
		"limit":    10,
	})
	if len(messagesList(bobOut)) != 0 {
		t.Fatalf("expected 0 media messages for Bob, got %d", len(messagesList(bobOut)))
	}
}

func TestListMediaMessages_Cursor(t *testing.T) {
	s, store, _ := newTestServerWithStore(t)

	alice := "447700000001@s.whatsapp.net"

	// Add a second media message so cursor pagination has something to page over.
	ctx := context.Background()
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	_, err := store.DB().ExecContext(ctx, `
		INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"m6", alice, "me", "another pic", now.Add(30*time.Minute), 1, "video", "clip.mp4", 5678)
	if err != nil {
		t.Fatalf("insert test media: %v", err)
	}

	p1 := callListMediaMessages(t, s, map[string]any{
		"chat_jid": alice,
		"limit":    1,
	})
	if len(messagesList(p1)) != 1 {
		t.Fatal("expected 1 media message on page 1")
	}
	if !p1["has_more"].(bool) {
		t.Fatal("expected has_more=true")
	}
	nextCursor, _ := p1["next_cursor"].(string)
	if nextCursor == "" {
		t.Fatal("expected non-empty next_cursor")
	}

	p2 := callListMediaMessages(t, s, map[string]any{
		"chat_jid": alice,
		"limit":    1,
		"cursor":   nextCursor,
	})
	if len(messagesList(p2)) != 1 {
		t.Fatalf("expected 1 media message on page 2, got %d", len(messagesList(p2)))
	}
	if p2["has_more"].(bool) {
		t.Fatal("expected has_more=false on last page")
	}
	if p2["next_cursor"] != nil {
		t.Fatalf("expected nil next_cursor on last page, got %v", p2["next_cursor"])
	}

	// The two pages must return different messages.
	if messageID(messagesList(p1)[0]) == messageID(messagesList(p2)[0]) {
		t.Fatal("same message on both cursor pages")
	}
}

// fakePairCache is a minimal pairingCache for exercising pairing_status
// without standing up the daemon's real PairCache.
type fakePairCache struct {
	paired bool
	qr     string
}

func (f fakePairCache) Paired() bool { return f.paired }
func (f fakePairCache) QR() string   { return f.qr }

// callPairingStatus invokes the pairing_status handler and decodes its JSON
// "setup_state" envelope into a map.
func callPairingStatus(t *testing.T, s *Server) map[string]any {
	t.Helper()
	tool, ok := s.MCP().ListTools()["pairing_status"]
	if !ok || tool == nil || tool.Handler == nil {
		t.Fatal("pairing_status not registered")
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("pairing_status handler: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("pairing_status returned no content")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("pairing_status content not text: %T", res.Content[0])
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(text.Text), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", text.Text, err)
	}
	if env["type"] != "setup_state" {
		t.Errorf("type = %v, want setup_state", env["type"])
	}
	return env
}

// A nil pairing cache (smoke/tests) yields the error envelope without touching
// the WhatsApp client — so passing a nil client here must not panic.
func TestPairingStatus_NilCache(t *testing.T) {
	env := callPairingStatus(t, NewServer(nil, nil))
	if env["state"] != "error" {
		t.Errorf("state = %v, want error", env["state"])
	}
	if env["detail"] != "pairing cache unavailable" {
		t.Errorf("detail = %v, want pairing cache unavailable", env["detail"])
	}
}

// An unpaired cache with a pending QR yields awaiting_qr carrying the payload.
func TestPairingStatus_AwaitingQRWithPayload(t *testing.T) {
	env := callPairingStatus(t, NewServer(nil, fakePairCache{qr: "2@QRPAYLOAD"}))
	if env["state"] != "awaiting_qr" {
		t.Fatalf("state = %v, want awaiting_qr", env["state"])
	}
	if env["qr_payload"] != "2@QRPAYLOAD" {
		t.Errorf("qr_payload = %v, want 2@QRPAYLOAD", env["qr_payload"])
	}
	if env["detail"] != "Scan with WhatsApp → Linked devices" {
		t.Errorf("detail = %v, want the scan instruction", env["detail"])
	}
}

// An unpaired cache with no QR yet yields the generating-code envelope and
// omits qr_payload.
func TestPairingStatus_AwaitingQRGenerating(t *testing.T) {
	env := callPairingStatus(t, NewServer(nil, fakePairCache{}))
	if env["state"] != "awaiting_qr" {
		t.Fatalf("state = %v, want awaiting_qr", env["state"])
	}
	if _, ok := env["qr_payload"]; ok {
		t.Errorf("qr_payload should be absent, got %v", env["qr_payload"])
	}
	if env["detail"] != "Generating pairing code…" {
		t.Errorf("detail = %v, want generating message", env["detail"])
	}
}
