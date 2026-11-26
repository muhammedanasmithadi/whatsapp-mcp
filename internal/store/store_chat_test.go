package store

import (
	"context"
	"testing"
	"time"
)

func TestStoreChatPreservesArchivedFlag(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const jid = "15551234567@s.whatsapp.net"
	if err := s.StoreChat(jid, "First", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := s.ArchiveChat(jid); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	// An inbound message re-stores the chat row; it must not resurrect the
	// chat into the default listing.
	if err := s.StoreChat(jid, "Second", time.Now()); err != nil {
		t.Fatalf("StoreChat after archive: %v", err)
	}

	chats, err := s.ListChats(ctx, "", 10, 0, false, false, "")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	for _, c := range chats {
		if c.JID == jid {
			t.Fatalf("archived chat %q was un-archived by StoreChat", jid)
		}
	}

	archived, err := s.ListChats(ctx, "", 10, 0, false, true, "")
	if err != nil {
		t.Fatalf("ListChats archived: %v", err)
	}
	found := false
	for _, c := range archived {
		if c.JID == jid {
			found = true
			if c.Name != "Second" {
				t.Errorf("name = %q, want Second (StoreChat must still update the name)", c.Name)
			}
		}
	}
	if !found {
		t.Fatalf("archived chat %q vanished from the archived listing", jid)
	}
}

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"plain":      "plain",
		"100%":       `100\%`,
		"a_b":        `a\_b`,
		`back\slash`: `back\\slash`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
