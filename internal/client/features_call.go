package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (c *Client) RejectCall(ctx context.Context, from, callID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if strings.TrimSpace(callID) == "" {
		return errors.New("call_id required")
	}
	fromJID, err := parseRecipient(from)
	if err != nil {
		return err
	}
	if err := c.wa.RejectCall(ctx, fromJID.ToNonAD(), callID); err != nil {
		return fmt.Errorf("reject call: %w", err)
	}
	return nil
}
