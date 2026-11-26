package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

var GroupMemberAddModes = []string{
	string(types.GroupMemberAddModeAdmin),
	string(types.GroupMemberAddModeAllMember),
}

var ParticipantRequestActions = []string{
	string(whatsmeow.ParticipantChangeApprove),
	string(whatsmeow.ParticipantChangeReject),
}

type GroupJoinRequest struct {
	JID         string    `json:"jid"`
	RequestedAt time.Time `json:"requested_at"`
}

type GroupJoinRequestOutcome struct {
	JID         string `json:"jid"`
	PhoneNumber string `json:"phone_number,omitempty"`
	LID         string `json:"lid,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

func parseGroupChatJID(chatJID string) (types.JID, error) {
	jid, err := parseRecipient(chatJID)
	if err != nil {
		return types.JID{}, err
	}
	if jid.Server != types.GroupServer || jid.User == "" {
		return types.JID{}, fmt.Errorf("chat_jid must be a group JID (…@%s); got %q", types.GroupServer, chatJID)
	}
	return jid, nil
}

func parseGroupMemberAddMode(mode string) (types.GroupMemberAddMode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(types.GroupMemberAddModeAdmin):
		return types.GroupMemberAddModeAdmin, nil
	case string(types.GroupMemberAddModeAllMember):
		return types.GroupMemberAddModeAllMember, nil
	default:
		return "", fmt.Errorf("invalid mode %q: must be one of %s", mode, strings.Join(GroupMemberAddModes, ", "))
	}
}

func parseParticipantRequestAction(action string) (whatsmeow.ParticipantRequestChange, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case string(whatsmeow.ParticipantChangeApprove):
		return whatsmeow.ParticipantChangeApprove, nil
	case string(whatsmeow.ParticipantChangeReject):
		return whatsmeow.ParticipantChangeReject, nil
	default:
		return "", fmt.Errorf("invalid action %q: must be one of %s", action, strings.Join(ParticipantRequestActions, ", "))
	}
}

func (c *Client) SetGroupPhoto(ctx context.Context, chatJID, photoPath string) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	chat, err := parseGroupChatJID(chatJID)
	if err != nil {
		return "", err
	}
	safePath, err := c.ValidateMediaPath(photoPath)
	if err != nil {
		return "", fmt.Errorf("media_path rejected: %w", err)
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("read media file: %w", err)
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return "", err
	}
	photoID, err := c.wa.SetGroupPhoto(ctx, chat, data)
	if err != nil {
		return "", fmt.Errorf("set group photo: %w", err)
	}
	return photoID, nil
}

func (c *Client) SetGroupJoinApprovalMode(ctx context.Context, chatJID string, enabled bool) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := parseGroupChatJID(chatJID)
	if err != nil {
		return err
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return err
	}
	if err := c.wa.SetGroupJoinApprovalMode(ctx, chat, enabled); err != nil {
		return fmt.Errorf("set group join approval mode: %w", err)
	}
	return nil
}

func (c *Client) SetGroupMemberAddMode(ctx context.Context, chatJID string, mode string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	chat, err := parseGroupChatJID(chatJID)
	if err != nil {
		return err
	}
	addMode, err := parseGroupMemberAddMode(mode)
	if err != nil {
		return err
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return err
	}
	if err := c.wa.SetGroupMemberAddMode(ctx, chat, addMode); err != nil {
		return fmt.Errorf("set group member add mode: %w", err)
	}
	return nil
}

func (c *Client) ListGroupJoinRequests(ctx context.Context, chatJID string) ([]GroupJoinRequest, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	chat, err := parseGroupChatJID(chatJID)
	if err != nil {
		return nil, err
	}
	requests, err := c.wa.GetGroupRequestParticipants(ctx, chat)
	if err != nil {
		return nil, fmt.Errorf("list group join requests: %w", err)
	}
	out := make([]GroupJoinRequest, 0, len(requests))
	for _, req := range requests {
		out = append(out, GroupJoinRequest{JID: req.JID.String(), RequestedAt: req.RequestedAt})
	}
	return out, nil
}

func (c *Client) ResolveGroupJoinRequest(ctx context.Context, chatJID string, participants []string, action string) ([]GroupJoinRequestOutcome, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	chat, err := parseGroupChatJID(chatJID)
	if err != nil {
		return nil, err
	}
	if len(participants) == 0 {
		return nil, errors.New("participants must not be empty")
	}
	jids, err := parseParticipantJIDs(participants)
	if err != nil {
		return nil, err
	}
	change, err := parseParticipantRequestAction(action)
	if err != nil {
		return nil, err
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return nil, err
	}
	results, err := c.wa.UpdateGroupRequestParticipants(ctx, chat, jids, change)
	if err != nil {
		return nil, fmt.Errorf("resolve group join requests: %w", err)
	}
	out := make([]GroupJoinRequestOutcome, 0, len(results))
	for _, res := range results {
		outcome := GroupJoinRequestOutcome{JID: res.JID.String(), ErrorCode: res.Error}
		if !res.PhoneNumber.IsEmpty() {
			outcome.PhoneNumber = res.PhoneNumber.String()
		}
		if !res.LID.IsEmpty() {
			outcome.LID = res.LID.String()
		}
		out = append(out, outcome)
	}
	return out, nil
}
