package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestRequireBoolPresent(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"absent", map[string]any{"chat_jid": "x"}, true},
		{"explicit false", map[string]any{"muted": false}, false},
		{"explicit true", map[string]any{"muted": true}, false},
		{"nil arguments", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tc.args
			if got := requireBoolPresent(req, "muted") != nil; got != tc.want {
				t.Errorf("rejected = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMuteChatRejectsMissingMuted(t *testing.T) {
	s := NewServer(nil, nil, nil)
	res := callToolRaw(t, s, "mute_chat", map[string]any{"chat_jid": "919847144512@s.whatsapp.net"})
	if !res.IsError {
		t.Fatal("mute_chat must reject a call with no muted argument: its zero value would unmute the chat")
	}
}

func TestPinChatRejectsMissingPinned(t *testing.T) {
	s := NewServer(nil, nil, nil)
	res := callToolRaw(t, s, "pin_chat", map[string]any{"chat_jid": "919847144512@s.whatsapp.net"})
	if !res.IsError {
		t.Fatal("pin_chat must reject a call with no pinned argument")
	}
}

func TestStarMessageRejectsMissingStarred(t *testing.T) {
	s := NewServer(nil, nil, nil)
	res := callToolRaw(t, s, "star_message", map[string]any{"chat_jid": "919847144512@s.whatsapp.net", "message_id": "ABC"})
	if !res.IsError {
		t.Fatal("star_message must reject a call with no starred argument: its zero value would unstar")
	}
}

func callToolRaw(t *testing.T, s *Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool, ok := s.MCP().ListTools()[name]
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}
