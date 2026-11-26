package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestChannelPostTools_ArgValidation(t *testing.T) {
	s := NewServer(nil, nil, nil)
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{name: "missing channel_jid", tool: "send_channel_message", args: map[string]any{}},
		{name: "empty channel_jid", tool: "send_channel_message", args: map[string]any{"channel_jid": "", "message": "hello"}},
		{name: "empty message without media_path", tool: "send_channel_message", args: map[string]any{"channel_jid": "1234567890@newsletter"}},
		{name: "missing name", tool: "create_channel", args: map[string]any{}},
		{name: "empty name", tool: "create_channel", args: map[string]any{"name": "", "description": "ops"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := s.MCP().ListTools()[tc.tool]
			if !ok || tool == nil || tool.Handler == nil {
				t.Fatalf("%s not registered", tc.tool)
			}
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tc.args
			res, err := tool.Handler(context.Background(), req)
			if err != nil {
				t.Fatalf("%s handler returned error: %v", tc.tool, err)
			}
			if !res.IsError {
				t.Fatalf("%s with args %v: want a rejected (IsError) result, got %+v", tc.tool, tc.args, res)
			}
		})
	}
}

func TestChannelPostTools_Registered(t *testing.T) {
	s := NewServer(nil, nil, nil)
	tools := s.MCP().ListTools()

	send, ok := tools["send_channel_message"]
	if !ok || send == nil || send.Handler == nil {
		t.Fatal("send_channel_message not registered")
	}
	if send.Tool.Annotations.ReadOnlyHint != nil && *send.Tool.Annotations.ReadOnlyHint {
		t.Error("send_channel_message must not be annotated read-only")
	}
	if send.Tool.Annotations.DestructiveHint == nil || !*send.Tool.Annotations.DestructiveHint {
		t.Error("send_channel_message must be annotated destructive: a channel post cannot be deleted")
	}
	if send.Tool.Annotations.IdempotentHint == nil || *send.Tool.Annotations.IdempotentHint {
		t.Error("send_channel_message must not be annotated idempotent: each call publishes a new post")
	}
	for _, phrase := range []string{"every follower sees the post", "no delete for a channel post"} {
		if !strings.Contains(send.Tool.Description, phrase) {
			t.Errorf("send_channel_message description must state %q", phrase)
		}
	}
	for _, arg := range []string{"channel_jid", "message", "media_path"} {
		if _, ok := send.Tool.InputSchema.Properties[arg]; !ok {
			t.Errorf("send_channel_message schema is missing the %q argument", arg)
		}
	}
	if _, ok := send.Tool.InputSchema.Properties["view_once"]; ok {
		t.Error("send_channel_message must not expose view_once: the channel protocol has no view-once post")
	}

	create, ok := tools["create_channel"]
	if !ok || create == nil || create.Handler == nil {
		t.Fatal("create_channel not registered")
	}
	if create.Tool.Annotations.ReadOnlyHint != nil && *create.Tool.Annotations.ReadOnlyHint {
		t.Error("create_channel must not be annotated read-only")
	}
	for _, arg := range []string{"name", "description", "picture_path"} {
		if _, ok := create.Tool.InputSchema.Properties[arg]; !ok {
			t.Errorf("create_channel schema is missing the %q argument", arg)
		}
	}
}
