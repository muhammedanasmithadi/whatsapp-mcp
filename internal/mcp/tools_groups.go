package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

// registerGroupTools wires every group-management MCP tool.
func (s *Server) registerGroupTools() {
	s.registerCreateGroup()
	s.registerLeaveGroup()
	s.registerListGroups()
	s.registerGetGroupInfo()
	s.registerUpdateGroupParticipants()
	s.registerSetGroupName()
	s.registerSetGroupTopic()
	s.registerSetGroupAnnounce()
	s.registerSetGroupLocked()
	s.registerGetGroupInviteLink()
	s.registerJoinGroupWithLink()
	s.registerSetGroupPhoto()
	s.registerSetGroupJoinApproval()
	s.registerSetGroupMemberAddMode()
	s.registerListGroupJoinRequests()
	s.registerResolveGroupJoinRequest()
}

type createGroupArgs struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
}

func (s *Server) registerCreateGroup() {
	tool := mcp.NewTool("create_group",
		mcp.WithDescription("Create a new WhatsApp group with the given name and initial participants; the paired user becomes admin and listed participants receive a `you were added` system message in the new chat. Reversible by calling leave_group (irreversible itself) or update_group_participants with `remove`. Returns a JSON object `{jid, info}` where `jid` is the new group's JID and `info` is the freshly-fetched group metadata."),
		mcp.WithString("name", mcp.Required(), mcp.Description("group display name (subject)")),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("initial members as bare phone digits or individual JIDs ("+jidDesc+")")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a createGroupArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("name", a.Name); r != nil {
			return r, nil
		}
		if len(a.Participants) == 0 {
			return errorResult(ErrInvalidParameter("participants", "non-empty array", "empty array")), nil
		}
		jid, info, err := s.client.CreateGroup(ctx, a.Name, a.Participants)
		if err != nil {
			s.auditLog("create_group", "", "", "create_group", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error creating group", true)), nil
		}
		s.auditLog("create_group", jid, "", "create_group", "success", "created group")
		return resultJSON(map[string]any{"jid": jid, "info": json.RawMessage(info)})
	}))
}

type leaveGroupArgs struct {
	ChatJID string `json:"chat_jid"`
}

func (s *Server) registerLeaveGroup() {
	tool := mcp.NewTool("leave_group",
		mcp.WithDescription("Leave a WhatsApp group; remaining members see a `you left` system message and the paired user loses access to all future messages in the chat. Permanent — to rejoin you must be re-added by an admin or invited via a fresh link (join_group_with_link). Prefer setting privacy or muting on the client if you only want silence. Returns the plain-text string `Left group <chat_jid>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a leaveGroupArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.LeaveGroup(ctx, a.ChatJID); err != nil {
			s.auditLog("leave_group", a.ChatJID, "", "leave_group", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error leaving group", true)), nil
		}
		s.auditLog("leave_group", a.ChatJID, "", "leave_group", "success", "left group")
		return mcp.NewToolResultText(fmt.Sprintf("Left group %s", a.ChatJID)), nil
	}))
}

