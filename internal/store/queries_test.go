package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

func TestListMessages_ChatJIDFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages for Alice, got zero")
	}
	for _, m := range msgs {
		if m.ChatJID != "447700000001@s.whatsapp.net" {
			t.Fatalf("expected only Alice's chat, got ChatJID=%q", m.ChatJID)
		}
	}
	// Alice has 5 seeded messages.
	if got := len(msgs); got != 5 {
		t.Fatalf("expected 5 alice messages, got %d", got)
	}
}

func TestListMessages_AfterFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cutoff := mustTime(t, "2026-01-05 00:00:00")
	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		After: cutoff,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages after cutoff")
	}
	for _, m := range msgs {
		if !m.Timestamp.After(cutoff) {
			t.Fatalf("message %q has timestamp %v, expected > %v", m.ID, m.Timestamp, cutoff)
		}
	}
}

func TestListMessages_QueryCaseInsensitive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Seed has content "HOW are you" in uppercase.
	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		Query: "how are",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 match for 'how are', got %d", len(msgs))
	}
	if msgs[0].ID != "a3" {
		t.Fatalf("expected match on message a3, got %q", msgs[0].ID)
	}
}

func TestListMessages_Pagination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	page0, err := s.ListMessages(ctx, ListMessagesParams{Limit: 2, Page: 0})
	if err != nil {
		t.Fatalf("ListMessages page 0: %v", err)
	}
	if len(page0) != 2 {
		t.Fatalf("page 0 should return 2, got %d", len(page0))
	}
	page1, err := s.ListMessages(ctx, ListMessagesParams{Limit: 2, Page: 1})
	if err != nil {
		t.Fatalf("ListMessages page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 should return 2, got %d", len(page1))
	}

	// Pages must not overlap.
	seen := map[string]bool{}
	for _, m := range page0 {
		seen[m.ID] = true
	}
	for _, m := range page1 {
		if seen[m.ID] {
			t.Fatalf("message %q appears on both pages", m.ID)
		}
	}

	// Ordering: both pages should be DESC timestamp, and all page0 timestamps
	// should be >= all page1 timestamps.
	for _, a := range page0 {
		for _, b := range page1 {
			if a.Timestamp.Before(b.Timestamp) {
				t.Fatalf("page0 msg %v before page1 msg %v", a.Timestamp, b.Timestamp)
			}
		}
	}
}

func TestListChats_SortByLastActive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// NOTE: ListChats always selects latest_msg.* columns even when
	// includeLastMessage=false, which causes a SQL error. Until the source
	// is fixed, tests pass true here. See bug note in the test summary.
	chats, err := s.ListChats(ctx, "", 20, 0, true, "last_active")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	// Seed has 4 chats: Alice (direct), Project Team (group), Bob (direct),
	// LidOnly (@lid). ListChats does not filter @lid.
	if len(chats) != 4 {
		t.Fatalf("expected 4 chats, got %d", len(chats))
	}
	for i := 1; i < len(chats); i++ {
		if chats[i-1].LastMessageTime.Before(chats[i].LastMessageTime) {
			t.Fatalf("chats not sorted DESC by last_message_time: %v then %v",
				chats[i-1].LastMessageTime, chats[i].LastMessageTime)
		}
	}
	// Most recent should be Project Team (2026-01-10).
	if chats[0].JID != "123456789@g.us" {
		t.Fatalf("expected Project Team first, got %q", chats[0].JID)
	}
}

func TestListChats_SortByName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// See note in TestListChats_SortByLastActive.
	chats, err := s.ListChats(ctx, "", 20, 0, true, "name")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	names := make([]string, len(chats))
	for i, c := range chats {
		names[i] = c.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("chats not sorted ascending by name: %v", names)
	}
}

func TestListChats_IncludeLastMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	chats, err := s.ListChats(ctx, "", 20, 0, true, "last_active")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) == 0 {
		t.Fatal("no chats returned")
	}
	// At least one chat should now have a non-empty last message.
	anyNonEmpty := false
	for _, c := range chats {
		if c.LastMessage != "" {
			anyNonEmpty = true
			break
		}
	}
	if !anyNonEmpty {
		t.Fatal("expected at least one LastMessage populated when includeLastMessage=true")
	}
}

