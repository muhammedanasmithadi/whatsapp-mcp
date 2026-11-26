package mcp

import (
	"context"
	"fmt"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) toolErr(toolName, chatJID, messageID, action string, err error) *mcp.CallToolResult {
	s.auditLog(toolName, chatJID, messageID, action, "error", err.Error())
	return errorResult(err)
}

func (s *Server) sendResult(toolName, chatJID, messageID string, r client.SendResult, errDetail string) (*mcp.CallToolResult, error) {
	if !r.Success {
		return s.toolErr(toolName, chatJID, messageID, toolName, fmt.Errorf("%s", r.Message)), nil
	}
	return s.toolOK(toolName, chatJID, messageID, toolName, "", r)
}

func (s *Server) toolOK(toolName, chatJID, messageID, action string, details string, result any) (*mcp.CallToolResult, error) {
	s.auditLog(toolName, chatJID, messageID, action, "success", details)
	return resultJSON(result)
}

func (s *Server) handleTool(ctx context.Context, toolName, chatJID, messageID, action string, fn func() (any, string, error)) (*mcp.CallToolResult, error) {
	result, details, err := fn()
	if err != nil {
		return s.toolErr(toolName, chatJID, messageID, action, err), nil
	}
	return s.toolOK(toolName, chatJID, messageID, action, details, result)
}
