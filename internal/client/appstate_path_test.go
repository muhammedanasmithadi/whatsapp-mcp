package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

type appStateProbe struct {
	patches   []appstate.PatchInfo
	failWith  error
	connected bool
	calls     []string
}

func newAppStateProbe(t *testing.T) (*Client, *appStateProbe) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := &appStateProbe{connected: true}
	c := &Client{
		store:           st,
		log:             NewStderrLogger("test", "ERROR", false),
		socketConnected: func() bool { return p.connected },
		sendAppState: func(_ context.Context, patch appstate.PatchInfo) error {
			if p.failWith != nil {
				return p.failWith
			}
			p.patches = append(p.patches, patch)
			return nil
		},
		markRead: func(context.Context, []types.MessageID, time.Time, types.JID, types.JID, ...types.ReceiptType) error {
			return nil
		},
	}
	return c, p
}

func (p *appStateProbe) last() appstate.MutationInfo {
	if len(p.patches) == 0 {
		return appstate.MutationInfo{}
	}
	return p.patches[len(p.patches)-1].Mutations[0]
}

const chatJID = "447700900123@s.whatsapp.net"

func seedChat(t *testing.T, c *Client, jid string) {
	t.Helper()
	if err := c.store.StoreChat(jid, "test chat", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
}

func TestChatStateToolsRefuseWhileDisconnected(t *testing.T) {
	c, p := newAppStateProbe(t)
	p.connected = false

	calls := map[string]func() error{
		"mute":    func() error { return c.SetChatMuted(context.Background(), chatJID, true, time.Hour) },
		"pin":     func() error { return c.SetChatPinned(context.Background(), chatJID, true) },
		"star":    func() error { return c.StarMessage(context.Background(), chatJID, "x", true) },
		"archive": func() error { return c.ArchiveChat(context.Background(), chatJID) },
		"delete":  func() error { return c.DeleteChat(context.Background(), chatJID, false) },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s succeeded while disconnected", name)
		}
	}
	if len(p.patches) != 0 {
		t.Errorf("%d patches sent while disconnected", len(p.patches))
	}
}

func TestMuteSendsMuteActionWithTheDeadline(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)

	if err := c.SetChatMuted(context.Background(), chatJID, true, 8*time.Hour); err != nil {
		t.Fatalf("SetChatMuted: %v", err)
	}

	m := p.last()
	if got := m.Index[0]; got != "mute" {
		t.Errorf("mutation index = %q, want mute", got)
	}
	if m.Index[1] != chatJID {
		t.Errorf("mutation target = %q, want %q", m.Index[1], chatJID)
	}
	if !m.Value.GetMuteAction().GetMuted() {
		t.Error("mute flag not set")
	}
	end := m.Value.GetMuteAction().GetMuteEndTimestamp()
	if end <= time.Now().UnixMilli() {
		t.Errorf("mute end = %d, want a future timestamp", end)
	}

	flags := readChatRow(t, c)
	if !flags.muted {
		t.Error("mute was not mirrored to the store")
	}
}

func TestUnmuteClearsTheMirrorAndTheDeadline(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)

	if err := c.SetChatMuted(context.Background(), chatJID, false, 0); err != nil {
		t.Fatalf("SetChatMuted: %v", err)
	}
	m := p.last()
	if m.Value.GetMuteAction().GetMuted() {
		t.Error("unmute sent a muted mutation")
	}
	if m.Value.GetMuteAction().GetMuteEndTimestamp() != 0 {
		t.Error("unmute carried an end timestamp")
	}
	if readChatRow(t, c).muted {
		t.Error("unmute left the store muted")
	}
}

func TestMuteRejectsNegativeDuration(t *testing.T) {
	c, p := newAppStateProbe(t)
	if err := c.SetChatMuted(context.Background(), chatJID, true, -time.Second); err == nil {
		t.Fatal("a negative mute duration was accepted")
	}
	if len(p.patches) != 0 {
		t.Error("a rejected mute still sent a patch")
	}
}

func TestPinSendsPinActionAndMirrors(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)

	if err := c.SetChatPinned(context.Background(), chatJID, true); err != nil {
		t.Fatalf("SetChatPinned: %v", err)
	}
	m := p.last()
	if m.Index[1] != chatJID {
		t.Errorf("mutation target = %q, want %q", m.Index[1], chatJID)
	}
	if !m.Value.GetPinAction().GetPinned() {
		t.Error("pin flag not set")
	}
	if !readChatRow(t, c).pinned {
		t.Error("pin was not mirrored to the store")
	}

	if err := c.SetChatPinned(context.Background(), chatJID, false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if readChatRow(t, c).pinned {
		t.Error("unpin left the store pinned")
	}
}

