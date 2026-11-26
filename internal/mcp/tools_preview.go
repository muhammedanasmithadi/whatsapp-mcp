package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

type previewSendArgs struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message,omitempty"`
	MediaPath string `json:"media_path,omitempty"`
	ViewOnce  bool   `json:"view_once,omitempty"`
}

func (s *Server) registerPreviewSend() {
	tool := mcp.NewTool("preview_send",
		mcp.WithDescription("Dry-run a WhatsApp send and return the post-processed payload: the resolved recipient JID and chat class, the message kind (text/image/audio/video/document), the exact caption or body text, the media file's type, MIME type, size, dimensions, duration and thumbnail, the view-once and disappearing-timer flags, and — for a Status (Story) post — the resolved audience, its device count, and the participant-list hash WhatsApp will be asked to accept. Nothing is sent, uploaded, or rate-limited. Use it before send_message, send_file, send_audio_message and send_status to see exactly what contacts would receive."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc+"; use status@broadcast to preview a Status (Story) post")),
		mcp.WithString("message", mcp.Description("message body or media caption text that would be sent")),
		mcp.WithString("media_path", mcp.Description("absolute path to an image, audio, video or document file under the media root")),
		mcp.WithBoolean("view_once", mcp.Description("pass true to preview a view-once media message")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a previewSendArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
		}
		if a.Message == "" && a.MediaPath == "" {
			return errorResult(ErrInvalidParameter("message", "non-empty text when media_path is omitted", "empty string")), nil
		}
		preview, err := s.client.PreviewSend(ctx, client.SendPreviewOptions{
			Recipient: a.Recipient,
			Message:   a.Message,
			MediaPath: a.MediaPath,
			ViewOnce:  a.ViewOnce,
		})
		if err != nil {
			return s.toolErr("preview_send", a.Recipient, "", "preview_send", err), nil
		}
		return s.toolOK("preview_send", a.Recipient, "", "preview_send", "", preview)
	}))
}
