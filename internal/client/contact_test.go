package client

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

func seedStoreWithChat(t *testing.T, jid string, withInbound bool) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.StoreChat(jid, "test", time.Now().UTC()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if withInbound {
		if err := s.StoreMessage(context.Background(), store.Message{
			ID:        "in-1",
			ChatJID:   jid,
			Sender:    jid,
			Content:   "hello from them",
			Timestamp: time.Now().UTC(),
			IsFromMe:  false,
		}, nil, nil, nil, 0); err != nil {
			t.Fatalf("seed inbound: %v", err)
		}
	}
	return s
}

func TestIsKnownContact_WithInboundHistory(t *testing.T) {
	const jid = "447700000001@s.whatsapp.net"
	c := newClientWithStore(t, seedStoreWithChat(t, jid, true))

	if !c.IsKnownContact(types.JID{User: "447700000001", Server: types.DefaultUserServer}) {
		t.Fatal("a JID with prior inbound history should be a known contact")
	}
}

func TestIsKnownContact_NoInboundIsCold(t *testing.T) {
	const jid = "447700000001@s.whatsapp.net"
	s := seedStoreWithChat(t, jid, false)
	if err := s.StoreMessage(context.Background(), store.Message{
		ID: "out-1", ChatJID: jid, Sender: "me", Content: "cold outreach",
		Timestamp: time.Now().UTC(), IsFromMe: true,
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed outbound: %v", err)
	}
	c := newClientWithStore(t, s)

	if c.IsKnownContact(types.JID{User: "447700000001", Server: types.DefaultUserServer}) {
		t.Fatal("a JID we only ever messaged (no inbound) must be treated as cold")
	}
}

func TestIsKnownContact_UnknownJIDIsCold(t *testing.T) {
	c := newClientWithStore(t, seedStoreWithChat(t, "447700000001@s.whatsapp.net", true))

	if c.IsKnownContact(types.JID{User: "999999999", Server: types.DefaultUserServer}) {
		t.Fatal("a JID with no rows at all must be treated as cold")
	}
}

func TestIsKnownContact_GroupIsAlwaysKnown(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	c := newClientWithStore(t, s)

	if !c.IsKnownContact(types.JID{User: "120363000000000000", Server: types.GroupServer}) {
		t.Fatal("a group JID should always be a known context")
	}
}
