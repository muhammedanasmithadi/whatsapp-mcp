// Package mcp wires mark3labs/mcp-go to the internal/client and internal/store
// packages, exposing the WhatsApp bridge over MCP Streamable HTTP.
package mcp

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// pairingCache is the minimal pairing-state surface pairing_status needs.
// *daemon.PairCache satisfies it. The smoke command and tests pass nil, in
// which case pairing_status reports an "error" envelope.
type pairingCache interface {
	Paired() bool
	QR() string
}

// Server holds the MCP server, its bound WhatsApp client, and an optional
// pairing cache (used only by pairing_status).
type Server struct {
	client   *client.Client
	mcp      *server.MCPServer
	cache    pairingCache
	readOnly bool
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithReadOnly makes the server expose only read/query tools: no send,
// edit, delete, mark-read, group mutation, blocklist mutation, privacy or
// status changes. Use this when granting an AI agent access to an account
// where outbound actions must be impossible even under prompt injection.
func WithReadOnly(ro bool) Option { return func(s *Server) { s.readOnly = ro } }

// NewServer constructs an MCP server with all tools registered against the
// provided WhatsApp client. cache may be nil (smoke/tests); pairing_status is
// the only tool that consults it. Pass options (e.g. WithReadOnly) to tune
// the tool surface.
// Version is reported as serverInfo.version. cmd/whatsapp-mcp sets it from its
// -ldflags build version so the two cannot drift.
var Version = "dev"

func NewServer(c *client.Client, cache pairingCache, opts ...Option) *Server {
	mcpSrv := server.NewMCPServer(
		"whatsapp",
		Version,
		server.WithToolCapabilities(true),
	)
	s := &Server{client: c, mcp: mcpSrv, cache: cache}
	for _, opt := range opts {
		opt(s)
	}
	s.registerTools()
	s.registerResources()
	return s
}

// ReadOnly reports whether the server was constructed in read-only mode.
func (s *Server) ReadOnly() bool { return s.readOnly }

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
