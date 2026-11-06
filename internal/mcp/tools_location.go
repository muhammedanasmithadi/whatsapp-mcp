package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

type sendLocationArgs struct {
	Recipient    string  `json:"recipient"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	Name         string  `json:"name,omitempty"`
	Address      string  `json:"address,omitempty"`
	MarkChatRead bool    `json:"mark_chat_read,omitempty"`
}

func (s *Server) registerSendLocation() {
	tool := mcp.NewTool("send_location",
		mcp.WithDescription("Send a map pin (static location) to a WhatsApp chat. Recipients see a tappable map preview with the name and address. Static locations only: live location sharing is not supported by the multidevice protocol library. Rate limiting applies as with send_message. Returns a JSON object {Success, Message, ID}."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithNumber("latitude", mcp.Required(), mcp.Description("latitude in decimal degrees (-90 to 90)")),
		mcp.WithNumber("longitude", mcp.Required(), mcp.Description("longitude in decimal degrees (-180 to 180)")),
		mcp.WithString("name", mcp.Description("optional place name shown on the pin")),
		mcp.WithString("address", mcp.Description("optional street address shown under the name")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("if true, also ack recent incoming messages in the chat")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, req mcp.CallToolRequest, a sendLocationArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
		}
		if a.Latitude < -90 || a.Latitude > 90 {
			return errorResult(ErrInvalidParameter("latitude", "-90 to 90", fmt.Sprintf("%f", a.Latitude))), nil
		}
		if a.Longitude < -180 || a.Longitude > 180 {
			return errorResult(ErrInvalidParameter("longitude", "-180 to 180", fmt.Sprintf("%f", a.Longitude))), nil
		}
		ctx = withRateLimitOverride(ctx, req)
		r := s.client.SendLocation(ctx, a.Recipient, a.Latitude, a.Longitude, a.Name, a.Address)
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		if !r.Success {
			return s.toolErr("send_location", a.Recipient, "", "send", fmt.Errorf("%s", r.Message)), nil
		}
		result, err := resultJSON(r)
		if err != nil {
			return s.toolErr("send_location", a.Recipient, r.ID, "send", err), nil
		}
		return s.toolOK("send_location", a.Recipient, r.ID, "send", r.Message, result)
	}))
}
