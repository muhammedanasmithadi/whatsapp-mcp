package daemon

import (
	"fmt"

	"github.com/skip2/go-qrcode"
)

func renderQRPNG(payload string, size int) ([]byte, error) {
	if payload == "" {
		return nil, fmt.Errorf("renderQRPNG: empty payload")
	}
	return qrcode.Encode(payload, qrcode.Medium, size)
}
