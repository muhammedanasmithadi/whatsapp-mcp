// Package mcp wires mark3labs/mcp-go to the internal/client and internal/store
// packages, exposing the WhatsApp bridge over MCP Streamable HTTP.
package mcp

import (
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// pairingCache is the minimal pairing-state surface pairing_status needs.
// *daemon.PairCache satisfies it. The smoke command and tests pass nil, in
// which case pairing_status reports an "error" envelope.
type pairingCache interface {
	Paired() bool
	QR() string
}

// Server holds the MCP server, its bound WhatsApp client, store interfaces
// for data access (Dependency Inversion), and an optional pairing cache (used
// only by pairing_status).
type Server struct {
	client   *client.Client
	mcp      *server.MCPServer
	cache    pairingCache
	readOnly bool
	// toolAllowlist, if non-empty, restricts registration to only these tools.
	// Used with WHATSAPP_MCP_ENABLED_TOOLS for granular access control beyond
	// read-only mode.
	toolAllowlist map[string]struct{}

	// Store interfaces — all satisfied by *store.Store. Keeping them as
	// interfaces decouples MCP tools from the concrete store type for data
	// access operations.
	contacts   store.ContactStore
	scheduling store.SchedulingStore
	status     store.StatusStore
	presence   store.PresenceStore
	audit      store.AuditStore
	messages   store.MessageReader
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithReadOnly makes the server expose only read/query tools: no send,
// edit, delete, mark-read, group mutation, blocklist mutation, privacy or
// status changes. Use this when granting an AI agent access to an account
// where outbound actions must be impossible even under prompt injection.
func WithReadOnly(ro bool) Option { return func(s *Server) { s.readOnly = ro } }

// WithAllowedTools restricts the server to only register the given tool names.
// If empty, all tools are registered (subject to read-only mode). Use this
// for granular access control beyond read-only mode.
func WithAllowedTools(names []string) Option {
	return func(s *Server) {
		s.toolAllowlist = make(map[string]struct{}, len(names))
		for _, n := range names {
			s.toolAllowlist[n] = struct{}{}
		}
	}
}

// NewServer constructs an MCP server with all tools registered against the
// provided WhatsApp client and store. cache may be nil (smoke/tests);
// pairing_status is the only tool that consults it. Pass options (e.g.
// WithReadOnly) to tune the tool surface.
//
// Version is reported as serverInfo.version. cmd/whatsapp-mcp sets it from its
// -ldflags build version so the two cannot drift.
var Version = "dev"

func NewServer(c *client.Client, st *store.Store, cache pairingCache, opts ...Option) *Server {
	mcpSrv := server.NewMCPServer(
		"whatsapp",
		Version,
		server.WithToolCapabilities(true),
	)
	s := &Server{
		client:     c,
		mcp:        mcpSrv,
		cache:      cache,
		contacts:   st,
		scheduling: st,
		status:     st,
		presence:   st,
		audit:      st,
		messages:   st,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.registerTools()
	s.registerResources()
	return s
}

// ReadOnly reports whether the server was constructed in read-only mode.
func (s *Server) ReadOnly() bool { return s.readOnly }

// auditLog records an agent-initiated action in the append-only audit log.
func (s *Server) auditLog(toolName, chatJID, messageID, action, result, details string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.AuditLog(toolName, chatJID, messageID, action, result, details)
}

// addTool registers a tool only if it passes the allowlist filter.
func (s *Server) addTool(tool mcp.Tool, handler server.ToolHandlerFunc) {
	if s.shouldRegister(tool.Name) {
		s.mcp.AddTool(tool, handler)
	}
}

// shouldRegister reports whether a tool with the given name should be
// registered. If no allowlist is configured, all tools are registered.
func (s *Server) shouldRegister(toolName string) bool {
	if len(s.toolAllowlist) == 0 {
		return true
	}
	_, ok := s.toolAllowlist[toolName]
	return ok
}

// MCP returns the underlying mcp-go server (tests only).
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// AttachHTTP mounts the MCP Streamable HTTP handler on mux at /mcp. The
// actual listener lifecycle is owned by the caller (internal/daemon).
func (s *Server) AttachHTTP(mux *http.ServeMux) {
	// Stateless: nothing here is session-scoped (the pairing cache is process-wide
	// device state), so legacy clients get no Mcp-Session-Id either.
	httpHandler := server.NewStreamableHTTPServer(s.mcp, server.WithStateLess(true))
	mux.Handle("/mcp", httpHandler)
}

// Store returns the underlying message store.
func (s *Server) Store() *store.Store {
	if st, ok := s.contacts.(*store.Store); ok {
		return st
	}
	return nil
}
