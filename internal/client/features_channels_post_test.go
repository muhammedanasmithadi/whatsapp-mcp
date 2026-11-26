package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestSendChannelMessage_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	res, err := c.SendChannelMessage(context.Background(), "123@newsletter", "hello followers", "")
	if err == nil {
		t.Fatalf("SendChannelMessage on a disconnected client: want error, got %+v", res)
	}
	if res != nil {
		t.Fatalf("SendChannelMessage on a disconnected client: want nil result, got %+v", res)
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("SendChannelMessage error = %q, want the not-connected error", err)
	}
}

func TestSendChannelMessage_RejectsMediaOutsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "post.png")
	if err := os.WriteFile(inside, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{allowedMediaRoot: root}
	if _, err := c.ValidateMediaPath("/etc/passwd"); err == nil {
		t.Fatal("ValidateMediaPath must reject a path outside the media root")
	} else if !strings.Contains(err.Error(), "outside allowed root") {
		t.Fatalf("ValidateMediaPath error = %q, want an out-of-root rejection", err)
	}
	if _, err := c.ValidateMediaPath(inside); err != nil {
		t.Fatalf("ValidateMediaPath on a file inside the media root: unexpected error: %v", err)
	}
}

func TestCreateChannel_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	info, err := c.CreateChannel(context.Background(), "Ops Notes", "release notes", "")
	if err == nil {
		t.Fatalf("CreateChannel on a disconnected client: want error, got %+v", info)
	}
	if info != nil {
		t.Fatalf("CreateChannel on a disconnected client: want nil info, got %+v", info)
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("CreateChannel error = %q, want the not-connected error", err)
	}
}

func TestCreateChannel_RejectsPictureOutsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "channel.png")
	if err := os.WriteFile(inside, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{allowedMediaRoot: root}
	if _, err := c.ValidateMediaPath("/etc/hostname"); err == nil {
		t.Fatal("ValidateMediaPath must reject a picture path outside the media root")
	}
	if _, err := c.ValidateMediaPath(inside); err != nil {
		t.Fatalf("ValidateMediaPath on a picture inside the media root: unexpected error: %v", err)
	}
}

func TestValidateChannelPost(t *testing.T) {
	cases := []struct {
		name      string
		message   string
		mediaPath string
		wantErr   bool
	}{
		{name: "text only", message: "hello followers", wantErr: false},
		{name: "caption with media", message: "see attached", mediaPath: "/media/pic.png", wantErr: false},
		{name: "media without caption", mediaPath: "/media/pic.png", wantErr: false},
		{name: "no message and no media", wantErr: true},
		{name: "whitespace message only", message: "   \t\n ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChannelPost(tc.message, tc.mediaPath)
			if tc.wantErr && err == nil {
				t.Fatalf("validateChannelPost(%q, %q): want error, got nil", tc.message, tc.mediaPath)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateChannelPost(%q, %q): want nil, got %v", tc.message, tc.mediaPath, err)
			}
		})
	}
}

func TestResolveChannelJID_RejectsBadInput(t *testing.T) {
	c := newDisconnectedClient()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "channel JID or invite link required"},
		{name: "whitespace", input: "   ", want: "channel JID or invite link required"},
		{name: "contact JID", input: "447700000001@s.whatsapp.net", want: "not a channel JID"},
		{name: "group JID", input: "447700000001-1600000000@g.us", want: "not a channel JID"},
		{name: "no server part", input: "@@", want: "not a channel JID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jid, info, err := c.resolveChannelJID(context.Background(), tc.input)
			if err == nil {
				t.Fatalf("resolveChannelJID(%q): want error, got %s / %+v", tc.input, jid, info)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resolveChannelJID(%q) error = %q, want it to mention %q", tc.input, err, tc.want)
			}
		})
	}
}

func TestResolveChannelJID_AcceptsNewsletterJID(t *testing.T) {
	c := newDisconnectedClient()
	jid, info, err := c.resolveChannelJID(context.Background(), "  1234567890@newsletter  ")
	if err != nil {
		t.Fatalf("resolveChannelJID: unexpected error: %v", err)
	}
	if jid.Server != "newsletter" || jid.User != "1234567890" {
		t.Fatalf("resolveChannelJID = %s, want 1234567890@newsletter", jid)
	}
	if info != nil {
		t.Fatalf("resolveChannelJID returned metadata %+v for a JID input; want nil until a lookup happens", info)
	}
}

