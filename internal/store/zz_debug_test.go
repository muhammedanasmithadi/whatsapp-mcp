package store

import (
	"testing"
)

func TestZZDebug(t *testing.T) {
	s := openTestStore(t)
	t.Log("opened")
	msgs, err := s.ListMessages(t.Context(), ListMessagesParams{
		ChatJID: "447700000001@s.whatsapp.net",
		Limit:   50,
	})
	t.Logf("ListMessages returned: %d msgs, err=%v", len(msgs), err)
	_ = msgs
}
