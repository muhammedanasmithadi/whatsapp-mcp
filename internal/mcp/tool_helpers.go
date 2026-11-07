package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// toolErr handles a tool error consistently: audits the failure and returns an
// MCP error result. chatJID and messageID may be empty when not applicable.
// err is any Go error; *MCPError values are rendered as structured errors,
// everything else falls back to a plain text error.
func (s *Server) toolErr(toolName, chatJID, messageID, action string, err error) *mcp.CallToolResult {
	s.auditLog(toolName, chatJID, messageID, action, "error", err.Error())
	return errorResult(err)
}

// toolOK handles a tool success consistently: audits the success and returns a
// JSON result. details is a short human-readable summary stored in the audit
// log.
func (s *Server) toolOK(toolName, chatJID, messageID, action string, details string, result any) (*mcp.CallToolResult, error) {
	s.auditLog(toolName, chatJID, messageID, action, "success", details)
	return resultJSON(result)
}

// handleTool runs fn, and on error reports it via toolErr; on success it
// reports it via toolOK. chatJID and messageID are used for audit logging and
// may be empty when not applicable. This eliminates the repetitive
// validate→call→error/success boilerplate that every tool handler repeats.
func (s *Server) handleTool(ctx context.Context, toolName, chatJID, messageID, action string, fn func() (any, string, error)) (*mcp.CallToolResult, error) {
	result, details, err := fn()
	if err != nil {
		return s.toolErr(toolName, chatJID, messageID, action, err), nil
	}
	return s.toolOK(toolName, chatJID, messageID, action, details, result)
}
