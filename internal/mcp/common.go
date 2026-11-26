package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/ratelimit"
)

const rateLimitOverrideHeader = "X-Rate-Limit-Override"

func withRateLimitOverride(ctx context.Context, req mcp.CallToolRequest) context.Context {
	switch req.Header.Get(rateLimitOverrideHeader) {
	case "true", "1", "yes", "TRUE", "True":
		return ratelimit.WithBypass(ctx)
	default:
		return ctx
	}
}

const jidDesc = "WhatsApp JID: individual as `<digits>@s.whatsapp.net` or bare phone digits, group as `<digits>-<timestamp>@g.us`"

const recipientDesc = "Send target: digits only (E.164 without `+`, no spaces or punctuation); or `<digits>@s.whatsapp.net`; or group `<digits>-<timestamp>@g.us`"

const offlineSafePrefix = "(reads local cache; works while disconnected) "

func requireNonEmpty(name, value string) *mcp.CallToolResult {
	if value == "" {
		return mcp.NewToolResultError(name + ": required")
	}
	return nil
}

func requireBoolPresent(req mcp.CallToolRequest, name string) *mcp.CallToolResult {
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return errorResult(ErrInvalidParameter(name, "a boolean", "missing arguments"))
	}
	if _, present := args[name]; !present {
		return errorResult(ErrInvalidParameter(name, "a boolean (true or false)", "omitted"))
	}
	return nil
}

func toolResult(payload any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return resultJSON(payload)
}
