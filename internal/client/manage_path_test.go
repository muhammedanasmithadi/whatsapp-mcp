package client

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

type manageProbe struct {
	wa       *whatsmeow.Client
	client   *Client
	sent     []*waE2E.Message
	failWith error
	online   bool
}

func newManageProbe(t *testing.T) (*Client, *manageProbe) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	own := types.NewJID("447700900123", types.DefaultUserServer)
	wa := whatsmeow.NewClient(&wmstore.Device{ID: &own}, NewStderrLogger("test", "ERROR", false))
	p := &manageProbe{wa: wa, online: true}
	p.client = &Client{
		wa:              wa,
		store:           st,
		redactor:        &security.Redactor{},
		log:             NewStderrLogger("test", "ERROR", false),
		socketConnected: func() bool { return p.online },
		sendMessage: func(_ context.Context, _ types.JID, m *waE2E.Message) (whatsmeow.SendResponse, error) {
			if p.failWith != nil {
				return whatsmeow.SendResponse{}, p.failWith
			}
			p.sent = append(p.sent, m)
			return whatsmeow.SendResponse{ID: "ACKMANAGE1"}, nil
		},
	}
	return p.client, p
}

func (p *manageProbe) last() *waE2E.Message {
	if len(p.sent) == 0 {
		return nil
	}
	return p.sent[len(p.sent)-1]
}

