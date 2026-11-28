package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

func parseParticipantJIDs(raws []string) ([]types.JID, error) {
	out := make([]types.JID, len(raws))
	for i, r := range raws {
		jid, err := parseRecipient(r)
		if err != nil {
			return nil, err
		}
		out[i] = jid
	}
	return out, nil
}

func (c *Client) CreateGroup(ctx context.Context, name string, participants []string) (string, string, error) {
	if !c.socketConnected() {
		return "", "", errors.New("not connected to WhatsApp")
	}
	if strings.TrimSpace(name) == "" {
		return "", "", errors.New("group name is required")
	}
	jids, err := parseParticipantJIDs(participants)
	if err != nil {
		return "", "", err
	}
	info, err := c.wa.CreateGroup(ctx, whatsmeow.ReqCreateGroup{
		Name:         name,
		Participants: jids,
	})
	if err != nil {
		return "", "", fmt.Errorf("create group: %w", err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		return info.JID.String(), "", fmt.Errorf("marshal group info: %w", err)
	}
	return info.JID.String(), string(b), nil
}

func (c *Client) LeaveGroup(ctx context.Context, chatJID string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.LeaveGroup(ctx, jid); err != nil {
		return fmt.Errorf("leave group: %w", err)
	}
	return nil
}

func (c *Client) ListJoinedGroups(ctx context.Context) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	groups, err := c.wa.GetJoinedGroups(ctx)
	if err != nil {
		return "", fmt.Errorf("list joined groups: %w", err)
	}
	b, err := json.Marshal(groups)
	if err != nil {
		return "", fmt.Errorf("marshal groups: %w", err)
	}
	return string(b), nil
}

func (c *Client) GetGroupInfoJSON(ctx context.Context, chatJID string) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	info, err := c.wa.GetGroupInfo(ctx, jid)
	if err != nil {
		return "", fmt.Errorf("get group info: %w", err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("marshal group info: %w", err)
	}
	return string(b), nil
}

func parseParticipantAction(action string) (whatsmeow.ParticipantChange, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "add":
		return whatsmeow.ParticipantChangeAdd, nil
	case "remove":
		return whatsmeow.ParticipantChangeRemove, nil
	case "promote":
		return whatsmeow.ParticipantChangePromote, nil
	case "demote":
		return whatsmeow.ParticipantChangeDemote, nil
	default:
		return "", fmt.Errorf("invalid action %q: must be one of add, remove, promote, demote", action)
	}
}

type participantStatus string

const (
	statusAdded          participantStatus = "added"
	statusRemoved        participantStatus = "removed"
	statusPromoted       participantStatus = "promoted"
	statusDemoted        participantStatus = "demoted"
	statusAlreadyMember  participantStatus = "already_member"
	statusNotMember      participantStatus = "not_member"
	statusInviteRequired participantStatus = "invite_required"
	statusInviteSent     participantStatus = "invite_sent"
	statusInviteFailed   participantStatus = "invite_failed"
	statusFailed         participantStatus = "failed"
)

var participantSuccessStatus = map[whatsmeow.ParticipantChange]participantStatus{
	whatsmeow.ParticipantChangeAdd:     statusAdded,
	whatsmeow.ParticipantChangeRemove:  statusRemoved,
	whatsmeow.ParticipantChangePromote: statusPromoted,
	whatsmeow.ParticipantChangeDemote:  statusDemoted,
}

const errCodeAlreadyMember = 409

type ParticipantUpdateOptions struct {
	SendInvite    bool
	InviteMessage string
}

type ParticipantOutcome struct {
	Input           string            `json:"input"`
	JID             string            `json:"jid,omitempty"`
	Status          participantStatus `json:"status"`
	ErrorCode       int               `json:"error_code,omitempty"`
	Error           string            `json:"error,omitempty"`
	InviteMessageID string            `json:"invite_message_id,omitempty"`
	InviteExpiresAt time.Time         `json:"invite_expires_at,omitzero"`
}

type ParticipantUpdateReport struct {
	ChatJID   string               `json:"chat_jid"`
	GroupName string               `json:"group_name"`
	Action    string               `json:"action"`
	IsAdmin   bool                 `json:"is_admin"`
	Results   []ParticipantOutcome `json:"results"`
}