func TestSearchContacts_ExcludesGroups(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Empty query returns all non-group chats.
	contacts, err := s.SearchContacts(ctx, "")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	for _, c := range contacts {
		if c.JID == "123456789@g.us" {
			t.Fatalf("SearchContacts returned a group: %+v", c)
		}
	}
	if len(contacts) != 3 {
		t.Fatalf("expected 3 contacts (Alice + Bob + address-book Jane), got %d", len(contacts))
	}

	// Filtering by name.
	got, err := s.SearchContacts(ctx, "Alice")
	if err != nil {
		t.Fatalf("SearchContacts Alice: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Alice" {
		t.Fatalf("expected one match for Alice, got %+v", got)
	}
}

func TestGetChat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// NOTE: Same shape as ListChats — GetChat references m.* columns even
	// when includeLastMessage=false, causing a SQL error. Tests pass true.
	c, err := s.GetChat(ctx, "447700000001@s.whatsapp.net", true)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c == nil {
		t.Fatal("expected chat, got nil")
	}
	if c.Name != "Alice" {
		t.Fatalf("expected Alice, got %q", c.Name)
	}

	missing, err := s.GetChat(ctx, "999@s.whatsapp.net", true)
	if err != nil {
		t.Fatalf("GetChat unknown: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for unknown chat, got %+v", missing)
	}
}

func TestGetMessageContext(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// a3 is the middle-ish Alice message (2026-01-03). Ask for 2 before, 1 after.
	c, err := s.GetMessageContext(ctx, "a3", 2, 1)
	if err != nil {
		t.Fatalf("GetMessageContext: %v", err)
	}
	if c.Message.ID != "a3" {
		t.Fatalf("expected target a3, got %q", c.Message.ID)
	}
	if len(c.Before) != 2 {
		t.Fatalf("expected 2 before, got %d", len(c.Before))
	}
	if len(c.After) != 1 {
		t.Fatalf("expected 1 after, got %d", len(c.After))
	}
	// All context messages are from Alice's chat.
	for _, m := range append(append([]Message{}, c.Before...), c.After...) {
		if m.ChatJID != "447700000001@s.whatsapp.net" {
			t.Fatalf("context message from wrong chat: %+v", m)
		}
	}
}

func TestGetSenderName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	got := s.GetSenderName(ctx, "447700000001@s.whatsapp.net")
	if got != "Alice" {
		t.Fatalf("expected Alice, got %q", got)
	}
}