func seedInbound(t *testing.T, p *manageProbe, id, content string) {
	t.Helper()
	if err := p.client.store.StoreChat(chatJID, "probe chat", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := p.client.store.StoreMessage(context.Background(), store.Message{
		ID: id, ChatJID: chatJID, Sender: "447700900123", Content: content, Timestamp: time.Now(),
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func TestReactionPostsProtocolMessageAndMirrors(t *testing.T) {
	c, p := newManageProbe(t)
	seedInbound(t, p, "REACT1", "react to me")
	reaction := p.last

	if err := c.SendReaction(context.Background(), chatJID, "REACT1", "447700900123@s.whatsapp.net", "👍"); err != nil {
		t.Fatalf("SendReaction: %v", err)
	}

	rx := reaction().GetReactionMessage()
	if rx == nil {
		t.Fatal("no reaction message sent")
	}
	if rx.GetText() != "👍" {
		t.Errorf("reaction text = %q", rx.GetText())
	}
	key := rx.GetKey()
	if key.GetID() != "REACT1" {
		t.Errorf("reaction key = %q, want REACT1", key.GetID())
	}
	if key.GetRemoteJID() != chatJID {
		t.Errorf("reaction chat = %q", key.GetRemoteJID())
	}

	cached, err := c.store.GetMessage(context.Background(), "ACKMANAGE1", chatJID)
	if err != nil || cached == nil {
		t.Fatalf("reaction was not cached: %v", err)
	}
	if cached.Content != "[reaction] 👍" || !cached.IsFromMe {
		t.Errorf("cached reaction = %+v", cached)
	}
}

func TestReactionClearedMirrorsAsCleared(t *testing.T) {
	c, p := newManageProbe(t)
	seedInbound(t, p, "REACT2", "unreact me")

	if err := c.SendReaction(context.Background(), chatJID, "REACT2", "447700900123@s.whatsapp.net", ""); err != nil {
		t.Fatalf("SendReaction: %v", err)
	}
	if got := p.last().GetReactionMessage().GetText(); got != "" {
		t.Errorf("cleared reaction text = %q", got)
	}
	cached, _ := c.store.GetMessage(context.Background(), "ACKMANAGE1", chatJID)
	if cached == nil || cached.Content != "[reaction cleared]" {
		t.Errorf("cached = %+v, want the cleared marker", cached)
	}
}

func TestReplyCarriesTheQuotedStanzaAndParticipant(t *testing.T) {
	c, p := newManageProbe(t)
	seedInbound(t, p, "REPLY1", "original")

	id, err := c.SendReply(context.Background(), chatJID, "REPLY1", "447700900123@s.whatsapp.net", "my reply")
	if err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	if id != "ACKMANAGE1" {
		t.Errorf("returned id = %q", id)
	}

	ext := p.last().GetExtendedTextMessage()
	if ext.GetText() != "my reply" {
		t.Errorf("body = %q", ext.GetText())
	}
	ctx := ext.GetContextInfo()
	if ctx.GetStanzaID() != "REPLY1" {
		t.Errorf("quoted stanza = %q, want REPLY1", ctx.GetStanzaID())
	}
	if participant := ctx.GetParticipant(); participant != "447700900123@s.whatsapp.net" {
		t.Errorf("quoted participant = %q, want the non-AD JID", participant)
	}
}

func TestEditWrapsTheBodyAndUpdatesTheCache(t *testing.T) {
	c, p := newManageProbe(t)
	seedInbound(t, p, "EDIT1", "before")

	id, err := c.EditMessage(context.Background(), chatJID, "EDIT1", "after")
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if id != "ACKMANAGE1" {
		t.Errorf("returned id = %q", id)
	}

	proto := p.last().GetEditedMessage().GetMessage().GetProtocolMessage()
	if proto == nil || proto.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT {
		t.Fatalf("edit was not sent as a MESSAGE_EDIT protocol message: %+v", p.last())
	}
	if edited := proto.GetEditedMessage(); edited.GetConversation() != "after" {
		t.Errorf("edited body = %q, want after", edited.GetConversation())
	}

	cached, _ := c.store.GetMessage(context.Background(), "EDIT1", chatJID)
	if cached == nil || cached.Content != "after" {
		t.Errorf("cached = %+v, want the edited body", cached)
	}
}

func TestRevokeSendsRevokeAndMirrorsTheCache(t *testing.T) {
	c, p := newManageProbe(t)
	seedInbound(t, p, "REVOKE1", "delete me")

	if err := c.DeleteMessage(context.Background(), chatJID, "REVOKE1", "447700900123@s.whatsapp.net"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	proto := p.last().GetProtocolMessage()
	if proto == nil || proto.GetType() != waE2E.ProtocolMessage_REVOKE {
		t.Fatalf("revoke was not sent as a REVOKE protocol message: %+v", p.last())
	}
	if proto.GetKey().GetID() != "REVOKE1" {
		t.Errorf("revoked key = %q", proto.GetKey().GetID())
	}
	cached, _ := c.store.GetMessage(context.Background(), "REVOKE1", chatJID)
	if cached == nil || cached.Content != "[deleted]" {
		t.Errorf("cached = %+v, want the [deleted] marker", cached)
	}
}

func TestForwardCarriesTheCachedBodyAndForwardingScore(t *testing.T) {
	c, p := newManageProbe(t)
	source := "447700900999@s.whatsapp.net"
	if err := c.store.StoreChat(source, "source chat", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := c.store.StoreMessage(context.Background(), store.Message{
		ID: "FWD1", ChatJID: source, Sender: "447700900999", Content: "forward this", Timestamp: time.Now(),
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed source message: %v", err)
	}

	if err := c.ForwardMessage(context.Background(), chatJID, source, "FWD1"); err != nil {
		t.Fatalf("ForwardMessage: %v", err)
	}
	ext := p.last().GetExtendedTextMessage()
	if ext.GetText() != "forward this" {
		t.Errorf("forwarded body = %q", ext.GetText())
	}
	ctx := ext.GetContextInfo()
	if ctx.GetForwardingScore() != 1 || !ctx.GetIsForwarded() {
		t.Errorf("forwarding score = %d, isForwarded = %v", ctx.GetForwardingScore(), ctx.GetIsForwarded())
	}
	if ctx.GetRemoteJID() != source {
		t.Errorf("forwarded from = %q, want %q", ctx.GetRemoteJID(), source)
	}
}

func TestForwardRejectsAnUncachedMessage(t *testing.T) {
	c, p := newManageProbe(t)
	if err := c.ForwardMessage(context.Background(), chatJID, "447700900999@s.whatsapp.net", "NEVERSYNCED"); err == nil {
		t.Fatal("forwarding an uncached message succeeded")
	}
	if p.last() != nil {
		t.Error("a rejected forward still sent a message")
	}
}

func TestManageToolsRejectBadInputAndDisconnection(t *testing.T) {
	c, p := newManageProbe(t)
	seedInbound(t, p, "MANAGE1", "react to me")

	if err := c.SendReaction(context.Background(), "447700900123", "MANAGE1", "447700900123@s.whatsapp.net", "👍"); err != nil {
		t.Fatalf("a bare-phone chat JID must resolve like the recipient parser: %v", err)
	}
	if got := p.last().GetReactionMessage().GetKey().GetRemoteJID(); got != "447700900123@s.whatsapp.net" {
		t.Errorf("reaction chat = %q, want the individual server", got)
	}

	if err := c.SendReaction(context.Background(), "not a jid at all", "MANAGE1", "", "👍"); err == nil {
		t.Error("a reaction to an unparseable JID succeeded")
	}

	p.online = false
	p.sent = nil
	if _, err := c.EditMessage(context.Background(), chatJID, "EDIT1", "x"); err == nil {
		t.Error("an edit while disconnected succeeded")
	}
	if p.last() != nil {
		t.Error("a rejected call still sent a message")
	}

	p.online = true
	if err := c.DeleteMessage(context.Background(), chatJID, "REVOKE1", ""); err != nil {
		t.Errorf("a revoke without an explicit sender should default to empty: %v", err)
	}
}
