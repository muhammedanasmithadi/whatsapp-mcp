package mcp

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

type ErrorCode string

const (
	ErrCodeInvalidRequest   ErrorCode = "invalid_request"
	ErrCodeInvalidParameter ErrorCode = "invalid_parameter"
	ErrCodeNotFound         ErrorCode = "not_found"
	ErrCodeConflict         ErrorCode = "conflict"
	ErrCodeRateLimited      ErrorCode = "rate_limited"
	ErrCodeWhatsAppAPI      ErrorCode = "whatsapp_api"
	ErrCodeInternal         ErrorCode = "internal_error"
	ErrCodeUnauthorized     ErrorCode = "unauthorized"
	ErrCodeUnavailable      ErrorCode = "unavailable"
)

type ErrorCategory string

const (
	ErrorCategoryClient    ErrorCategory = "client_error" // Client did something wrong (can retry after fixing)
	ErrorCategoryServer    ErrorCategory = "server_error" // Server-side problem (retry may help)
	ErrorCategoryRateLimit ErrorCategory = "rate_limit"   // Rate limit hit (retry after wait)
	ErrorCategoryAuth      ErrorCategory = "auth_error"   // Authentication/authorization failure
)

type ErrorDetail struct {
	Code       ErrorCode     `json:"code"`
	Category   ErrorCategory `json:"category"`
	Message    string        `json:"message"`
	Detail     string        `json:"detail,omitempty"`      // Human-readable explanation
	Retryable  bool          `json:"retryable"`             // Whether retrying may succeed
	RetryAfter int           `json:"retry_after,omitempty"` // Seconds to wait before retrying (0 = no specific time)
}

type MCPError struct {
	Detail ErrorDetail
}

func (e *MCPError) Error() string {
	return e.Detail.Message
}

func NewMPCError(code ErrorCode, category ErrorCategory, message, detail string, retryable bool, retryAfter int) *MCPError {
	return &MCPError{
		Detail: ErrorDetail{
			Code:       code,
			Category:   category,
			Message:    message,
			Detail:     detail,
			Retryable:  retryable,
			RetryAfter: retryAfter,
		},
	}
}

func (e *MCPError) ErrorResult() *mcp.CallToolResult {
	return mcp.NewToolResultError(fmt.Sprintf("{\"code\":\"%s\",\"category\":\"%s\",\"message\":\"%s\",\"detail\":\"%s\",\"retryable\":%v,\"retry_after\":%d}",
		e.Detail.Code, e.Detail.Category, e.Detail.Message, e.Detail.Detail, e.Detail.Retryable, e.Detail.RetryAfter))
}

func ErrInvalidRequest(message, detail string) *MCPError {
	return NewMPCError(ErrCodeInvalidRequest, ErrorCategoryClient, message, detail, false, 0)
}

func ErrInvalidParameter(field, expected, got string) *MCPError {
	return NewMPCError(ErrCodeInvalidParameter, ErrorCategoryClient,
		fmt.Sprintf("invalid value for %s", field),
		fmt.Sprintf("expected %s, got %s", expected, got),
		false, 0)
}

func ErrNotFound(resourceType, resourceID string) *MCPError {
	return NewMPCError(ErrCodeNotFound, ErrorCategoryClient,
		fmt.Sprintf("%s not found: %s", resourceType, resourceID),
		fmt.Sprintf("the requested %s '%s' does not exist", resourceType, resourceID),
		false, 0)
}

func ErrConflict(message string) *MCPError {
	return NewMPCError(ErrCodeConflict, ErrorCategoryClient, message,
		"the resource already exists or is in a conflicting state", false, 0)
}

func ErrRateLimited(retryAfter int, detail string) *MCPError {
	return NewMPCError(ErrCodeRateLimited, ErrorCategoryRateLimit,
		"rate limit exceeded",
		detail,
		true, retryAfter)
}

func ErrWhatsAppAPI(message, detail string, retryable bool) *MCPError {
	return NewMPCError(ErrCodeWhatsAppAPI, ErrorCategoryServer, message, detail, retryable, 0)
}

func ErrInternal(message string) *MCPError {
	return NewMPCError(ErrCodeInternal, ErrorCategoryServer, message,
		"an internal server error occurred", false, 0)
}

func ErrUnauthorized(message string) *MCPError {
	return NewMPCError(ErrCodeUnauthorized, ErrorCategoryAuth, message,
		"authentication or authorization required", false, 0)
}

func ErrUnavailable(message string, retryAfter int) *MCPError {
	return NewMPCError(ErrCodeUnavailable, ErrorCategoryServer, message,
		"service temporarily unavailable", true, retryAfter)
}

func errorResult(err error) *mcp.CallToolResult {
	if mcpErr, ok := err.(*MCPError); ok {
		return mcpErr.ErrorResult()
	}
	return mcp.NewToolResultError(err.Error())
}

func wrapError(err error) *MCPError {
	if mcpErr, ok := err.(*MCPError); ok {
		return mcpErr
	}
	msg := err.Error()
	switch {
	case containsAny(msg, "rate limit", "rate_limit", "throttl"):
		return ErrRateLimited(0, msg)
	case containsAny(msg, "not found", "not exists", "does not exist"):
		return ErrNotFound("resource", msg)
	case containsAny(msg, "unauthorized", "authentication", "forbidden", "401", "403"):
		return ErrUnauthorized(msg)
	case containsAny(msg, "timeout", "connection refused", "network", "unreachable"):
		return ErrInternal(msg)
	case containsAny(msg, "banned", "restricted", "blocked"):
		return ErrWhatsAppAPI(msg, "WhatsApp account may be restricted or banned", true)
	default:
		return ErrInternal(msg)
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if containsStr(s, sub) {
			return true
		}
	}
	return false
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
