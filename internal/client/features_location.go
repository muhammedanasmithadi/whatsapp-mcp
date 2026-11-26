package client

import (
	"context"
	"fmt"
	"math"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func (c *Client) SendLocation(ctx context.Context, recipient string, latitude, longitude float64, name, address string) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	if math.IsNaN(latitude) || math.IsNaN(longitude) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return SendResult{Success: false, Message: "latitude must be -90..90 and longitude -180..180"}
	}
	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}
	if err := c.checkSendWindow(ctx, recipientJID); err != nil {
		c.incRateLimited()
		return SendResult{Success: false, Message: err.Error()}
	}

	location := &waE2E.LocationMessage{
		DegreesLatitude:  proto.Float64(latitude),
		DegreesLongitude: proto.Float64(longitude),
	}
	if name != "" {
		location.Name = proto.String(name)
	}
	if address != "" {
		location.Address = proto.String(address)
	}
	msg := &waE2E.Message{
		MessageContextInfo: c.ephemeralContextInfo(ctx, recipientJID),
		LocationMessage:    location,
	}

	c.incSends()
	resp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		c.incErrors()
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending location: %v", err)}
	}
	c.incMessagesOut()

	label := name
	if label == "" {
		label = "Location"
	}
	cachedText := fmt.Sprintf("📍 %s (%.6f, %.6f)", label, latitude, longitude)
	c.persistSent(ctx, recipientJID, resp.ID, cachedText, "", msg)
	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Location sent to %s", recipient),
		ID:      resp.ID,
	}
}
