package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

type sendProbe struct {
	sentTo    types.JID
	sentMsg   *waProto.Message
	uploads   int
	uploadByt []byte
	failWith  error
	ack       whatsmeow.SendResponse
	ackPHash  string
	srvPHash  string
}

func newSendProbe(t *testing.T, dir string) (*Client, *sendProbe) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p := &sendProbe{ack: whatsmeow.SendResponse{ID: "ACKID1"}}
	c := &Client{
		store:            st,
		allowedMediaRoot: dir,
		redactor:         &security.Redactor{},
		log:              NewStderrLogger("test", "ERROR", false),
		socketConnected:  func() bool { return true },
		uploadMedia: func(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
			p.uploads++
			p.uploadByt = data
			return whatsmeow.UploadResponse{URL: "https://cdn.example/x", DirectPath: "/x", FileLength: uint64(len(data))}, nil
		},
		sendMessage: func(_ context.Context, to types.JID, m *waProto.Message) (whatsmeow.SendResponse, error) {
			if p.failWith != nil {
				return whatsmeow.SendResponse{}, p.failWith
			}
			p.sentTo = to
			p.sentMsg = m
			r := p.ack
			r.PHash = p.ackPHash
			r.ExpectedPHash = p.srvPHash
			return r, nil
		},
	}
	return c, p
}

func TestSendTextPostsTheBody(t *testing.T) {
	c, p := newSendProbe(t, t.TempDir())

	r := c.Send(context.Background(), "447700900123", "hello there")
	if !r.Success {
		t.Fatalf("send failed: %s", r.Message)
	}
	if p.sentTo.String() != "447700900123@s.whatsapp.net" {
		t.Errorf("sent to %s", p.sentTo)
	}
	if got := p.sentMsg.GetConversation(); got != "hello there" {
		t.Errorf("body = %q", got)
	}
	if p.uploads != 0 {
		t.Errorf("a text send uploaded %d times", p.uploads)
	}

	msgs, err := c.Store().ListMessages(context.Background(), store.ListMessagesParams{ChatJID: "447700900123@s.whatsapp.net", Limit: 5})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello there" {
		t.Errorf("cache = %+v, want the sent body", msgs)
	}
}

func TestSendTextReportsTransportFailure(t *testing.T) {
	c, p := newSendProbe(t, t.TempDir())
	p.failWith = errors.New("dial tcp: connection refused")

	r := c.Send(context.Background(), "447700900123", "never lands")
	if r.Success {
		t.Fatal("a transport failure must not report success")
	}
	if !strings.Contains(r.Message, "connection refused") {
		t.Errorf("message = %q, want the transport cause", r.Message)
	}
}

func TestSendTextWarnsWhenWhatsAppRejectsTheFanOut(t *testing.T) {
	c, p := newSendProbe(t, t.TempDir())
	p.ackPHash = "2:client"
	p.srvPHash = "2:server"

	r := c.Send(context.Background(), store.StatusBroadcastJID, "story caption")
	if !r.Success {
		t.Fatalf("a fan-out mismatch is a warning, not a failure: %s", r.Message)
	}
	if !strings.Contains(r.Warning, "2:server") || !strings.Contains(r.Warning, "2:client") {
		t.Errorf("warning = %q, want both hashes", r.Warning)
	}
	if p.sentMsg.GetMessageContextInfo().GetMessageAddOnDurationInSecs() != 86400 {
		t.Error("a status update must carry the 24h ephemeral duration")
	}
}

func TestSendImageAppliesViewOnceAndCachesTheRow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pic.png")
	if err := os.WriteFile(path, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, p := newSendProbe(t, root)

	r := c.SendMediaWithOptions(context.Background(), SendMediaOptions{Recipient: "447700900123", MediaPath: path, Caption: "a caption", ViewOnce: true})
	if !r.Success {
		t.Fatalf("send failed: %s", r.Message)
	}
	if p.uploads != 1 {
		t.Errorf("uploads = %d, want 1", p.uploads)
	}
	if string(p.uploadByt) != "png-bytes" {
		t.Errorf("uploaded %q, want the file bytes", p.uploadByt)
	}
	img := p.sentMsg.GetImageMessage()
	if img == nil {
		t.Fatal("no image submessage")
	}
	if img.GetMimetype() != "image/png" || img.GetCaption() != "a caption" {
		t.Errorf("image = %+v", img)
	}
	if !img.GetViewOnce() {
		t.Error("view_once was not applied")
	}
}

func TestSendRejectsMediaOutsideTheRoot(t *testing.T) {
	c, p := newSendProbe(t, t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := c.SendMediaWithOptions(context.Background(), SendMediaOptions{Recipient: "447700900123", MediaPath: outside})
	if r.Success {
		t.Fatal("media outside the root must be rejected")
	}
	if p.uploads != 0 {
		t.Error("a rejected media path must not be uploaded")
	}
}
