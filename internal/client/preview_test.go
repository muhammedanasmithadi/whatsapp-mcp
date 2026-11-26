package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

func TestParticipantListHash(t *testing.T) {
	cases := []struct {
		name       string
		recipients []types.JID
		want       string
	}{
		{
			name:       "single device",
			recipients: []types.JID{types.NewJID("15551234567", types.DefaultUserServer)},
			want:       "2:XMZZanIp",
		},
		{
			name: "order of the input does not matter",
			recipients: []types.JID{
				types.NewJID("447700900001", types.DefaultUserServer),
				types.NewJID("447700900000", types.DefaultUserServer),
			},
			want: "2:JnsCrpRX",
		},
		{
			name:       "empty list",
			recipients: nil,
			want:       "2:47DEQpj8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := participantListHash(tc.recipients); got != tc.want {
				t.Errorf("participantListHash = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSendResultForPHashVerdict(t *testing.T) {
	cases := []struct {
		name      string
		resp      whatsmeow.SendResponse
		wantOK    bool
		wantInMsg []string
		wantNotIn []string
	}{
		{
			name:   "server echoes no hash",
			resp:   whatsmeow.SendResponse{ID: "MSG1"},
			wantOK: true,
		},
		{
			name:   "matching hash",
			resp:   whatsmeow.SendResponse{ID: "MSG2", PHash: "2:abc", ExpectedPHash: "2:abc"},
			wantOK: true,
		},
		{
			name:      "mismatched hash fails the send",
			resp:      whatsmeow.SendResponse{ID: "MSG3", PHash: "2:current", ExpectedPHash: "2:server"},
			wantOK:    false,
			wantInMsg: []string{"2:current", "2:server", "MSG3"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sendResultFor("15551234567", tc.resp)
			if got.Success != tc.wantOK {
				t.Fatalf("Success = %v, want %v (message %q)", got.Success, tc.wantOK, got.Message)
			}
			for _, want := range tc.wantInMsg {
				if !strings.Contains(got.Message, want) {
					t.Errorf("message %q missing %q", got.Message, want)
				}
			}
		})
	}
}

func TestPlanOutboundText(t *testing.T) {
	c := &Client{}
	plan, err := c.planOutbound(context.Background(), "+15551234567", "hello there", "", false, false)
	if err != nil {
		t.Fatalf("planOutbound: %v", err)
	}
	if plan.jid.String() != "15551234567@s.whatsapp.net" {
		t.Errorf("jid = %q", plan.jid.String())
	}
	if plan.message.Conversation == nil || *plan.message.Conversation != "hello there" {
		t.Errorf("conversation = %v", plan.message.Conversation)
	}
	preview := plan.preview
	if preview.MessageKind != "text" || preview.TextChars != len("hello there") {
		t.Errorf("preview kind = %q, chars = %d", preview.MessageKind, preview.TextChars)
	}
	if preview.ChatKind != "contact" || preview.Media != nil {
		t.Errorf("unexpected preview: %+v", preview)
	}
}

func TestPlanOutboundMediaRequiresMediaRoot(t *testing.T) {
	c := &Client{allowedMediaRoot: t.TempDir()}
	path := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.planOutbound(context.Background(), "15551234567", "caption", path, false, false); err == nil {
		t.Fatal("expected media_path outside the media root to be rejected")
	}
}

func TestPlanOutboundMedia(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "picture.png")
	if err := os.WriteFile(path, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{allowedMediaRoot: root}

	plan, err := c.planOutbound(context.Background(), "15551234567", "a caption", path, false, false)
	if err != nil {
		t.Fatalf("planOutbound: %v", err)
	}
	if plan.message.ImageMessage == nil {
		t.Fatal("expected an image submessage")
	}
	if got := plan.message.ImageMessage.GetMimetype(); got != "image/png" {
		t.Errorf("mime = %q, want image/png", got)
	}
	media := plan.preview.Media
	if media == nil {
		t.Fatal("expected a media preview")
	}
	if media.Kind != "image" || media.Bytes != len("png-bytes") || media.MimeType != "image/png" {
		t.Errorf("media preview = %+v", media)
	}
	if plan.preview.MessageKind != "image" {
		t.Errorf("message kind = %q", plan.preview.MessageKind)
	}
}

func TestPlanOutboundStatusEphemeral(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, []byte("mp4-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{allowedMediaRoot: root}

	plan, err := c.planOutbound(context.Background(), store.StatusBroadcastJID, "story caption", path, false, false)
	if err != nil {
		t.Fatalf("planOutbound: %v", err)
	}
	if plan.preview.ChatKind != "broadcast" {
		t.Errorf("chat kind = %q, want broadcast", plan.preview.ChatKind)
	}
	if plan.preview.EphemeralSecs != statusEphemeralSecs {
		t.Errorf("ephemeral seconds = %d, want %d", plan.preview.EphemeralSecs, statusEphemeralSecs)
	}
	if plan.message.VideoMessage == nil || plan.message.VideoMessage.GetContextInfo().GetExpiration() == 0 {
		t.Error("expected the 24h expiration on the video submessage")
	}
	if plan.message.GetMessageContextInfo().GetMessageAddOnDurationInSecs() == 0 {
		t.Error("expected the message-level ephemeral duration for a status update")
	}
}

func TestPlanOutboundStatusAudienceWarningOffline(t *testing.T) {
	c := &Client{}
	plan, err := c.planOutbound(context.Background(), "status@broadcast", "text status", "", false, false)
	if err != nil {
		t.Fatalf("planOutbound: %v", err)
	}
	if plan.message.Conversation == nil || *plan.message.Conversation != "text status" {
		t.Errorf("conversation = %v", plan.message.Conversation)
	}
	if len(plan.preview.Warnings) != 1 || !strings.Contains(plan.preview.Warnings[0], "not connected") {
		t.Errorf("warnings = %v, want one connectivity warning", plan.preview.Warnings)
	}
}

func TestChatKindAndMediaKindName(t *testing.T) {
	if got := chatKind(types.NewJID("123", types.GroupServer)); got != "group" {
		t.Errorf("chatKind(group) = %q", got)
	}
	if got := chatKind(types.NewJID("status", types.BroadcastServer)); got != "broadcast" {
		t.Errorf("chatKind(broadcast) = %q", got)
	}
	if got := mediaKindName(whatsmeow.MediaDocument); got != "document" {
		t.Errorf("mediaKindName(document) = %q", got)
	}
}

func TestAppendUnique(t *testing.T) {
	own := types.NewJID("15550001111", types.DefaultUserServer)
	jids := []types.JID{own, types.NewJID("15550002222", types.DefaultUserServer)}
	if got := appendUnique(jids, own); len(got) != 2 {
		t.Errorf("appendUnique added a duplicate: %d entries", len(got))
	}
	if got := appendUnique(jids, types.NewJID("15550003333", types.DefaultUserServer)); len(got) != 3 {
		t.Errorf("appendUnique did not append: %d entries", len(got))
	}
}