func (s *Server) registerListGroups() {
	tool := mcp.NewTool("list_groups",
		mcp.WithDescription("List every WhatsApp group the paired user is currently a member of, fetched live from WhatsApp. Read-only; no side effects. Use get_group_info for detailed metadata about one specific group. Returns a JSON array of group-info objects (each with JID, subject, participants, settings, and so on)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		js, err := s.client.ListJoinedGroups(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	})
}

type getGroupInfoArgs struct {
	ChatJID string `json:"chat_jid"`
}

func (s *Server) registerGetGroupInfo() {
	tool := mcp.NewTool("get_group_info",
		mcp.WithDescription("Fetch live group metadata (subject, topic, participants with admin flags, announce/locked settings, invite config) for the given group JID. Read-only; no side effects. Use list_groups to discover which groups exist. Returns a JSON object describing the group."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getGroupInfoArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		js, err := s.client.GetGroupInfoJSON(ctx, a.ChatJID)
		if err != nil {
			s.auditLog("get_group_info", a.ChatJID, "", "get_group_info", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error fetching group info", true)), nil
		}
		return mcp.NewToolResultText(js), nil
	}))
}

type updateGroupParticipantsArgs struct {
	ChatJID       string   `json:"chat_jid"`
	Participants  []string `json:"participants"`
	Action        string   `json:"action"`
	SendInvite    *bool    `json:"send_invite,omitempty"`
	InviteMessage string   `json:"invite_message,omitempty"`
}

func (s *Server) registerUpdateGroupParticipants() {
	tool := mcp.NewTool("update_group_participants",
		mcp.WithDescription("Add, remove, promote, or demote participants of a group after first checking that the paired user is a group admin (or, for `add`, that the group lets every member add) — if not, it fails and nothing changes. The group shows a system message for each change; people whose privacy settings block being added directly get a WhatsApp group-invite card in their 1:1 chat instead (unless `send_invite=false`) and join by tapping it; invites share the outbound rate limit (about one per 45–90 s), so in a batch the rest come back as `invite_required` with a retry hint — repeat the add for them later. Reversible by calling again with the inverse action (`add` ↔ `remove`, `promote` ↔ `demote`), though a sent invite cannot be recalled. Returns `{chat_jid, group_name, action, is_admin, results: [{input, jid, status, error_code?, error?, invite_message_id?, invite_expires_at?}]}` where status is one of added, removed, promoted, demoted, already_member, not_member, invite_sent, invite_required, invite_failed, failed."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description("group JID as `<digits>-<timestamp>@g.us` or `<digits>@g.us` (use list_groups to find it)")),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("people to change, as phone digits only (E.164 without `+`, no spaces) or individual JIDs (`<digits>@s.whatsapp.net` or `<digits>@lid`); each at most once")),
		mcp.WithString("action", mcp.Required(), mcp.Enum("add", "remove", "promote", "demote"), mcp.Description("mutation to perform: `add`, `remove`, `promote` (to admin), or `demote` (from admin)")),
		mcp.WithBoolean("send_invite", mcp.DefaultBool(true), mcp.Description("only for `add`: when someone's privacy settings block adding them directly, send them a group-invite card in their 1:1 chat (defaults to true); false just reports `invite_required`")),
		mcp.WithString("invite_message", mcp.Description("optional caption shown on the invite card (defaults to none); ignored unless an invite is sent")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		// Not idempotent: repeating an add re-sends invites to anyone who
		// has not joined yet.
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a updateGroupParticipantsArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if len(a.Participants) == 0 {
			return errorResult(ErrInvalidParameter("participants", "non-empty array", "empty array")), nil
		}
		report, err := s.client.UpdateGroupParticipants(ctx, a.ChatJID, a.Participants, a.Action, client.ParticipantUpdateOptions{
			SendInvite:    a.SendInvite == nil || *a.SendInvite,
			InviteMessage: a.InviteMessage,
		})
		if err != nil {
			s.auditLog("update_group_participants", a.ChatJID, "", "update_group_participants", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error updating group participants", true)), nil
		}
		return toolResult(report, err)
	}))
}

type setGroupNameArgs struct {
	ChatJID string `json:"chat_jid"`
	Name    string `json:"name"`
}

func (s *Server) registerSetGroupName() {
	tool := mcp.NewTool("set_group_name",
		mcp.WithDescription("Change a group's display name (its `subject`); members see a system message naming the new subject. Reversible by calling again with the previous name. Prerequisite: admin (or non-locked group). Returns the plain-text string `Group <chat_jid> renamed to \"<name>\"`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("name", mcp.Required(), mcp.Description("new group subject (display name)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupNameArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("name", a.Name); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupName(ctx, a.ChatJID, a.Name); err != nil {
			s.auditLog("set_group_name", a.ChatJID, "", "set_group_name", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error setting group name", true)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s renamed to %q", a.ChatJID, a.Name)), nil
	}))
}

type setGroupTopicArgs struct {
	ChatJID string `json:"chat_jid"`
	Topic   string `json:"topic"`
}

