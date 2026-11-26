package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// requestIDMiddleware wraps the handler with a unique request ID for each
// request. The ID is generated from crypto/rand and returned in the
// X-Request-ID response header so callers can correlate logs across the
// daemon and upstream services.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rid [16]byte
		if _, err := rand.Read(rid[:]); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		requestID := fmt.Sprintf("%x", rid[:])
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		next.ServeHTTP(w, r)
	})
}

// requestIDKey is the context key for the request ID. Unexported so only this
// package can access it.
type requestIDKey struct{}

// maxBodySizeMiddleware limits request body size to prevent abuse via large
// POST payloads. The default limit is 1MB which is sufficient for all
// legitimate MCP tool calls and webhook payloads.
func maxBodySizeMiddleware(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// pairDriver is the minimal surface the Server needs from the underlying
// WhatsApp client. Production wiring (cmd/whatsapp-mcp/production_driver.go)
// satisfies this via *client.Client. Tests substitute a fake.
type pairDriver interface {
	IsLoggedIn() bool
	// StartPairing begins an unpaired session. onQR is invoked every time
	// whatsmeow emits a new pairing code. onSuccess is invoked once
	// pairing succeeds.
	StartPairing(ctx context.Context, onQR func(code string), onSuccess func()) error
	// Connect attaches event handlers (including LoggedOut) on an already-
	// paired session. onLoggedOut fires when the remote device revokes the
	// session.
	Connect(ctx context.Context, onLoggedOut func()) error
	Logout(ctx context.Context) error
	Disconnect()
	// RequestHistorySync asks WhatsApp to backfill messages for a chat.
	RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time, anchor client.AnchorMode, count int, useLID client.LIDMode) (string, error)
	// ListChatJIDs returns all cached chat JIDs (used for full history sync opt-in).
	ListChatJIDs() ([]string, error)
	// GetPendingScheduledMessages returns all pending scheduled messages whose
	// scheduled_at is <= now.
	GetPendingScheduledMessages(ctx context.Context) ([]store.ScheduledMessage, error)
	// NextScheduledMessageTime returns the earliest pending scheduled time.
	NextScheduledMessageTime() (time.Time, bool, error)
	// ClaimScheduledMessage atomically moves a row to 'sending'.
	ClaimScheduledMessage(id string) (bool, error)
	// RequeueScheduledMessage returns a failed send to the pending queue for
	// a later attempt.
	RequeueScheduledMessage(id string, attempts int, nextAttempt time.Time) error
	// MarkMessageSent marks a scheduled message as sent.
	MarkMessageSent(id, messageID string, sentAt time.Time) error
	// MarkMessageFailed marks a scheduled message as failed.
	MarkMessageFailed(id, reason string) error
	// SendMessage sends a WhatsApp message.
	SendMessage(ctx context.Context, chatJID string, content string) (*client.SendResult, error)
	// SendScheduledMessage sends a queued message, which may carry media.
	SendScheduledMessage(ctx context.Context, chatJID, content, mediaPath string) (*client.SendResult, error)
	// Download fetches one cached message's media to the media root.
	Download(ctx context.Context, messageID, chatJID string) client.DownloadResult
	// IsConnected reports whether the WhatsApp socket is live. Pairing state
	// alone cannot tell an operator that sends will fail.
	IsConnected() bool
}

// Config configures a daemon Server. Addr may be "host:port" or "host:0"
// (the latter only for tests). MCPMount, if non-nil, is called once the
// mux is being built so the caller can attach additional handlers (in
// practice, the /mcp Streamable HTTP handler from internal/mcp).
//
// AuthToken, if non-empty, gates /mcp and /pair/* behind the HTTP header
// `Authorization: Bearer <token>`. Loopback-bound operation passes an
// empty token and leaves these routes unauthenticated.
type Config struct {
	Addr            string
	Driver          pairDriver
	MCPMount        func(mux *http.ServeMux)
	AuthToken       string
	FullHistorySync bool   // after pairing, request full history sync for all cached chats
	WebhookSecret   []byte // HMAC secret for verifying /webhook/ signatures
	Logger          *log.Logger
}

// listenerState is the explicit lifecycle state of the HTTP listener.
type listenerState int

const (
	listenerStateUnbound listenerState = iota
	listenerStateBound
	listenerStateShutdown
)

