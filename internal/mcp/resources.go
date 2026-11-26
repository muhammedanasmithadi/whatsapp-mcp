package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	mediaURIScheme   = "whatsapp"
	mediaURIPrefix   = mediaURIScheme + "://media/"
	mediaURITemplate = mediaURIPrefix + "{chat_jid}/{message_id}"
)

func (s *Server) registerResources() {
	template := mcp.NewResourceTemplate(
		mediaURITemplate,
		"WhatsApp media",
		mcp.WithTemplateDescription("Read the decrypted bytes of a WhatsApp media message (image, video, audio, document) as a base64-encoded blob. Fetches from WhatsApp's CDN on first read; subsequent reads hit the local cache. Pair with list_messages to discover media message IDs, or call download_media first if you want the JSON summary alongside."),
	)
	s.mcp.AddResourceTemplate(template, s.handleMediaResource)
}

func (s *Server) handleMediaResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	chatJID, messageID, err := parseMediaURI(req.Params.URI)
	if err != nil {
		return nil, fmt.Errorf("invalid whatsapp media URI %q: %w", req.Params.URI, err)
	}
	r := s.client.Download(ctx, messageID, chatJID, "")
	if !r.Success {
		return nil, fmt.Errorf("download media for %s/%s: %s", chatJID, messageID, r.Message)
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return nil, fmt.Errorf("read decrypted media at %s: %w", r.Path, err)
	}
	return []mcp.ResourceContents{
		mcp.BlobResourceContents{
			URI:      req.Params.URI,
			MIMEType: sniffResourceMIME(data, r.MediaType),
			Blob:     base64.StdEncoding.EncodeToString(data),
		},
	}, nil
}

func parseMediaURI(raw string) (chatJID, messageID string, err error) {
	if !strings.HasPrefix(raw, mediaURIPrefix) {
		return "", "", fmt.Errorf("must start with %s", mediaURIPrefix)
	}
	rest := strings.TrimPrefix(raw, mediaURIPrefix)
	slash := strings.LastIndex(rest, "/")
	if slash == -1 {
		return "", "", errors.New("must contain chat_jid/message_id")
	}
	chatJID, err = url.PathUnescape(rest[:slash])
	if err != nil {
		return "", "", fmt.Errorf("decode chat_jid: %w", err)
	}
	messageID = rest[slash+1:]
	if chatJID == "" || messageID == "" {
		return "", "", errors.New("chat_jid and message_id must both be non-empty")
	}
	return chatJID, messageID, nil
}

func sniffResourceMIME(data []byte, mediaType string) string {
	mime := http.DetectContentType(data)
	if mediaType == "audio" && strings.HasPrefix(mime, "application/") {
		return "audio/ogg"
	}
	return mime
}