func (c *Client) UpdateGroupParticipants(ctx context.Context, chatJID string, participants []string, action string, opts ParticipantUpdateOptions) (*ParticipantUpdateReport, error) {
	if !c.socketConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat JID: %w", err)
	}
	if chat.Server != types.GroupServer {
		return nil, fmt.Errorf("chat_jid must be a group JID (…@%s); got %q", types.GroupServer, chatJID)
	}
	parsedAction, err := parseParticipantAction(action)
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

	info, err := c.wa.GetGroupInfo(ctx, chat)
	if err != nil {
		return nil, fmt.Errorf("get group info: %w", err)
	}
	self := findParticipant(info, c.wa.Store.GetJID(), c.wa.Store.GetLID())
	if err := checkParticipantPermission(info, self, parsedAction); err != nil {
		return nil, err
	}
	lidFor := func(jid types.JID) types.JID { return c.lidForPN(ctx, jid) }
	results, pending, err := planParticipantChange(info, self, participants, jids, parsedAction, lidFor)
	if err != nil {
		return nil, err
	}
	report := &ParticipantUpdateReport{
		ChatJID:   chat.String(),
		GroupName: info.Name,
		Action:    string(parsedAction),
		IsAdmin:   self.IsAdmin,
		Results:   results,
	}
	if len(pending) == 0 {
		return report, nil
	}

	for i, batch := range participantBatches(pending, parsedAction, info.AddressingMode) {
		toSend := make([]types.JID, len(batch))
		for j, p := range batch {
			toSend[j] = p.jid
		}
		changed, err := c.wa.UpdateGroupParticipants(ctx, chat, toSend, parsedAction)
		if err != nil {
			if i == 0 {
				return nil, fmt.Errorf("update group participants: %w", err)
			}
			for _, p := range batch {
				report.Results[p.idx].Status = statusFailed
				report.Results[p.idx].Error = fmt.Sprintf("update group participants: %v", err)
			}
			continue
		}
		for _, p := range batch {
			out := &report.Results[p.idx]
			res, ok := c.resultFor(ctx, changed, p.jid, len(toSend))
			if !ok {
				out.Status = statusFailed
				out.Error = "WhatsApp returned no result for this participant"
				continue
			}
			applyResult(out, parsedAction, res)
			if out.Status == statusInviteRequired && opts.SendInvite {
				c.inviteParticipant(ctx, out, p.jid, chat, info.Name, opts.InviteMessage, res.AddRequest)
			}
		}
	}
	return report, nil
}

func (c *Client) lidForPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return types.EmptyJID
	}
	lid, err := c.wa.Store.LIDs.GetLIDForPN(ctx, jid)
	if err != nil {
		return types.EmptyJID
	}
	return lid
}

func participantBatches(pending []plannedChange, action whatsmeow.ParticipantChange, mode types.AddressingMode) [][]plannedChange {
	if action != whatsmeow.ParticipantChangeAdd || mode != types.AddressingModeLID {
		return [][]plannedChange{pending}
	}
	batches := make([][]plannedChange, len(pending))
	for i, p := range pending {
		batches[i] = []plannedChange{p}
	}
	return batches
}

type plannedChange struct {
	idx int
	jid types.JID
}

func sameUser(a, b types.JID) bool {
	return a.User != "" && a.User == b.User && a.Server == b.Server
}

func participantMatches(p types.GroupParticipant, jid types.JID) bool {
	return sameUser(p.JID, jid) || sameUser(p.PhoneNumber, jid) || sameUser(p.LID, jid)
}

func findParticipant(info *types.GroupInfo, ids ...types.JID) *types.GroupParticipant {
	for i := range info.Participants {
		for _, id := range ids {
			if participantMatches(info.Participants[i], id) {
				return &info.Participants[i]
			}
		}
	}
	return nil
}

func checkParticipantPermission(info *types.GroupInfo, self *types.GroupParticipant, action whatsmeow.ParticipantChange) error {
	if self == nil {
		return fmt.Errorf("you are not a member of group %q; nothing was changed", info.Name)
	}
	if self.IsAdmin {
		return nil
	}
	if action == whatsmeow.ParticipantChangeAdd && info.MemberAddMode == types.GroupMemberAddModeAllMember {
		return nil
	}
	return fmt.Errorf("you are not an admin of group %q, so you cannot %s participants; nothing was changed", info.Name, action)
}

func planParticipantChange(info *types.GroupInfo, self *types.GroupParticipant, inputs []string, jids []types.JID, action whatsmeow.ParticipantChange, lidFor func(types.JID) types.JID) ([]ParticipantOutcome, []plannedChange, error) {
	outcomes := make([]ParticipantOutcome, len(jids))
	var pending []plannedChange
	seen := make(map[types.JID]int, len(jids))
	for i, jid := range jids {
		lid := lidFor(jid)
		member := findParticipant(info, jid, lid)
		identity := jid
		switch {
		case member != nil:
			identity = member.JID
		case !lid.IsEmpty():
			identity = lid
		}
		identity = identity.ToNonAD()
		if prev, dup := seen[identity]; dup {
			return nil, nil, fmt.Errorf("participants %q and %q are the same person; list each once", inputs[prev], inputs[i])
		}
		seen[identity] = i

		outcomes[i] = ParticipantOutcome{Input: inputs[i], JID: jid.String()}
		if member != nil {
			outcomes[i].JID = member.JID.String()
		}
		switch {
		case member != nil && member == self && action == whatsmeow.ParticipantChangeRemove:
			return nil, nil, errors.New("cannot remove yourself from the group; use leave_group instead")
		case action == whatsmeow.ParticipantChangeAdd && member != nil:
			outcomes[i].Status = statusAlreadyMember
		case action == whatsmeow.ParticipantChangeAdd:
			pending = append(pending, plannedChange{idx: i, jid: jid})
		case member == nil:
			outcomes[i].Status = statusNotMember
		default:
			pending = append(pending, plannedChange{idx: i, jid: member.JID})
		}
	}
	return outcomes, pending, nil
}