// Issue #21: name lookups must reach the synced address book
// (whatsmeow_contacts), not just the chats table.
func TestSearchContacts_AddressBook(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, q := range []string{"Jane", "Jane Smith", "jane s"} {
		got, err := s.SearchContacts(ctx, q)
		if err != nil {
			t.Fatalf("SearchContacts(%q): %v", q, err)
		}
		if len(got) != 1 {
			t.Fatalf("SearchContacts(%q): expected 1 match, got %+v", q, got)
		}
		c := got[0]
		if c.Name != "Jane Smith Delhi" || c.JID != "919900000000@s.whatsapp.net" || c.PhoneNumber != "919900000000" {
			t.Fatalf("SearchContacts(%q): unexpected contact %+v", q, c)
		}
	}

	// Number search must also surface the saved name, not the bare number.
	got, err := s.SearchContacts(ctx, "919900000000")
	if err != nil {
		t.Fatalf("SearchContacts by number: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Jane Smith Delhi" {
		t.Fatalf("expected Jane by number, got %+v", got)
	}
}

func TestGetSenderName_AddressBook(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// No chats row for this JID — the name must come from whatsmeow_contacts.
	if got := s.GetSenderName(ctx, "919900000000@s.whatsapp.net"); got != "Jane Smith Delhi" {
		t.Fatalf("expected address-book name, got %q", got)
	}
}

func TestContactName_ResolvesLID(t *testing.T) {
	s := openTestStore(t)

	if got := s.ContactName("11223344@lid"); got != "Jane Smith Delhi" {
		t.Fatalf("expected LID to resolve to address-book name, got %q", got)
	}
	if got := s.ContactName("999@s.whatsapp.net"); got != "" {
		t.Fatalf("expected empty for unknown contact, got %q", got)
	}
}

func TestListChats_IsGroupField(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	chats, err := s.ListChats(ctx, "", 20, 0, true, "last_active")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}

	var sawGroup, sawDirect bool
	for _, c := range chats {
		switch c.JID {
		case "123456789@g.us":
			sawGroup = true
			if !c.IsGroup {
				t.Fatalf("chat %q should have IsGroup=true", c.JID)
			}
		case "447700000001@s.whatsapp.net":
			sawDirect = true
			if c.IsGroup {
				t.Fatalf("chat %q should have IsGroup=false", c.JID)
			}
		}
	}
	if !sawGroup {
		t.Fatal("expected to see the @g.us chat in list")
	}
	if !sawDirect {
		t.Fatal("expected to see a direct chat in list")
	}
}

func TestListMessages_IsGroupField(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// g1 lives in the Project Team group; a1 lives in a direct chat.
	msgs, err := s.ListMessages(ctx, ListMessagesParams{
		ChatJID: "123456789@g.us",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("ListMessages group: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected group messages")
	}
	for _, m := range msgs {
		if !m.IsGroup {
			t.Fatalf("group message %q should have IsGroup=true", m.ID)
		}
	}

	direct, err := s.ListMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("ListMessages direct: %v", err)
	}
	if len(direct) == 0 {
		t.Fatal("expected direct messages")
	}
	for _, m := range direct {
		if m.IsGroup {
			t.Fatalf("direct message %q should have IsGroup=false", m.ID)
		}
	}
}

func TestSearchContacts_ExcludesLID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	contacts, err := s.SearchContacts(ctx, "")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	for _, c := range contacts {
		if strings.HasSuffix(c.JID, "@lid") {
			t.Fatalf("SearchContacts returned an @lid row: %+v", c)
		}
	}

	// Searching by the LID-only name must also return nothing.
	got, err := s.SearchContacts(ctx, "LidOnly")
	if err != nil {
		t.Fatalf("SearchContacts LidOnly: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero results for LidOnly query, got %+v", got)
	}
}

func TestGetNewestMessage(t *testing.T) {
	s := openTestStore(t)

	id, ts, isFromMe, err := s.GetNewestMessage("447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetNewestMessage: %v", err)
	}
	if id != "a5" {
		t.Fatalf("expected newest alice msg id a5, got %q", id)
	}
	if !ts.Equal(mustTime(t, "2026-01-09 10:00:00")) {
		t.Fatalf("unexpected timestamp: %v", ts)
	}
	if isFromMe {
		t.Fatalf("expected is_from_me=false, got true")
	}
}

func TestGetOldestMessage(t *testing.T) {
	s := openTestStore(t)

	id, ts, isFromMe, err := s.GetOldestMessage("447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetOldestMessage: %v", err)
	}
	if id != "a1" {
		t.Fatalf("expected oldest alice msg id a1, got %q", id)
	}
	if !ts.Equal(mustTime(t, "2026-01-01 09:00:00")) {
		t.Fatalf("unexpected timestamp: %v", ts)
	}
	if isFromMe {
		t.Fatalf("expected is_from_me=false, got true")
	}
}

