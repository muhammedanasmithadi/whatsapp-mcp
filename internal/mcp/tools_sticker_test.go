package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func stickerToolText(res *mcp.CallToolResult) string {
	out := ""
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

func callStickerTool(t *testing.T, s *Server, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool := s.MCP().GetTool(name)
	if tool == nil || tool.Handler == nil {
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

func requireStickerArgs(t *testing.T, s *Server, name string, want []string) {
	t.Helper()
	tool := s.MCP().GetTool(name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	for _, w := range want {
		found := false
		for _, got := range tool.Tool.InputSchema.Required {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tool %q: %q is not a required argument", name, w)
		}
	}
}

func TestSendSticker_ToolContract(t *testing.T) {
	s := NewServer(nil, nil, nil)
	requireStickerArgs(t, s, "send_sticker", []string{"recipient", "media_path"})
	tool := s.MCP().GetTool("send_sticker")
	ann := tool.Tool.Annotations
	if ann.ReadOnlyHint == nil || *ann.ReadOnlyHint {
		t.Errorf("send_sticker ReadOnlyHint = %v, want false", ann.ReadOnlyHint)
	}
	if ann.DestructiveHint == nil || *ann.DestructiveHint {
		t.Errorf("send_sticker DestructiveHint = %v, want false", ann.DestructiveHint)
	}
}

func TestSendSticker_MissingArgs(t *testing.T) {
	s := NewServer(nil, nil, nil)
	cases := map[string]map[string]any{
		"empty recipient":  {"recipient": "", "media_path": "/media/sticker.webp"},
		"empty media_path": {"recipient": "447700000001", "media_path": ""},
		"both empty":       {"recipient": "", "media_path": ""},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			res := callStickerTool(t, s, "send_sticker", args)
			if !res.IsError {
				t.Fatalf("args %v: want a rejected result, got %+v", args, res)
			}
			if text := stickerToolText(res); text == "" {
				t.Fatalf("rejected result carries no message")
			}
		})
	}
}

func TestSendSticker_NotConnected(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)
	res := callStickerTool(t, s, "send_sticker", map[string]any{
		"recipient":  "447700000001",
		"media_path": "sticker.webp",
	})
	if !res.IsError {
		t.Fatalf("want a rejected result for a disconnected client, got %+v", res)
	}
	if got := stickerToolText(res); !strings.Contains(got, "Not connected to WhatsApp") {
		t.Fatalf("error text = %q, want the not-connected message", got)
	}
}

func TestSendSticker_RejectsPathOutsideMediaRoot(t *testing.T) {
	s, _, _ := newTestServerWithStore(t)
	res := callStickerTool(t, s, "send_sticker", map[string]any{
		"recipient":  "447700000001",
		"media_path": "../../etc/passwd",
	})
	if !res.IsError {
		t.Fatalf("want a rejected result for a path outside the media root, got %+v", res)
	}
}

func TestGetStickerPack_ToolContract(t *testing.T) {
	s := NewServer(nil, nil, nil)
	requireStickerArgs(t, s, "get_sticker_pack", []string{"pack_id"})
	tool := s.MCP().GetTool("get_sticker_pack")
	ann := tool.Tool.Annotations
	if ann.ReadOnlyHint == nil || !*ann.ReadOnlyHint {
		t.Errorf("get_sticker_pack ReadOnlyHint = %v, want true", ann.ReadOnlyHint)
	}
	if ann.DestructiveHint == nil || *ann.DestructiveHint {
		t.Errorf("get_sticker_pack DestructiveHint = %v, want false", ann.DestructiveHint)
	}
}

func TestGetStickerPack_EmptyPackID(t *testing.T) {
	s := NewServer(nil, nil, nil)
	res := callStickerTool(t, s, "get_sticker_pack", map[string]any{"pack_id": "   "})
	if !res.IsError {
		t.Fatalf("want a rejected result for an empty pack_id, got %+v", res)
	}
	if got := stickerToolText(res); !strings.Contains(got, "pack_id") {
		t.Fatalf("error text = %q, want the pack_id validation message", got)
	}
}
