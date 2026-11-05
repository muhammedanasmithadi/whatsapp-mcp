package mcp

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ErrorCode is a machine-readable error code.
type ErrorCode string

const (
	// ErrCodeInvalidRequest - Malformed request or missing required parameters.
	ErrCodeInvalidRequest ErrorCode = "invalid_request"
	// ErrCodeInvalidParameter - A specific parameter is invalid or out of range.
	ErrCodeInvalidParameter ErrorCode = "invalid_parameter"
	// ErrCodeNotFound - The requested resource (chat, message, contact) was not found.
	ErrCodeNotFound ErrorCode = "not_found"
	// ErrCodeRateLimited - The client has exceeded rate limits.
	ErrCodeRateLimited ErrorCode = "rate_limited"
	// ErrCodeWhatsAppAPI - WhatsApp returned an error (network, auth, banned, etc.).
	ErrCodeWhatsAppAPI ErrorCode = "whatsapp_api"
	// ErrCodeInternal - Unexpected internal server error.
	ErrCodeInternal ErrorCode = "internal_error"
	// ErrCodeUnauthorized - Authentication or authorization failure.
	ErrCodeUnauthorized ErrorCode = "unauthorized"
	// ErrCodeUnavailable - The service is temporarily unavailable.
	ErrCodeUnavailable ErrorCode = "unavailable"
)

// ErrorCategory groups errors into broad categories for client-side handling.
type ErrorCategory string

const (
	ErrorCategoryClient    ErrorCategory = "client_error"   // Client did something wrong (can retry after fixing)
	ErrorCategoryServer    ErrorCategory = "server_error"   // Server-side problem (retry may help)
	ErrorCategoryNetwork   ErrorCategory = "network_error"  // Network/transport problem (retry may help)
	ErrorCategoryRateLimit ErrorCategory = "rate_limit"     // Rate limit hit (retry after wait)
	ErrorCategoryAuth      ErrorCategory = "auth_error"     // Authentication/authorization failure
)

// ErrorDetail is a structured error detail for MCP tool responses.
type ErrorDetail struct {
	Code       ErrorCode   `json:"code"`
	Category   ErrorCategory `json:"category"`
	Message    string      `json:"message"`
	Detail     string      `json:"detail,omitempty"`   // Human-readable explanation
	Retryable  bool        `json:"retryable"`          // Whether retrying may succeed
	RetryAfter int         `json:"retry_after,omitempty"` // Seconds to wait before retrying (0 = no specific time)
	Field      string      `json:"field,omitempty"`    // For invalid_parameter: which field caused the error
	Expected   string      `json:"expected,omitempty"` // For invalid_parameter: what was expected
	Got        string      `json:"got,omitempty"`      // For invalid_parameter: what was actually provided
}

// MCPError is a structured error that can be converted to an MCP tool result.
type MCPError struct {
	Detail ErrorDetail
}

func (e *MCPError) Error() string {
	return e.Detail.Message
}

// NewMCPError creates a new MCPError with the given fields.
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

// ErrorResult returns an MCP tool result containing the structured error as JSON.
func (e *MCPError) ErrorResult() *mcp.CallToolResult {
	return mcp.NewToolResultError(fmt.Sprintf("{\"code\":\"%s\",\"category\":\"%s\",\"message\":\"%s\",\"detail\":\"%s\",\"retryable\":%v,\"retry_after\":%d}",
		e.Detail.Code, e.Detail.Category, e.Detail.Message, e.Detail.Detail, e.Detail.Retryable, e.Detail.RetryAfter))
}

// Helper constructors for common error patterns.

// ErrInvalidRequest creates an invalid_request error.
func ErrInvalidRequest(message, detail string) *MCPError {
	return NewMPCError(ErrCodeInvalidRequest, ErrorCategoryClient, message, detail, false, 0)
}

// ErrInvalidParameter creates an invalid_parameter error.
func ErrInvalidParameter(field, expected, got string) *MCPError {
	return NewMPCError(ErrCodeInvalidParameter, ErrorCategoryClient,
		fmt.Sprintf("invalid value for %s", field),
		fmt.Sprintf("expected %s, got %s", expected, got),
		false, 0)
}

// ErrNotFound creates a not_found error.
func ErrNotFound(resourceType, resourceID string) *MCPError {
	return NewMPCError(ErrCodeNotFound, ErrorCategoryClient,
		fmt.Sprintf("%s not found: %s", resourceType, resourceID),
		fmt.Sprintf("the requested %s '%s' does not exist", resourceType, resourceID),
		false, 0)
}

// ErrRateLimited creates a rate_limited error.
func ErrRateLimited(retryAfter int, detail string) *MCPError {
	return NewMPCError(ErrCodeRateLimited, ErrorCategoryRateLimit,
		"rate limit exceeded",
		detail,
		true, retryAfter)
}

// ErrWhatsAppAPI creates a whatsapp_api error.
func ErrWhatsAppAPI(message, detail string, retryable bool) *MCPError {
	return NewMPCError(ErrCodeWhatsAppAPI, ErrorCategoryServer, message, detail, retryable, 0)
}

// ErrInternal creates an internal_error.
func ErrInternal(message string) *MCPError {
	return NewMPCError(ErrCodeInternal, ErrorCategoryServer, message,
		"an internal server error occurred", false, 0)
}

// ErrUnauthorized creates an unauthorized error.
func ErrUnauthorized(message string) *MCPError {
	return NewMPCError(ErrCodeUnauthorized, ErrorCategoryAuth, message,
		"authentication or authorization required", false, 0)
}

// ErrUnavailable creates an unavailable error.
func ErrUnavailable(message string, retryAfter int) *MCPError {
	return NewMPCError(ErrCodeUnavailable, ErrorCategoryServer, message,
		"service temporarily unavailable", true, retryAfter)
}

// errorResult wraps an MCPError into a CallToolResult.
func errorResult(err error) *mcp.CallToolResult {
	if mcpErr, ok := err.(*MCPError); ok {
		return mcpErr.ErrorResult()
	}
	// Fallback for non-MCP errors
	return mcp.NewToolResultError(err.Error())
}

// wrapError wraps a Go error into an MCPError with best-effort categorization.
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