// Server is the long-lived daemon process. Safe for a single Run call.
type Server struct {
	cfg   Config
	cache *PairCache
	log   *log.Logger
	// handlers is set in Run and exposed so the client can report metrics.
	handlers *pairHandlers

	// WebSocket broadcast hub for real-time message push notifications.
	wsMu         sync.Mutex
	wsSubs       map[chan wsEvent]struct{}
	wsRegister   chan chan wsEvent
	wsUnregister chan chan wsEvent
	wsBroadcast  chan wsEvent

	mu            sync.Mutex
	httpServer    *http.Server
	boundAddr     string
	listenerState listenerState
	listenerOK    chan struct{} // closed once the listener is bound (tests use this)
}

// wsEvent is a real-time event pushed to WebSocket subscribers.
type wsEvent struct {
	Type string `json:"type"` // "message", "paired", "unpaired", "error"
	Data string `json:"data"`
}

// New constructs a Server but does not start any goroutines yet.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("daemon.New: Addr required")
	}
	if cfg.Driver == nil {
		return nil, errors.New("daemon.New: Driver required")
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "Daemon ", log.LstdFlags)
	}
	return &Server{
		cfg:          cfg,
		cache:        NewPairCache(),
		log:          cfg.Logger,
		wsSubs:       make(map[chan wsEvent]struct{}),
		wsRegister:   make(chan chan wsEvent),
		wsUnregister: make(chan chan wsEvent),
		wsBroadcast:  make(chan wsEvent, 256),
		listenerOK:   make(chan struct{}),
	}, nil
}

// Cache exposes the pair cache so the MCP server can answer pairing_status
// (and for tests). The daemon owns the cache, so callers must construct the
// daemon before wiring the cache into the MCP server.
func (s *Server) Cache() *PairCache { return s.cache }

// SetMCPMount installs the MCP HTTP mount after construction. The daemon owns
// the pair cache, so serve.go builds the daemon first, hands its cache to the
// MCP server, then mounts the MCP handler here. cfg.MCPMount is read in Run,
// so setting it any time before Run takes effect.
func (s *Server) SetMCPMount(fn func(mux *http.ServeMux)) { s.cfg.MCPMount = fn }

// BoundAddr returns the address the HTTP listener actually bound to, useful
// when Addr was "host:0". Only valid after <-s.listenerOK fires.
func (s *Server) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundAddr
}

// MetricSink implementation — delegates to the internal pairHandlers.
// These are no-ops if handlers haven't been created yet (e.g. before Run).
func (s *Server) IncMessagesIn() {
	if s.handlers != nil {
		s.handlers.incMessagesIn()
	}
}
func (s *Server) IncMessagesOut() {
	if s.handlers != nil {
		s.handlers.incMessagesOut()
	}
}
func (s *Server) IncSends() {
	if s.handlers != nil {
		s.handlers.incSends()
	}
}
func (s *Server) IncErrors() {
	if s.handlers != nil {
		s.handlers.incErrors()
	}
}
func (s *Server) IncRateLimited() {
	if s.handlers != nil {
		s.handlers.incRateLimited()
	}
}

// PublishWS publishes a real-time event to all WebSocket subscribers. Safe to
// call from any goroutine; non-blocking if the broadcast channel is full.
func (s *Server) PublishWS(evType, data string) {
	select {
	case s.wsBroadcast <- wsEvent{Type: evType, Data: data}:
	default:
		// Drop if the broadcast channel is full — better than blocking.
	}
}

// wshub is the WebSocket broadcast hub. It registers/unregisters subscribers
// and fan-outs broadcast events to all connected clients. Runs in its own
// goroutine started from Run().
func (s *Server) wshub(ctx context.Context) {
	for {
		select {
		case ch := <-s.wsRegister:
			s.wsMu.Lock()
			s.wsSubs[ch] = struct{}{}
			s.wsMu.Unlock()
		case ch := <-s.wsUnregister:
			s.wsMu.Lock()
			if _, ok := s.wsSubs[ch]; ok {
				delete(s.wsSubs, ch)
				close(ch)
			}
			s.wsMu.Unlock()
		case ev := <-s.wsBroadcast:
			s.wsMu.Lock()
			for ch := range s.wsSubs {
				select {
				case ch <- ev:
				default:
					// Slow subscriber: drop rather than block.
				}
			}
			s.wsMu.Unlock()
		case <-ctx.Done():
			s.wsMu.Lock()
			for ch := range s.wsSubs {
				close(ch)
			}
			s.wsSubs = make(map[chan wsEvent]struct{})
			s.wsMu.Unlock()
			return
		}
	}
}

