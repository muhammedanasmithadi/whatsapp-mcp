package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
)

var errAlreadyPaired = errors.New("already paired: link a new device by logging out first")

var errNotConnected = errors.New("not connected: start the pairing flow before requesting a code")

func (c *Client) PairPhone(ctx context.Context, phone string) (string, error) {
	if c.wa.Store.ID != nil {
		return "", errAlreadyPaired
	}
	if !c.wa.IsConnected() {
		return "", errNotConnected
	}
	code, err := c.wa.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", fmt.Errorf("pair phone: %w", err)
	}
	return code, nil
}

func FormatPairingCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}
