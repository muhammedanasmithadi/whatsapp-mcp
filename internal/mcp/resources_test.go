package mcp

import (
	"testing"
)

func TestParseMediaURI(t *testing.T) {
	cases := []struct {
		name        string
		uri         string
		wantChatJID string
		wantMsgID   string
		wantErr     string // substring; empty means expect success
	}{
		{
			name:        "individual jid (url-encoded @)",
			uri:         "whatsapp://media/971503469348%40s.whatsapp.net/3EB01D1397A581AAC94B29",
			wantChatJID: "971503469348@s.whatsapp.net",
			wantMsgID:   "3EB01D1397A581AAC94B29",
		},
		{
			name:        "group jid (url-encoded @, hyphen kept as-is)",
			uri:         "whatsapp://media/120363404898807161%40g.us/3EB037D9D1351AA170A086",
			wantChatJID: "120363404898807161@g.us",
			wantMsgID:   "3EB037D9D1351AA170A086",
		},
		{
			name:    "wrong scheme",
			uri:     "https://media/foo/bar",
			wantErr: "must start with whatsapp://media/",
		},
		{
			name:    "missing message id",
			uri:     "whatsapp://media/971503469348%40s.whatsapp.net",
			wantErr: "must contain chat_jid/message_id",
		},
		{
			name:    "empty chat_jid",
			uri:     "whatsapp://media//3EB01D",
			wantErr: "chat_jid and message_id must both be non-empty",
		},
		{
			name:    "empty message_id",
			uri:     "whatsapp://media/971503469348%40s.whatsapp.net/",
			wantErr: "chat_jid and message_id must both be non-empty",
		},
		{
			name:    "malformed percent-encoding",
			uri:     "whatsapp://media/971503469348%ZZ/3EB01D",
			wantErr: "decode chat_jid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatJID, msgID, err := parseMediaURI(tc.uri)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (chatJID=%q msgID=%q)", tc.wantErr, chatJID, msgID)
				}
				if !contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if chatJID != tc.wantChatJID {
				t.Errorf("chatJID = %q, want %q", chatJID, tc.wantChatJID)
			}
			if msgID != tc.wantMsgID {
				t.Errorf("messageID = %q, want %q", msgID, tc.wantMsgID)
			}
		})
	}
}

func TestSniffResourceMIME_AudioOverride(t *testing.T) {
	oggPrefix := []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00")

	if got := sniffResourceMIME(oggPrefix, "audio"); got != "audio/ogg" {
		t.Errorf("audio Ogg: sniff = %q, want audio/ogg", got)
	}

	if got := sniffResourceMIME(oggPrefix, "video"); got != "application/ogg" {
		t.Errorf("video Ogg: sniff = %q, want application/ogg (no override)", got)
	}
}

func TestSniffResourceMIME_ImagePassThrough(t *testing.T) {
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	if got := sniffResourceMIME(jpegMagic, "image"); got != "image/jpeg" {
		t.Errorf("JPEG: sniff = %q, want image/jpeg", got)
	}
}

func TestRegisterResources_NewServerDoesNotPanic(t *testing.T) {
	s := NewServer(nil, nil, nil)
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.MCP() == nil {
		t.Fatal("Server.MCP() is nil")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