func TestStarSendsTheCachedMessageKey(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)
	id := "STARKEY1"
	if err := c.store.StoreMessage(context.Background(), store.Message{
		ID: id, ChatJID: chatJID, Sender: "447700900123", Content: "star me", Timestamp: time.Now(),
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := c.StarMessage(context.Background(), chatJID, id, true); err != nil {
		t.Fatalf("StarMessage: %v", err)
	}

	m := p.last()
	wantIndex := []string{"star", chatJID, id, "0", "0"}
	if strings.Join(m.Index, "|") != strings.Join(wantIndex, "|") {
		t.Errorf("star index = %q, want %q", m.Index, wantIndex)
	}
	if !m.Value.GetStarAction().GetStarred() {
		t.Error("star flag not set")
	}
	if !starred(t, c, id) {
		t.Error("star was not mirrored to the store")
	}
}

func TestStarRequiresACachedMessage(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)

	if err := c.StarMessage(context.Background(), chatJID, "NEVERSYNCED", true); err == nil {
		t.Fatal("starring an unsynced message succeeded")
	}
	if len(p.patches) != 0 {
		t.Error("a rejected star still sent a patch")
	}
}

func TestArchiveSendsArchiveActionBothWays(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)

	if err := c.ArchiveChat(context.Background(), chatJID); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	m := p.last()
	if !m.Value.GetArchiveChatAction().GetArchived() {
		t.Error("archive did not set the archived flag")
	}
	if !readChatRow(t, c).archived {
		t.Error("archive was not mirrored to the store")
	}

	if err := c.UnarchiveChat(context.Background(), chatJID); err != nil {
		t.Fatalf("UnarchiveChat: %v", err)
	}
	m = p.last()
	if m.Value.GetArchiveChatAction().GetArchived() {
		t.Error("unarchive sent an archived mutation")
	}
	if readChatRow(t, c).archived {
		t.Error("unarchive left the store archived")
	}
}

func TestDeleteChatCarriesTheMediaFlag(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)
	if err := c.store.StoreMessage(context.Background(), store.Message{
		ID: "DELCHAT1", ChatJID: chatJID, Sender: "447700900123", Content: "bye", Timestamp: time.Now(),
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := c.DeleteChat(context.Background(), chatJID, true); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	m := p.last()
	if m.Index[0] != "deleteChat" || m.Index[1] != chatJID || m.Index[2] != "1" {
		t.Errorf("delete index = %q, want [deleteChat %s 1]", m.Index, chatJID)
	}
	if m.Value.GetDeleteChatAction().GetMessageRange() == nil {
		t.Error("delete carried no message range")
	}
	if got, _ := c.store.GetChat(context.Background(), chatJID, false); got != nil {
		t.Error("delete did not clear the cached chat")
	}
}

func TestAppStateRejectionLeavesTheStoreAlone(t *testing.T) {
	c, p := newAppStateProbe(t)
	seedChat(t, c, chatJID)
	p.failWith = errors.New("409 conflict")

	if err := c.ArchiveChat(context.Background(), chatJID); err == nil {
		t.Fatal("a rejected app-state write reported success")
	}
	if readChatRow(t, c).archived {
		t.Error("a rejected archive was mirrored to the store anyway")
	}
	got, _ := c.store.GetChat(context.Background(), chatJID, false)
	if got == nil {
		t.Error("a rejected archive removed the cached chat")
	}
}

type chatRow struct {
	pinned, muted, archived bool
}

func readChatRow(t *testing.T, c *Client) chatRow {
	t.Helper()
	row, err := c.store.GetChat(context.Background(), chatJID, false)
	if err != nil {
		t.Fatalf("read chat row: %v", err)
	}
	if row == nil {
		t.Fatalf("chat %s missing", chatJID)
	}
	return chatRow{pinned: row.Pinned, muted: row.Muted, archived: row.Archived}
}

func starred(t *testing.T, c *Client, id string) bool {
	t.Helper()
	var v bool
	if err := c.store.DB().QueryRow("SELECT is_starred FROM messages WHERE id = ?", id).Scan(&v); err != nil {
		t.Fatalf("read star: %v", err)
	}
	return v
}
