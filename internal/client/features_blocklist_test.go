package client

import (
	"context"
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestGetBlocklist_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetBlocklist(context.Background())
	assertNotConnected(t, err)
}

func TestBlockContact_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.BlockContact(context.Background(), "447700000001")
	assertNotConnected(t, err)
}

func TestUnblockContact_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.UnblockContact(context.Background(), "447700000001")
	assertNotConnected(t, err)
}

func TestBlocklistJSON_AddsPhones(t *testing.T) {
	list := &types.Blocklist{DHash: "h", Items: []types.BlocklistItem{
		{LID: types.JID{User: "99887766", Server: types.HiddenUserServer}},
		{LID: types.JID{User: "11112222", Server: types.HiddenUserServer}},
		{
			LID: types.JID{User: "55554444", Server: types.HiddenUserServer},
			PN:  types.JID{User: "447700000003", Server: types.DefaultUserServer},
		},
	}}
	phones := map[string]string{"99887766@lid": "447700000002", "55554444@lid": "stale"}
	b, err := json.Marshal(blocklistJSON(list, func(j string) string { return phones[j] }))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"DHash":"h","JIDs":["99887766@lid","11112222@lid","55554444@lid"],"Contacts":[{"jid":"99887766@lid","phone":"447700000002"},{"jid":"11112222@lid"},{"jid":"55554444@lid","phone":"447700000003"}]}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}