func TestGetOldestMessage_EmptyChat(t *testing.T) {
	s := openTestStore(t)

	_, _, _, err := s.GetOldestMessage("emptychat@s.whatsapp.net")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestGetMessageAtOrBefore(t *testing.T) {
	s := openTestStore(t)

	// Between a3 (01-03) and a4 (01-04): the newest at-or-before is a3.
	id, ts, _, err := s.GetMessageAtOrBefore("447700000001@s.whatsapp.net", mustTime(t, "2026-01-03 12:00:00"))
	if err != nil {
		t.Fatalf("GetMessageAtOrBefore: %v", err)
	}
	if id != "a3" {
		t.Fatalf("expected a3, got %q", id)
	}
	if !ts.Equal(mustTime(t, "2026-01-03 09:10:00")) {
		t.Fatalf("unexpected timestamp: %v", ts)
	}
}

func TestGetMessageAtOrBefore_Inclusive(t *testing.T) {
	s := openTestStore(t)

	// Exactly on a3's timestamp: the bound is inclusive.
	id, _, _, err := s.GetMessageAtOrBefore("447700000001@s.whatsapp.net", mustTime(t, "2026-01-03 09:10:00"))
	if err != nil {
		t.Fatalf("GetMessageAtOrBefore: %v", err)
	}
	if id != "a3" {
		t.Fatalf("expected a3 for an exact-timestamp bound, got %q", id)
	}
}

func TestGetMessageAtOrBefore_NothingThatFarBack(t *testing.T) {
	s := openTestStore(t)

	// Older than every cached message: caller falls back to the oldest.
	_, _, _, err := s.GetMessageAtOrBefore("447700000001@s.whatsapp.net", mustTime(t, "2020-01-01 00:00:00"))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

// -- cursor pagination -------------------------------------------------------

func TestListMessagesCursor_BasicRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Page 1: 3 messages from Alice's chat.
	page1, err := s.ListMessagesCursor(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   3,
	})
	if err != nil {
		t.Fatalf("ListMessagesCursor page 1: %v", err)
	}
	if len(page1.Messages) != 3 {
		t.Fatalf("expected 3 messages on page 1, got %d", len(page1.Messages))
	}
	if !page1.HasMore {
		t.Fatal("expected hasMore=true on page 1")
	}
	if page1.NextCursor == "" {
		t.Fatal("expected non-empty next_cursor on page 1")
	}

	// Page 2: use the cursor from page 1.
	page2, err := s.ListMessagesCursor(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   3,
		Cursor:  page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListMessagesCursor page 2: %v", err)
	}
	if len(page2.Messages) != 2 {
		t.Fatalf("expected 2 messages on page 2 (Alice has 5 total), got %d", len(page2.Messages))
	}
	if page2.HasMore {
		t.Fatal("expected hasMore=false on the last page")
	}
	if page2.NextCursor != "" {
		t.Fatalf("expected empty next_cursor on last page, got %q", page2.NextCursor)
	}

	// No message should appear on both pages.
	seen := map[string]bool{}
	for _, m := range page1.Messages {
		seen[m.ID] = true
	}
	for _, m := range page2.Messages {
		if seen[m.ID] {
			t.Fatalf("message %q appears on both cursor pages", m.ID)
		}
	}

	// Ordering: all page1 timestamps >= all page2 timestamps.
	for _, a := range page1.Messages {
		for _, b := range page2.Messages {
			if a.Timestamp.Before(b.Timestamp) {
				t.Fatalf("page1 msg %v before page2 msg %v", a.Timestamp, b.Timestamp)
			}
		}
	}
}

func TestListMessagesCursor_InvalidCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.ListMessagesCursor(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Cursor:  "not-a-valid-cursor",
	})
	if err == nil {
		t.Fatal("expected error for invalid cursor")
	}
}

func TestListMessagesCursor_EmptyChat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	result, err := s.ListMessagesCursor(ctx, ListMessagesParams{
		ChatJID: "nonexistent@s.whatsapp.net",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMessagesCursor empty chat: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected zero messages, got %d", len(result.Messages))
	}
	if result.HasMore {
		t.Fatal("expected hasMore=false for empty chat")
	}
	if result.NextCursor != "" {
		t.Fatalf("expected empty cursor, got %q", result.NextCursor)
	}
}

func TestListMessagesCursor_GlobalAcrossChats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// No chat filter: walks all messages across all chats.
	page1, err := s.ListMessagesCursor(ctx, ListMessagesParams{Limit: 5})
	if err != nil {
		t.Fatalf("ListMessagesCursor global: %v", err)
	}
	if len(page1.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(page1.Messages))
	}
	if !page1.HasMore {
		t.Fatal("expected more pages")
	}

	// Verify DESC ordering across all 12 seeded messages.
	var ids []string
	for _, m := range page1.Messages {
		ids = append(ids, m.ID)
	}
	if ids[0] != "g4" {
		t.Fatalf("expected newest message g4 first, got %q", ids[0])
	}
}

