package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

func callGroupAdminTool(t *testing.T, s *Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool, ok := s.MCP().ListTools()[name]
	if !ok || tool == nil || tool.Handler == nil {
		t.Fatalf("tool %q not registered", name)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("%s handler returned a transport error: %v", name, err)
	}
	return res
}

func TestGroupAdminTools_AreRegistered(t *testing.T) {
	s := NewServer(nil, nil, nil)
	for _, name := range []string{
		"set_group_photo",
		"set_group_join_approval",
		"set_group_member_add_mode",
		"list_group_join_requests",
		"resolve_group_join_request",
	} {
		tool := s.MCP().GetTool(name)
		if tool == nil {
			t.Errorf("tool %q not registered", name)
			continue
		}
		if tool.Handler == nil {
			t.Errorf("tool %q has a nil handler", name)
		}
	}
}

func TestGroupAdminTools_RequiredArgs(t *testing.T) {
	const groupJID = "120363000000000000@g.us"
	s := NewServer(nil, nil, nil)
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"set_group_photo without chat_jid", "set_group_photo", map[string]any{"photo_path": "/media/photo.jpg"}},
		{"set_group_photo without photo_path", "set_group_photo", map[string]any{"chat_jid": groupJID}},
		{"set_group_join_approval without chat_jid", "set_group_join_approval", map[string]any{"enabled": true}},
		{"set_group_member_add_mode without chat_jid", "set_group_member_add_mode", map[string]any{"mode": "admin_add"}},
		{"set_group_member_add_mode without mode", "set_group_member_add_mode", map[string]any{"chat_jid": groupJID}},
		{"list_group_join_requests without chat_jid", "list_group_join_requests", map[string]any{}},
		{"resolve_group_join_request without chat_jid", "resolve_group_join_request", map[string]any{"participants": []string{"447700000001"}, "action": "approve"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callGroupAdminTool(t, s, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("args %v: want a rejected (IsError) result, got %+v", tc.args, res)
			}
		})
	}
}

func TestGroupAdminTools_EnumValidation(t *testing.T) {
	const groupJID = "120363000000000000@g.us"
	s := NewServer(nil, nil, nil)
	badMode := []string{"admins", "everyone", "ADMIN", "", "all_member_adds"}
	for _, mode := range badMode {
		res := callGroupAdminTool(t, s, "set_group_member_add_mode", map[string]any{"chat_jid": groupJID, "mode": mode})
		if !res.IsError {
			t.Errorf("set_group_member_add_mode mode=%q: want a rejected result, got %+v", mode, res)
		}
		if !res.IsError && mode != "" {
			t.Errorf("set_group_member_add_mode mode=%q should have been rejected", mode)
		}
	}
	badActions := []string{"ban", "accept", "APPROVES", "", "declines"}
	for _, action := range badActions {
		res := callGroupAdminTool(t, s, "resolve_group_join_request", map[string]any{"chat_jid": groupJID, "participants": []string{"447700000001"}, "action": action})
		if !res.IsError {
			t.Errorf("resolve_group_join_request action=%q: want a rejected result, got %+v", action, res)
		}
	}
	emptyParticipants := []string{}
	res := callGroupAdminTool(t, s, "resolve_group_join_request", map[string]any{"chat_jid": groupJID, "participants": emptyParticipants, "action": "approve"})
	if !res.IsError {
		t.Errorf("resolve_group_join_request with no participants: want a rejected result, got %+v", res)
	}
}

func TestGroupAdminTools_EnumErrorNamesAllowedValues(t *testing.T) {
	const groupJID = "120363000000000000@g.us"
	s := NewServer(nil, nil, nil)
	modeRes := callGroupAdminTool(t, s, "set_group_member_add_mode", map[string]any{"chat_jid": groupJID, "mode": "turbo"})
	actionRes := callGroupAdminTool(t, s, "resolve_group_join_request", map[string]any{"chat_jid": groupJID, "participants": []string{"447700000001"}, "action": "turbo"})
	for _, want := range client.GroupMemberAddModes {
		if !strings.Contains(toolText(modeRes), want) {
			t.Errorf("mode error %q does not name %q", toolText(modeRes), want)
		}
	}
	for _, want := range client.ParticipantRequestActions {
		if !strings.Contains(toolText(actionRes), want) {
			t.Errorf("action error %q does not name %q", toolText(actionRes), want)
		}
	}
}

func toolText(res *mcp.CallToolResult) string {
	out := ""
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

func TestGroupAdminTools_SchemaEnums(t *testing.T) {
	s := NewServer(nil, nil, nil)
	modeTool := s.MCP().GetTool("set_group_member_add_mode")
	if modeTool == nil {
		t.Fatal("set_group_member_add_mode not registered")
	}
	schema, err := mcpSchemaProperty(modeTool.Tool.InputSchema, "mode")
	if err != nil {
		t.Fatalf("mode property missing from schema: %v", err)
	}
	for _, want := range client.GroupMemberAddModes {
		if !strings.Contains(schema, want) {
			t.Errorf("mode schema %q does not offer %q", schema, want)
		}
	}
	actionTool := s.MCP().GetTool("resolve_group_join_request")
	if actionTool == nil {
		t.Fatal("resolve_group_join_request not registered")
	}
	schema, err = mcpSchemaProperty(actionTool.Tool.InputSchema, "action")
	if err != nil {
		t.Fatalf("action property missing from schema: %v", err)
	}
	for _, want := range client.ParticipantRequestActions {
		if !strings.Contains(schema, want) {
			t.Errorf("action schema %q does not offer %q", schema, want)
		}
	}
}

func TestGroupAdminTools_Hints(t *testing.T) {
	s := NewServer(nil, nil, nil)
	readOnly := map[string]bool{"list_group_join_requests": true}
	for _, name := range []string{
		"set_group_photo",
		"set_group_join_approval",
		"set_group_member_add_mode",
		"list_group_join_requests",
		"resolve_group_join_request",
	} {
		tool := s.MCP().GetTool(name)
		if tool == nil {
			t.Fatalf("tool %q not registered", name)
		}
		ann := tool.Tool.Annotations
		if ann.ReadOnlyHint == nil {
			t.Fatalf("tool %q has no read-only hint", name)
		}
		want := !readOnly[name]
		if ann.ReadOnlyHint != nil && *ann.ReadOnlyHint == want {
			t.Errorf("tool %q ReadOnlyHint = %v, want %v", name, *ann.ReadOnlyHint, want)
		}
	}
}
