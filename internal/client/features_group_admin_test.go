package client

import (
	"context"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestParseGroupChatJID(t *testing.T) {
	got, err := parseGroupChatJID("120363000000000000@g.us")
	if err != nil {
		t.Fatalf("parseGroupChatJID(group jid) error: %v", err)
	}
	if got.Server != "g.us" || got.User != "120363000000000000" {
		t.Fatalf("parseGroupChatJID(group jid) = %v", got)
	}
	if _, err := parseGroupChatJID("120363000000000000@newsletter"); err == nil {
		t.Errorf("parseGroupChatJID(newsletter jid) expected error")
	}
	if _, err := parseGroupChatJID("120363000000000000@s.whatsapp.net"); err == nil {
		t.Errorf("parseGroupChatJID(individual jid) expected error")
	}
	if _, err := parseGroupChatJID(""); err == nil {
		t.Errorf("parseGroupChatJID(empty) expected error")
	}
	if _, err := parseGroupChatJID("   "); err == nil {
		t.Errorf("parseGroupChatJID(whitespace) expected error")
	}
}

func TestParseGroupChatJID_RejectsNonGroup(t *testing.T) {
	_, err := parseGroupChatJID("447700000001@s.whatsapp.net")
	if err == nil {
		t.Fatal("expected error for an individual JID")
	}
	if !strings.Contains(err.Error(), "g.us") {
		t.Errorf("error %q does not name the group server", err.Error())
	}
}

func TestParseGroupMemberAddMode(t *testing.T) {
	cases := []struct {
		in   string
		want types.GroupMemberAddMode
	}{
		{"admin_add", types.GroupMemberAddModeAdmin},
		{"ADMIN_ADD", types.GroupMemberAddModeAdmin},
		{" all_member_add ", types.GroupMemberAddModeAllMember},
	}
	for _, tc := range cases {
		got, err := parseGroupMemberAddMode(tc.in)
		if err != nil {
			t.Errorf("parseGroupMemberAddMode(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseGroupMemberAddMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseGroupMemberAddMode_Invalid(t *testing.T) {
	_, err := parseGroupMemberAddMode("admins")
	if err == nil {
		t.Fatal("expected error for an unknown mode")
	}
	for _, want := range []string{"admin_add", "all_member_add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing allowed value %q", err.Error(), want)
		}
	}
}

func TestParseParticipantRequestAction(t *testing.T) {
	cases := []struct {
		in   string
		want whatsmeow.ParticipantRequestChange
	}{
		{"approve", whatsmeow.ParticipantChangeApprove},
		{"Approve", whatsmeow.ParticipantChangeApprove},
		{" reject ", whatsmeow.ParticipantChangeReject},
	}
	for _, tc := range cases {
		got, err := parseParticipantRequestAction(tc.in)
		if err != nil {
			t.Errorf("parseParticipantRequestAction(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseParticipantRequestAction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseParticipantRequestAction_Invalid(t *testing.T) {
	_, err := parseParticipantRequestAction("ban")
	if err == nil {
		t.Fatal("expected error for an unknown action")
	}
	for _, want := range []string{"approve", "reject"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing allowed value %q", err.Error(), want)
		}
	}
}

func TestGroupMemberAddModesAndActions(t *testing.T) {
	wantModes := []string{string(types.GroupMemberAddModeAdmin), string(types.GroupMemberAddModeAllMember)}
	if len(GroupMemberAddModes) != 2 {
		t.Fatalf("GroupMemberAddModes = %v, want two values", GroupMemberAddModes)
	}
	for i, want := range wantModes {
		if GroupMemberAddModes[i] != want {
			t.Errorf("GroupMemberAddModes[%d] = %q, want %q", i, GroupMemberAddModes[i], want)
		}
	}
	wantActions := []string{string(whatsmeow.ParticipantChangeApprove), string(whatsmeow.ParticipantChangeReject)}
	if len(ParticipantRequestActions) != 2 {
		t.Fatalf("ParticipantRequestActions = %v, want two values", ParticipantRequestActions)
	}
	for i, want := range wantActions {
		if ParticipantRequestActions[i] != want {
			t.Errorf("ParticipantRequestActions[%d] = %q, want %q", i, ParticipantRequestActions[i], want)
		}
	}
}

func TestSetGroupPhoto_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.SetGroupPhoto(context.Background(), "120363000000000000@g.us", "/tmp/photo.jpg")
	assertNotConnected(t, err)
}

func TestSetGroupJoinApprovalMode_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	assertNotConnected(t, c.SetGroupJoinApprovalMode(context.Background(), "120363000000000000@g.us", true))
}

func TestSetGroupMemberAddMode_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	assertNotConnected(t, c.SetGroupMemberAddMode(context.Background(), "120363000000000000@g.us", "all_member_add"))
}

func TestListGroupJoinRequests_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.ListGroupJoinRequests(context.Background(), "120363000000000000@g.us")
	assertNotConnected(t, err)
}

func TestResolveGroupJoinRequest_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.ResolveGroupJoinRequest(context.Background(), "120363000000000000@g.us", []string{"447700000001"}, "approve")
	assertNotConnected(t, err)
}