func (s *Server) registerSetGroupTopic() {
	tool := mcp.NewTool("set_group_topic",
		mcp.WithDescription("Change a group's description/topic; members see a system message indicating the description was updated. Reversible by calling again with the previous text or with an empty string to clear. Prerequisite: admin (or non-locked group). Returns the plain-text string `Group <chat_jid> topic updated`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("topic", mcp.Description("new topic/description text; pass an empty string to clear the topic")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupTopicArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupTopic(ctx, a.ChatJID, a.Topic); err != nil {
			s.auditLog("set_group_topic", a.ChatJID, "", "set_group_topic", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error setting group topic", true)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s topic updated", a.ChatJID)), nil
	}))
}

type setGroupAnnounceArgs struct {
	ChatJID      string `json:"chat_jid"`
	AnnounceOnly bool   `json:"announce_only"`
}

func (s *Server) registerSetGroupAnnounce() {
	tool := mcp.NewTool("set_group_announce",
		mcp.WithDescription("Toggle announce-only mode on a group; when enabled, non-admin send attempts are rejected by WhatsApp servers and members see a system message about the change. Reversible by calling again with the inverse value. Prerequisite: admin. See set_group_locked for restricting metadata edits. Returns the plain-text string `Group <chat_jid> announce_only=<bool>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("announce_only", mcp.Required(), mcp.Description("true to lock posting to admins only, false to allow all members to post")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupAnnounceArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupAnnounce(ctx, a.ChatJID, a.AnnounceOnly); err != nil {
			s.auditLog("set_group_announce", a.ChatJID, "", "set_group_announce", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error setting group announce mode", true)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s announce_only=%t", a.ChatJID, a.AnnounceOnly)), nil
	}))
}

type setGroupLockedArgs struct {
	ChatJID string `json:"chat_jid"`
	Locked  bool   `json:"locked"`
}

func (s *Server) registerSetGroupLocked() {
	tool := mcp.NewTool("set_group_locked",
		mcp.WithDescription("Toggle locked mode on a group; when enabled, only admins can change name/topic/icon and members see a system message about the change. Reversible by calling again with the inverse value. Prerequisite: admin. See set_group_announce for restricting who can post. Returns the plain-text string `Group <chat_jid> locked=<bool>`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("locked", mcp.Required(), mcp.Description("true to restrict subject/topic/icon edits to admins, false to allow all members")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupLockedArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupLocked(ctx, a.ChatJID, a.Locked); err != nil {
			s.auditLog("set_group_locked", a.ChatJID, "", "set_group_locked", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error setting group locked mode", true)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Group %s locked=%t", a.ChatJID, a.Locked)), nil
	}))
}

type getGroupInviteLinkArgs struct {
	ChatJID string `json:"chat_jid"`
	Reset   bool   `json:"reset,omitempty"`
}

func (s *Server) registerGetGroupInviteLink() {
	tool := mcp.NewTool("get_group_invite_link",
		mcp.WithDescription("Return a group's current invite link, or revoke and mint a fresh one when `reset=true`. Warning: `reset=true` permanently invalidates the previous link — anyone holding a previously-shared copy can no longer use it to join, and there is no undo. Prerequisite: admin. Use join_group_with_link to consume an invite. Returns a JSON object `{link}` containing the active invite URL."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("reset", mcp.DefaultBool(false), mcp.Description("if true, permanently revoke the existing invite link and mint a new one (defaults to false); previously-shared copies stop working")),
		// Hints reflect the worst case: reset=true permanently revokes the
		// old link, so this is not read-only and is destructive.
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getGroupInviteLinkArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		link, err := s.client.GetGroupInviteLink(ctx, a.ChatJID, a.Reset)
		if err != nil {
			s.auditLog("get_group_invite_link", a.ChatJID, "", "get_group_invite_link", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error getting group invite link", true)), nil
		}
		return toolResult(map[string]string{"link": link}, nil)
	}))
}

type joinGroupArgs struct {
	LinkOrCode string `json:"link_or_code"`
}

