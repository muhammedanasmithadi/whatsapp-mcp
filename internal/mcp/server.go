package mcp

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

type pairingCache interface {
	Paired() bool
	QR() string
}

type ToolSurface int

const (
	ToolSurfaceFull ToolSurface = iota
	ToolSurfaceReadOnly
	ToolSurfaceAllowlisted
)

type Server struct {
	client        *client.Client
	mcp           *server.MCPServer
	cache         pairingCache
	readOnly      bool
	toolSurface   ToolSurface
	toolAllowlist map[string]struct{}

	contacts   store.ContactStore
	scheduling store.SchedulingStore
	status     store.StatusStore
	presence   store.PresenceStore
	audit      store.AuditStore
	messages   store.MessageReader

	syncMu   sync.Mutex
	lastSync time.Time
}

type Option func(*Server)

func WithReadOnly(ro bool) Option { return func(s *Server) { s.readOnly = ro } }

func WithAllowedTools(names []string) Option {
	return func(s *Server) {
		s.toolAllowlist = make(map[string]struct{}, len(names))
		for _, n := range names {
			s.toolAllowlist[n] = struct{}{}
		}
	}
}

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
	if s.readOnly {
		s.toolSurface = ToolSurfaceReadOnly
	} else if len(s.toolAllowlist) > 0 {
		s.toolSurface = ToolSurfaceAllowlisted
	} else {
		s.toolSurface = ToolSurfaceFull
	}
	s.registerTools()
	s.registerResources()
	return s
}

func (s *Server) auditLog(toolName, chatJID, messageID, action, result, details string) {
	if s.audit == nil {
		return
	}
	if err := s.audit.AuditLog(toolName, chatJID, messageID, action, result, details); err != nil {
		log.Printf("[ERROR] audit log write failed for %s/%s: %v", toolName, action, err)
	}
}

func (s *Server) addTool(tool mcp.Tool, handler server.ToolHandlerFunc) {
	if s.shouldRegister(tool.Name) {
		s.mcp.AddTool(tool, handler)
	}
}

func (s *Server) shouldRegister(toolName string) bool {
	if len(s.toolAllowlist) == 0 {
		return true
	}
	_, ok := s.toolAllowlist[toolName]
	return ok
}

func (s *Server) MCP() *server.MCPServer { return s.mcp }

func (s *Server) AttachHTTP(mux *http.ServeMux) {
	httpHandler := server.NewStreamableHTTPServer(s.mcp, server.WithStateLess(true))
	mux.Handle("/mcp", httpHandler)
	mux.Handle("/mcp/", httpHandler)
}

func (s *Server) Store() *store.Store {
	if st, ok := s.contacts.(*store.Store); ok {
		return st
	}
	return nil
}
