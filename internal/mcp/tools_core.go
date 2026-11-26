package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

func (s *Server) maybeMarkChatRead(ctx context.Context, r client.SendResult, recipient string, enabled bool) {
	if !enabled || !r.Success {
		return
	}
	chatJID := normalizeRecipientToChatJID(recipient)
	if chatJID == "" {
		return
	}
	_, _ = s.client.MarkChatRead(ctx, chatJID, 50)
}

func normalizeRecipientToChatJID(recipient string) string {
	canonical, err := client.NormalizeRecipient(recipient)
	if err != nil {
		return ""
	}
	if strings.Contains(canonical, "@") {
		return canonical
	}
	return canonical + "@s.whatsapp.net"
}

func parseTimestamp(s string) (time.Time, error) {
	formats := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp format: %s", s)
}