func (s *Server) registerJoinGroupWithLink() {
	tool := mcp.NewTool("join_group_with_link",
		mcp.WithDescription("Join a WhatsApp group via a `chat.whatsapp.com/<code>` invite URL or the bare invite code; existing members see a `joined via invite link` system message and the new chat appears for the paired user. Reversible via leave_group (which is itself permanent). Use get_group_invite_link to mint or read invite links. Returns a JSON object `{jid}` containing the joined group's JID."),
		mcp.WithString("link_or_code", mcp.Required(), mcp.Description("full invite URL (`https://chat.whatsapp.com/<code>`) or just the trailing invite code")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a joinGroupArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("link_or_code", a.LinkOrCode); r != nil {
			return r, nil
		}
		jid, err := s.client.JoinGroupWithLink(ctx, a.LinkOrCode)
		if err != nil {
			s.auditLog("join_group_with_link", "", "", "join_group_with_link", "error", err.Error())
			return errorResult(ErrWhatsAppAPI(err.Error(), "WhatsApp API returned an error joining group", true)), nil
		}
		return toolResult(map[string]string{"jid": jid}, nil)
	}))
}

type setGroupPhotoArgs struct {
	ChatJID   string `json:"chat_jid"`
	PhotoPath string `json:"photo_path"`
}

func (s *Server) registerSetGroupPhoto() {
	tool := mcp.NewTool("set_group_photo",
		mcp.WithDescription("Replace a group's photo with the image file at photo_path; every member sees the new icon in the chat list and inside the group. Only admins may change a group photo. Irreversible to a previous version — the old icon is overwritten, so save a copy first with get_profile_picture when the current one matters. Accepts JPEG bytes through the media-root allowlist; other formats may be rejected by WhatsApp with ErrInvalidImageFormat. Returns a JSON object {chat_jid, photo_id} where photo_id is the new photo handle/id."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("photo_path", mcp.Required(), mcp.Description("path to a JPEG image file inside the daemon's media root")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupPhotoArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("photo_path", a.PhotoPath); r != nil {
			return r, nil
		}
		photoID, err := s.client.SetGroupPhoto(ctx, a.ChatJID, a.PhotoPath)
		if err != nil {
			return s.toolErr("set_group_photo", a.ChatJID, "", "set_group_photo", err), nil
		}
		return s.toolOK("set_group_photo", a.ChatJID, "", "set_group_photo", "group photo updated", map[string]any{"chat_jid": a.ChatJID, "photo_id": photoID})
	}))
}