func matchResult(results []types.GroupParticipant, jid types.JID) (types.GroupParticipant, bool) {
	for _, r := range results {
		if participantMatches(r, jid) {
			return r, true
		}
	}
	return types.GroupParticipant{}, false
}

func (c *Client) resultFor(ctx context.Context, changed []types.GroupParticipant, jid types.JID, sent int) (types.GroupParticipant, bool) {
	if res, ok := matchResult(changed, jid); ok {
		return res, true
	}
	if jid.Server == types.DefaultUserServer {
		for _, r := range changed {
			if r.JID.Server != types.HiddenUserServer {
				continue
			}
			if pn, err := c.wa.Store.LIDs.GetPNForLID(ctx, r.JID); err == nil && sameUser(pn, jid) {
				return r, true
			}
		}
	}
	if sent == 1 && len(changed) == 1 {
		return changed[0], true
	}
	return types.GroupParticipant{}, false
}

func applyResult(out *ParticipantOutcome, action whatsmeow.ParticipantChange, res types.GroupParticipant) {
	out.JID = res.JID.String()
	out.ErrorCode = res.Error
	switch {
	case res.Error == 0:
		out.Status = participantSuccessStatus[action]
	case res.AddRequest != nil:
		out.Status = statusInviteRequired
		out.InviteExpiresAt = res.AddRequest.Expiration
	case action == whatsmeow.ParticipantChangeAdd && res.Error == errCodeAlreadyMember:
		out.Status = statusAlreadyMember
	default:
		out.Status = statusFailed
	}
}

func buildGroupInviteMessage(chat types.JID, groupName, caption string, req *types.GroupParticipantAddRequest) *waE2E.Message {
	invite := &waE2E.GroupInviteMessage{
		GroupJID:   proto.String(chat.String()),
		InviteCode: proto.String(req.Code),
		GroupName:  proto.String(groupName),
	}
	if !req.Expiration.IsZero() {
		invite.InviteExpiration = proto.Int64(req.Expiration.Unix())
	}
	if caption != "" {
		invite.Caption = proto.String(caption)
	}
	return &waE2E.Message{GroupInviteMessage: invite}
}

func (c *Client) inviteParticipant(ctx context.Context, out *ParticipantOutcome, to, chat types.JID, groupName, caption string, req *types.GroupParticipantAddRequest) {
	if req.Code == "" {
		out.Error = "WhatsApp returned no invite code, so no invite was sent"
		return
	}
	if err := c.checkSendWindow(ctx, to); err != nil {
		out.Error = fmt.Sprintf("invite not sent yet: %v; repeat the add for this participant after that to send it", err)
		return
	}
	msg := buildGroupInviteMessage(chat, groupName, caption, req)
	resp, err := c.wa.SendMessage(ctx, to, msg)
	if err != nil {
		out.Status = statusInviteFailed
		out.Error = fmt.Sprintf("send group invite: %v", err)
		return
	}
	summary := "Group invite: " + groupName
	if caption != "" {
		summary += " — " + caption
	}
	c.persistSent(ctx, to, resp.ID, summary, "", msg)
	out.Status = statusInviteSent
	out.InviteMessageID = resp.ID
}

func (c *Client) SetGroupName(ctx context.Context, chatJID, name string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupName(ctx, jid, name); err != nil {
		return fmt.Errorf("set group name: %w", err)
	}
	return nil
}

func (c *Client) SetGroupTopic(ctx context.Context, chatJID, topic string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupTopic(ctx, jid, "", "", topic); err != nil {
		return fmt.Errorf("set group topic: %w", err)
	}
	return nil
}

func (c *Client) SetGroupAnnounce(ctx context.Context, chatJID string, announceOnly bool) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupAnnounce(ctx, jid, announceOnly); err != nil {
		return fmt.Errorf("set group announce: %w", err)
	}
	return nil
}

func (c *Client) SetGroupLocked(ctx context.Context, chatJID string, locked bool) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %w", err)
	}
	if err := c.wa.SetGroupLocked(ctx, jid, locked); err != nil {
		return fmt.Errorf("set group locked: %w", err)
	}
	return nil
}

func (c *Client) GetGroupInviteLink(ctx context.Context, chatJID string, reset bool) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid chat JID: %w", err)
	}
	link, err := c.wa.GetGroupInviteLink(ctx, jid, reset)
	if err != nil {
		return "", fmt.Errorf("get group invite link: %w", err)
	}
	return link, nil
}

func (c *Client) JoinGroupWithLink(ctx context.Context, linkOrCode string) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	code := strings.TrimSpace(linkOrCode)
	if code == "" {
		return "", errors.New("invite link or code is required")
	}
	code = strings.TrimPrefix(code, whatsmeow.InviteLinkPrefix)
	jid, err := c.wa.JoinGroupWithLink(ctx, code)
	if err != nil {
		return "", fmt.Errorf("join group with link: %w", err)
	}
	return jid.String(), nil
}