// handleWSEvents upgrades the HTTP connection to a WebSocket and forwards
// broadcast events to the client until the connection closes.
func (s *Server) handleWSEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		http.Error(w, "websocket upgrade failed", http.StatusInternalServerError)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "connection closed")

	ctx := r.Context()
	ch := make(chan wsEvent, 16)

	s.wsRegister <- ch

	defer func() {
		s.wsUnregister <- ch
	}()

	for {
		select {
		case ev := <-ch:
			data, err := json.Marshal(ev)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// Run starts the HTTP listener, begins the pairing state machine, and
// blocks until ctx is cancelled. On cancel, performs the shutdown
// sequence: drain HTTP → Disconnect driver.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	var coder pairCoder
	if pc, ok := s.cfg.Driver.(pairCoder); ok {
		coder = pc
	}
	handlers := newPairHandlers(s.cache, s.cfg.Driver, coder, s.PublishWS, s.cfg.WebhookSecret, s.cfg.AuthToken)
	s.handlers = handlers
	handlers.mount(mux)

	// WebSocket broadcast hub for real-time push notifications.
	go s.wshub(ctx)

	mux.HandleFunc("/ws/events", s.handleWSEvents)

	if s.cfg.MCPMount != nil {
		s.cfg.MCPMount(mux)
	} else {
		// No MCP attached (tests). Provide a 503 so the shape is observable.
		mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "mcp not mounted", http.StatusServiceUnavailable)
		})
	}

	rootHandler := requestIDMiddleware(maxBodySizeMiddleware(authMiddleware(mux, s.cfg.AuthToken), 1<<20))

	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.mu.Lock()
	s.httpServer = &http.Server{
		Handler:           rootHandler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.boundAddr = ln.Addr().String()
	s.listenerState = listenerStateBound
	httpSrv := s.httpServer
	s.mu.Unlock()

	listenErrCh := make(chan error, 1)
	go func() { listenErrCh <- httpSrv.Serve(ln) }()

	close(s.listenerOK)

	// Drive the pairing state machine.
	if err := s.bootDriver(ctx); err != nil {
		_ = httpSrv.Shutdown(context.Background())
		return fmt.Errorf("boot driver: %w", err)
	}

	// Start the scheduled message scheduler goroutine.
	schedulerDone := make(chan struct{})
	go s.runScheduler(ctx, schedulerDone)

	// Wait for cancellation or a listener failure.
	select {
	case <-ctx.Done():
	case err := <-listenErrCh:
		if err != nil && err != http.ErrServerClosed {
			s.cfg.Driver.Disconnect()
			return fmt.Errorf("http listen: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	s.mu.Lock()
	s.listenerState = listenerStateShutdown
	s.mu.Unlock()
	// A scheduled send can be mid-ack when shutdown starts. Let it finish so
	// the row is marked sent rather than left 'sending' for the next boot to
	// reclaim and re-send as a duplicate.
	select {
	case <-schedulerDone:
	case <-time.After(schedulerDrainTimeout):
		s.log.Printf("[WARN] scheduled messages: drain timed out after %v; an in-flight send may be re-sent on next boot", schedulerDrainTimeout)
	}
	s.cfg.Driver.Disconnect()
	return nil
}

// bootDriver inspects the driver's IsLoggedIn and either starts pairing
// or connects with the existing session. Safe to call only once per Run.
func (s *Server) bootDriver(ctx context.Context) error {
	onQR := func(code string) { s.cache.SetQR(code) }
	onPairSuccess := func() {
		s.cache.SetPaired()
		// Transition into Connect to install the LoggedOut handler.
		_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
		// If full history sync is requested, backfill every cached chat.
		if s.cfg.FullHistorySync {
			go s.requestFullHistorySync(ctx)
		}
	}

	if s.cfg.Driver.IsLoggedIn() {
		s.cache.SetPaired()
		if s.cfg.FullHistorySync {
			go s.requestFullHistorySync(ctx)
		}
		return s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
	}
	return s.cfg.Driver.StartPairing(ctx, onQR, onPairSuccess)
}

// requestFullHistorySync asks WhatsApp to backfill older messages for every
// cached chat. It is fire-and-forget: each RequestHistorySync call delivers
// results asynchronously through the event handler. Runs in its own goroutine
// so bootDriver doesn't block.
func (s *Server) requestFullHistorySync(ctx context.Context) {
	jids, err := s.cfg.Driver.ListChatJIDs()
	if err != nil {
		s.cache.SetError("full history sync: failed to list chats: " + err.Error())
		return
	}
	for _, jid := range jids {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := s.cfg.Driver.RequestHistorySync(ctx, jid, time.Time{}, "oldest", 50, true); err != nil {
			s.log.Printf("[WARN] full history sync: failed for %s: %v", jid, err)
		}
	}
	s.log.Printf("[INFO] full history sync: requested backfill for %d chats", len(jids))
}

// runScheduler runs in a goroutine and sends scheduled messages at their
// scheduled time. It sleeps on a timer until the next due message; when the
// queue is empty it re-checks after a short idle interval. Sends are
// timer-driven (no per-message polling); the idle re-check only detects newly
// queued messages whose due time is earlier than the current sleep.
const (
	maxScheduleAttempts   = 5
	scheduleRetryBackoff  = 30 * time.Second
	scheduledSendTimeout  = 75 * time.Second
	schedulerDrainTimeout = 90 * time.Second
)

// unknownOutcome reports whether a failed send leaves the delivery state
// unknowable: the context ended (shutdown, deadline) rather than WhatsApp
// answering. whatsmeow writes the frame before it waits for the ack, so the
// message may well have been delivered.
func unknownOutcome(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "context canceled") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "client is closed")
}

// retryableSendFailure reports whether a failed scheduled send is worth
// retrying. These are transport conditions — a busy socket, an in-flight
// offline-sync replay, a media-connection refresh — rather than a verdict on
// the message itself.
func retryableSendFailure(msg string) bool {
	const markers = "timed out;timeout;i/o timeout;connection reset;connection refused;broken pipe;unexpected eof;EOF;stream:error;stream error;temporarily unavailable;service unavailable;HTTP 5;internal server error;rate limited;too many requests;try again"
	for _, m := range strings.Split(markers, ";") {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(m)) {
			return true
		}
	}
	return false
}

