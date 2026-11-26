package client

import (
	"fmt"
	"strings"
)

func NormalizeRecipient(recipient string) (string, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", fmt.Errorf("recipient: required")
	}
	if strings.Contains(recipient, "@") {
		return recipient, nil
	}
	trimmed := strings.TrimPrefix(recipient, "+")
	if trimmed == "" || !isAllDigits(trimmed) {
		return "", fmt.Errorf("recipient: must be digits only (no `+` prefix, spaces, or punctuation); got %q", recipient)
	}
	return trimmed, nil
}

func isAllDigits(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) == -1
}