func TestLoadMedia_ImageTypingOffline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "post.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n not really a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{allowedMediaRoot: root, log: NewStderrLogger("test", "ERROR", false)}

	asset, err := c.loadMedia(context.Background(), path, false)
	if err != nil {
		t.Fatalf("loadMedia: unexpected error: %v", err)
	}
	if asset.kind != whatsmeow.MediaImage || asset.mime != "image/png" {
		t.Fatalf("loadMedia kind/mime = %v/%q, want image/image/png", asset.kind, asset.mime)
	}
	if len(asset.data) == 0 {
		t.Fatal("loadMedia read no bytes")
	}
	if asset.upload != nil {
		t.Fatal("loadMedia with upload=false must not upload")
	}

	if _, err := c.loadMedia(context.Background(), filepath.Join(root, "missing.png"), false); err == nil {
		t.Fatal("loadMedia on a missing file: want error, got nil")
	}
}

func TestBuildChannelMediaMessage_UnencryptedShape(t *testing.T) {
	upload := &whatsmeow.UploadResponse{
		URL:        "https://cdn.example/newsletter-image/token",
		DirectPath: "/newsletter-image/token",
		FileSHA256: []byte("plainfilehash"),
		FileLength: 2048,
	}
	asset := &mediaAsset{path: "/media/post.png", data: make([]byte, 2048), kind: whatsmeow.MediaImage, mime: "image/png"}

	msg := buildChannelMediaMessage(asset, upload, "caption here")
	if msg.Conversation != nil {
		t.Fatal("a media post must not carry a Conversation body")
	}
	img := msg.GetImageMessage()
	if img == nil {
		t.Fatal("an image asset produced no ImageMessage")
	}
	if got := img.GetURL(); got != upload.URL {
		t.Errorf("ImageMessage.URL = %q, want %q", got, upload.URL)
	}
	if got := img.GetDirectPath(); got != upload.DirectPath {
		t.Errorf("ImageMessage.DirectPath = %q, want %q", got, upload.DirectPath)
	}
	if got := img.GetCaption(); got != "caption here" {
		t.Errorf("ImageMessage.Caption = %q, want the message as caption", got)
	}
	if got := img.GetFileLength(); got != upload.FileLength {
		t.Errorf("ImageMessage.FileLength = %d, want %d", got, upload.FileLength)
	}
	if got := img.GetFileSHA256(); len(got) == 0 {
		t.Error("ImageMessage.FileSHA256 must carry the plain SHA-256 from the newsletter upload")
	}
	if img.MediaKey != nil {
		t.Errorf("ImageMessage.MediaKey must stay empty for unencrypted channel media, got %q", img.MediaKey)
	}
	if img.FileEncSHA256 != nil {
		t.Errorf("ImageMessage.FileEncSHA256 must stay empty for unencrypted channel media, got %q", img.FileEncSHA256)
	}
}

func TestBuildChannelMediaMessage_DocumentShape(t *testing.T) {
	upload := &whatsmeow.UploadResponse{
		URL:        "https://cdn.example/newsletter-document/token",
		DirectPath: "/newsletter-document/token",
		FileSHA256: []byte("plainfilehash"),
		FileLength: 512,
	}
	asset := &mediaAsset{
		path: "/media/report.pdf",
		data: make([]byte, 512),
		kind: whatsmeow.MediaDocument,
		mime: "application/pdf",
	}

	msg := buildChannelMediaMessage(asset, upload, "")
	doc := msg.GetDocumentMessage()
	if doc == nil {
		t.Fatal("a document asset produced no DocumentMessage")
	}
	if got := doc.GetTitle(); got != "report.pdf" {
		t.Errorf("DocumentMessage.Title = %q, want the file name", got)
	}
	if doc.MediaKey != nil || doc.FileEncSHA256 != nil {
		t.Error("DocumentMessage must not carry encryption fields for unencrypted channel media")
	}
}

func TestBuildChannelMediaMessage_UnknownKind(t *testing.T) {
	msg := buildChannelMediaMessage(&mediaAsset{kind: whatsmeow.MediaAppState}, &whatsmeow.UploadResponse{}, "")
	if msg.ImageMessage != nil || msg.VideoMessage != nil || msg.AudioMessage != nil || msg.DocumentMessage != nil {
		t.Fatalf("an unsupported media kind produced a submessage: %+v", msg)
	}
}