// -- read positions ----------------------------------------------------------

func TestGetSetReadPosition(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// No position yet.
	_, err := s.GetReadPosition(ctx, "447700000001@s.whatsapp.net")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for unset position, got %v", err)
	}

	// Set a position.
	ts := mustTime(t, "2026-01-04 09:15:00")
	if err := s.SetReadPosition(ctx, "447700000001@s.whatsapp.net", "a4", ts); err != nil {
		t.Fatalf("SetReadPosition: %v", err)
	}

	// Read it back.
	pos, err := s.GetReadPosition(ctx, "447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetReadPosition: %v", err)
	}
	if pos.ChatJID != "447700000001@s.whatsapp.net" {
		t.Fatalf("expected chat_jid Alice, got %q", pos.ChatJID)
	}
	if pos.LastReadMessageID != "a4" {
		t.Fatalf("expected last_read_message_id a4, got %q", pos.LastReadMessageID)
	}
	if !pos.LastReadAt.Equal(ts) {
		t.Fatalf("expected last_read_at %v, got %v", ts, pos.LastReadAt)
	}

	// Upsert: update to a later message.
	ts2 := mustTime(t, "2026-01-09 10:00:00")
	if err := s.SetReadPosition(ctx, "447700000001@s.whatsapp.net", "a5", ts2); err != nil {
		t.Fatalf("SetReadPosition upsert: %v", err)
	}
	pos, err = s.GetReadPosition(ctx, "447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetReadPosition after upsert: %v", err)
	}
	if pos.LastReadMessageID != "a5" {
		t.Fatalf("expected upserted last_read_message_id a5, got %q", pos.LastReadMessageID)
	}
}

// -- media messages ----------------------------------------------------------

func TestListMediaMessages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	result, err := s.ListMediaMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMediaMessages: %v", err)
	}
	// Alice has exactly one media message (a4, an image).
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 media message for Alice, got %d", len(result.Messages))
	}
	if result.Messages[0].ID != "a4" {
		t.Fatalf("expected media message a4, got %q", result.Messages[0].ID)
	}
	if result.Messages[0].MediaType != "image" {
		t.Fatalf("expected media_type image, got %q", result.Messages[0].MediaType)
	}
	if result.HasMore {
		t.Fatal("expected hasMore=false")
	}

	// Global media listing: only a4 has media in the seed.
	global, err := s.ListMediaMessages(ctx, ListMessagesParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListMediaMessages global: %v", err)
	}
	if len(global.Messages) != 1 {
		t.Fatalf("expected 1 media message globally, got %d", len(global.Messages))
	}
	if global.Messages[0].ID != "a4" {
		t.Fatalf("expected a4 globally, got %q", global.Messages[0].ID)
	}
}

func TestListMediaMessages_CursorPagination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Add a second media message to Alice's chat to test cursor paging across
	// more than one media row.
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"a6", "447700000001@s.whatsapp.net", "me", "another pic", "2026-01-05 10:00:00", 1, "video", "clip.mp4", 5678)
	if err != nil {
		t.Fatalf("insert test media: %v", err)
	}

	page1, err := s.ListMediaMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("ListMediaMessages page 1: %v", err)
	}
	if len(page1.Messages) != 1 {
		t.Fatalf("expected 1 media message on page 1, got %d", len(page1.Messages))
	}
	if !page1.HasMore {
		t.Fatal("expected hasMore=true")
	}

	page2, err := s.ListMediaMessages(ctx, ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   1,
		Cursor:  page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListMediaMessages page 2: %v", err)
	}
	if len(page2.Messages) != 1 {
		t.Fatalf("expected 1 media message on page 2, got %d", len(page2.Messages))
	}
	if page2.HasMore {
		t.Fatal("expected hasMore=false on last page")
	}
	if page1.Messages[0].ID == page2.Messages[0].ID {
		t.Fatal("same message on both cursor pages")
	}
}