type setGroupJoinApprovalArgs struct {
	ChatJID string `json:"chat_jid"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) registerSetGroupJoinApproval() {
	tool := mcp.NewTool("set_group_join_approval",
		mcp.WithDescription("Toggle admin approval for join requests on a group: when enabled, a person who follows a group invite link waits in a pending state instead of joining, and only an admin can admit or decline them (resolve_group_join_request, list_group_join_requests). Existing members are not asked and see a system message about the setting change. Reversible by calling again with the inverse value; pending requests survive the toggle either way. Prerequisite: admin. Returns a JSON object {chat_jid, enabled}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("enabled", mcp.Required(), mcp.Description("true to hold invite-link joins for admin approval, false to let them join immediately")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupJoinApprovalArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if err := s.client.SetGroupJoinApprovalMode(ctx, a.ChatJID, a.Enabled); err != nil {
			return s.toolErr("set_group_join_approval", a.ChatJID, "", "set_group_join_approval", err), nil
		}
		return s.toolOK("set_group_join_approval", a.ChatJID, "", "set_group_join_approval", fmt.Sprintf("join approval enabled=%t", a.Enabled), map[string]any{"chat_jid": a.ChatJID, "enabled": a.Enabled})
	}))
}

type setGroupMemberAddModeArgs struct {
	ChatJID string `json:"chat_jid"`
	Mode    string `json:"mode"`
}

func (s *Server) registerSetGroupMemberAddMode() {
	tool := mcp.NewTool("set_group_member_add_mode",
		mcp.WithDescription("Set who may add participants to a group: `admin_add` restricts add/remove/promote/demote to admins, `all_member_add` lets every member add others. Affects update_group_participants — a non-admin add in an admin_add group is refused before anything changes. Members see a system message about the setting change. Reversible by calling again with the inverse value. Prerequisite: admin. Returns a JSON object {chat_jid, mode}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("mode", mcp.Required(), mcp.Enum(client.GroupMemberAddModes...), mcp.Description("`admin_add` (admins only) or `all_member_add` (every member may add)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a setGroupMemberAddModeArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if !slices.Contains(client.GroupMemberAddModes, a.Mode) {
			return errorResult(ErrInvalidParameter("mode", strings.Join(client.GroupMemberAddModes, ", "), a.Mode)), nil
		}
		if err := s.client.SetGroupMemberAddMode(ctx, a.ChatJID, a.Mode); err != nil {
			return s.toolErr("set_group_member_add_mode", a.ChatJID, "", "set_group_member_add_mode", err), nil
		}
		return s.toolOK("set_group_member_add_mode", a.ChatJID, "", "set_group_member_add_mode", fmt.Sprintf("member add mode=%s", a.Mode), map[string]any{"chat_jid": a.ChatJID, "mode": a.Mode})
	}))
}

type listGroupJoinRequestsArgs struct {
	ChatJID string `json:"chat_jid"`
}

func (s *Server) registerListGroupJoinRequests() {
	tool := mcp.NewTool("list_group_join_requests",
		mcp.WithDescription("List the people waiting for admin approval to join a group, with the JID of each requester and the time of the request. Read-only; no side effects and nothing is sent to the requesters. Returns an empty list when join approval is off (set_group_join_approval) or nobody is waiting. Use resolve_group_join_request to admit or decline. Returns a JSON object {chat_jid, requests: [{jid, requested_at}]}."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listGroupJoinRequestsArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		requests, err := s.client.ListGroupJoinRequests(ctx, a.ChatJID)
		if err != nil {
			return s.toolErr("list_group_join_requests", a.ChatJID, "", "list_group_join_requests", err), nil
		}
		return s.toolOK("list_group_join_requests", a.ChatJID, "", "list_group_join_requests", fmt.Sprintf("listed %d join requests", len(requests)), map[string]any{"chat_jid": a.ChatJID, "requests": requests})
	}))
}

type resolveGroupJoinRequestArgs struct {
	ChatJID      string   `json:"chat_jid"`
	Participants []string `json:"participants"`
	Action       string   `json:"action"`
}

func (s *Server) registerResolveGroupJoinRequest() {
	tool := mcp.NewTool("resolve_group_join_request",
		mcp.WithDescription("Admit or decline people waiting for approval to join a group. Approving puts them in the group, and every member sees a `joined` system message for each admitted person; declining silently drops the request — no system message is posted and the requester is not told. Each request resolves once; resolving it again returns an error for that participant. Not reversible in the same shape: a declined requester must ask again, and an admitted member has to be removed through update_group_participants. Works while join approval is on (set_group_join_approval). Prerequisite: admin. Returns a JSON object {chat_jid, action, results: [{jid, phone_number?, lid?, error_code?}]} where error_code is set only for outcomes WhatsApp rejected."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithArray("participants", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("people to resolve, as phone digits only (E.164 without `+`, no spaces) or individual JIDs (`<digits>@s.whatsapp.net` or `<digits>@lid`); the JIDs list_group_join_requests returned work as-is")),
		mcp.WithString("action", mcp.Required(), mcp.Enum(client.ParticipantRequestActions...), mcp.Description("`approve` to admit the requester, `reject` to decline the request")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a resolveGroupJoinRequestArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if len(a.Participants) == 0 {
			return errorResult(ErrInvalidParameter("participants", "non-empty array", "empty array")), nil
		}
		if !slices.Contains(client.ParticipantRequestActions, a.Action) {
			return errorResult(ErrInvalidParameter("action", strings.Join(client.ParticipantRequestActions, ", "), a.Action)), nil
		}
		outcomes, err := s.client.ResolveGroupJoinRequest(ctx, a.ChatJID, a.Participants, a.Action)
		if err != nil {
			return s.toolErr("resolve_group_join_request", a.ChatJID, "", "resolve_group_join_request", err), nil
		}
		return s.toolOK("resolve_group_join_request", a.ChatJID, "", "resolve_group_join_request", fmt.Sprintf("%sd %d join requests", a.Action, len(outcomes)), map[string]any{"chat_jid": a.ChatJID, "action": a.Action, "results": outcomes})
	}))
}
