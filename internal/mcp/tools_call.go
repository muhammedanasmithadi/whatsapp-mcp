package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

type rejectCallArgs struct {
	From   string `json:"from"`
	CallID string `json:"call_id"`
}

func (s *Server) registerRejectCall() {
	tool := mcp.NewTool("reject_call",
		mcp.WithDescription("Reject an incoming WhatsApp call. Use the caller's JID as `from` and the call id from the [voice call] or [video call] row that list_messages shows for that chat. Only a call that is still ringing can be rejected; a call that already ended returns an error. Returns {success}."),
		mcp.WithString("from", mcp.Required(), mcp.Description("JID of the caller, as stored on the call row")),
		mcp.WithString("call_id", mcp.Required(), mcp.Description("the call id stored as the message id of the [voice call] row")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a rejectCallArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("from", a.From); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("call_id", a.CallID); r != nil {
			return r, nil
		}
		if err := s.client.RejectCall(ctx, a.From, a.CallID); err != nil {
			return s.toolErr("reject_call", a.From, a.CallID, "reject_call", err), nil
		}
		return s.toolOK("reject_call", a.From, a.CallID, "reject_call", "", map[string]any{"success": true})
	}))
}