func (s *Server) runScheduler(ctx context.Context, done chan struct{}) {
	defer close(done)
	const idleInterval = 30 * time.Second
	const duePerPass = 3
	const sendInterval = 10 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgs, err := s.cfg.Driver.GetPendingScheduledMessages(ctx)
		if err != nil {
			s.log.Printf("[WARN] scheduled messages: failed to query: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(idleInterval):
			}
			continue
		}

		sentThisPass := 0
		for _, m := range msgs {
			if sentThisPass >= duePerPass {
				break
			}
			select {
			case <-ctx.Done():
				return
			default:
			}

			if d := time.Until(m.ScheduledAt); d > 0 {
				timer := time.NewTimer(d)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}

			claimed, err := s.cfg.Driver.ClaimScheduledMessage(m.ID)
			if err != nil {
				s.log.Printf("[WARN] scheduled message %s: claim failed: %v", m.ID, err)
				continue
			}
			if !claimed {
				// Another worker or a prior run already handled it.
				continue
			}

			if sentThisPass > 0 {
				// Pace the backlog: past-due messages must not go out
				// back-to-back, which reads as a burst to WhatsApp.
				select {
				case <-ctx.Done():
					return
				case <-time.After(sendInterval):
				}
			}
			// Detached on purpose: the run context is cancelled at shutdown,
			// and cancelling after the frame is on the wire turns a
			// delivered message into a recorded failure. The drain in Run
			// bounds how long this can outlive the process.
			sendCtx, sendCancel := context.WithTimeout(context.Background(), scheduledSendTimeout)
			r, err := s.cfg.Driver.SendScheduledMessage(sendCtx, m.ChatJID, m.Content, m.MediaPath)
			sendCancel()
			sentThisPass++
			failure := ""
			switch {
			case err != nil:
				failure = err.Error()
			case !r.Success:
				failure = r.Message
			}
			if unknownOutcome(failure) {
				// Leave the row 'sending': the next pass (or the next boot)
				// reclaims it, and re-sending a message that may already have
				// been delivered is preferable to recording a false failure.
				s.log.Printf("[INFO] scheduled message %s (chat %s): outcome unknown (%s); left for reclaim", m.ID, m.ChatJID, failure)
				continue
			}
			if failure != "" {
				// A transient transport failure is not a verdict on the
				// message: the socket can be mid offline-sync replay, or
				// WhatsApp can be briefly slow. Requeue with a backoff so
				// the send is retried instead of dropped.
				if retryableSendFailure(failure) && m.Attempts < maxScheduleAttempts {
					attempts := m.Attempts + 1
					backoff := time.Duration(attempts) * scheduleRetryBackoff
					if err := s.cfg.Driver.RequeueScheduledMessage(m.ID, attempts, time.Now().Add(backoff)); err != nil {
						// Leave the row 'sending': the next pass, or the next
						// boot, reclaims it. Writing a terminal state here would
						// turn a retryable send into a lost message.
						s.log.Printf("[WARN] scheduled message %s (chat %s): requeue failed: %v; left for reclaim", m.ID, m.ChatJID, err)
					} else {
						s.log.Printf("[INFO] scheduled message %s (chat %s): deferred, attempt %d/%d: %s", m.ID, m.ChatJID, attempts, maxScheduleAttempts, failure)
					}
					continue
				}
				if markErr := s.cfg.Driver.MarkMessageFailed(m.ID, failure); markErr != nil {
					s.log.Printf("[WARN] scheduled message %s: mark failed: %v", m.ID, markErr)
				}
				s.log.Printf("[WARN] scheduled message %s (chat %s) failed after %d attempt(s): %s", m.ID, m.ChatJID, m.Attempts+1, failure)
				continue
			}
			if markErr := s.cfg.Driver.MarkMessageSent(m.ID, r.ID, time.Now()); markErr != nil {
				s.log.Printf("[WARN] scheduled message %s: mark sent: %v", m.ID, markErr)
			}
			s.log.Printf("[INFO] scheduled message %s sent to %s", m.ID, m.ChatJID)
		}

		// Sleep until the next pending message is due. When nothing is
		// pending, re-check after the idle interval so newly queued messages
		// are picked up without a per-message poll.
		next, ok, err := s.cfg.Driver.NextScheduledMessageTime()
		if err != nil {
			s.log.Printf("[WARN] scheduled messages: failed to read next: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(idleInterval):
			}
			continue
		}

		d := idleInterval
		if ok {
			d = time.Until(next)
			if d < 0 {
				d = 0
			}
			if d > idleInterval {
				d = idleInterval
			}
		}

		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// onLoggedOut returns a callback the driver invokes when the remote device
// revokes the session. Flips the cache to unpaired and kicks the pairing
// flow back up in a separate goroutine so whatsmeow's event loop isn't blocked.
func (s *Server) onLoggedOut(ctx context.Context) func() {
	return func() {
		s.cache.Reset()
		onQR := func(code string) { s.cache.SetQR(code) }
		onPairSuccess := func() {
			s.cache.SetPaired()
			_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
		}
		go func() {
			if err := s.cfg.Driver.StartPairing(ctx, onQR, onPairSuccess); err != nil {
				s.cache.SetError("pairing: " + err.Error())
				if s.log != nil {
					s.log.Printf("[ERROR] re-pairing after logout failed: %v", err)
				}
			}
		}()
	}
}

func authMiddleware(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requiresAuth(r.URL.Path) {
			got := r.Header.Get("Authorization")
			// The pairing page cannot send an Authorization header: a browser
			// attaches no such header to an <img>, an EventSource, or a plain
			// form POST. Accept the same token as a query parameter on those
			// routes so remote pairing works, and let the page inject it.
			if got == "" && isBrowserSubresource(r.URL.Path) {
				got = "Bearer " + r.URL.Query().Get("access_token")
			}
			if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="whatsapp-mcp"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isBrowserSubresource names the pairing routes a page loads as a sub-resource,
// where a header is not an option.
func isBrowserSubresource(path string) bool {
	return path == "/pair/qr.png" || path == "/pair/events" || path == "/pair/code"
}

func requiresAuth(path string) bool {
	if path == "/mcp" || strings.HasPrefix(path, "/mcp/") {
		return true
	}
	if path == "/pair" || strings.HasPrefix(path, "/pair/") {
		return true
	}
	if path == "/ws/events" {
		return true
	}
	if strings.HasPrefix(path, "/webhook/") {
		return true
	}
	return false
}
