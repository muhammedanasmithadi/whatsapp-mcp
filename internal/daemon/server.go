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

type requestIDKey struct{}

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

type pairDriver interface {
	IsLoggedIn() bool
	StartPairing(ctx context.Context, onQR func(code string), onSuccess func()) error
	Connect(ctx context.Context, onLoggedOut func()) error
	Logout(ctx context.Context) error
	Disconnect()
	RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time, anchor client.AnchorMode, count int, useLID client.LIDMode) (string, error)
	ListChatJIDs() ([]string, error)
	GetPendingScheduledMessages(ctx context.Context) ([]store.ScheduledMessage, error)
	NextScheduledMessageTime() (time.Time, bool, error)
	ClaimScheduledMessage(id string) (bool, error)
	RequeueScheduledMessage(id string, attempts int, nextAttempt time.Time) error
	MarkMessageSent(id, messageID string, sentAt time.Time) error
	MarkMessageFailed(id, reason string) error
	SendMessage(ctx context.Context, chatJID string, content string) (*client.SendResult, error)
	SendScheduledMessage(ctx context.Context, chatJID, content, mediaPath string) (*client.SendResult, error)
	Download(ctx context.Context, messageID, chatJID string) client.DownloadResult
	IsConnected() bool
}

type Config struct {
	Addr            string
	Driver          pairDriver
	MCPMount        func(mux *http.ServeMux)
	AuthToken       string
	FullHistorySync bool   // after pairing, request full history sync for all cached chats
	WebhookSecret   []byte // HMAC secret for verifying /webhook/ signatures
	Logger          *log.Logger
}

type Server struct {
	cfg      Config
	cache    *PairCache
	log      *log.Logger
	handlers *pairHandlers

	wsMu         sync.Mutex
	wsSubs       map[chan wsEvent]struct{}
	wsRegister   chan chan wsEvent
	wsUnregister chan chan wsEvent
	wsBroadcast  chan wsEvent

	mu         sync.Mutex
	httpServer *http.Server
	boundAddr  string
	listenerOK chan struct{} // closed once the listener is bound (tests use this)
}

type wsEvent struct {
	Type string `json:"type"` // "message", "paired", "unpaired", "error"
	Data string `json:"data"`
}

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

func (s *Server) Cache() *PairCache { return s.cache }

func (s *Server) SetMCPMount(fn func(mux *http.ServeMux)) { s.cfg.MCPMount = fn }

func (s *Server) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundAddr
}

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

func (s *Server) PublishWS(evType, data string) {
	select {
	case s.wsBroadcast <- wsEvent{Type: evType, Data: data}:
	default:
	}
}

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

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	var coder pairCoder
	if pc, ok := s.cfg.Driver.(pairCoder); ok {
		coder = pc
	}
	handlers := newPairHandlers(s.cache, s.cfg.Driver, coder, s.PublishWS, s.cfg.WebhookSecret, s.cfg.AuthToken)
	s.handlers = handlers
	handlers.mount(mux)

	go s.wshub(ctx)

	mux.HandleFunc("/ws/events", s.handleWSEvents)

	if s.cfg.MCPMount != nil {
		s.cfg.MCPMount(mux)
	} else {
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
	httpSrv := s.httpServer
	s.mu.Unlock()

	listenErrCh := make(chan error, 1)
	go func() { listenErrCh <- httpSrv.Serve(ln) }()

	close(s.listenerOK)

	if err := s.bootDriver(ctx); err != nil {
		_ = httpSrv.Shutdown(context.Background())
		return fmt.Errorf("boot driver: %w", err)
	}

	schedulerDone := make(chan struct{})
	go s.runScheduler(ctx, schedulerDone)

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
	select {
	case <-schedulerDone:
	case <-time.After(schedulerDrainTimeout):
		s.log.Printf("[WARN] scheduled messages: drain timed out after %v; an in-flight send may be re-sent on next boot", schedulerDrainTimeout)
	}
	s.cfg.Driver.Disconnect()
	return nil
}

func (s *Server) bootDriver(ctx context.Context) error {
	onQR := func(code string) { s.cache.SetQR(code) }
	onPairSuccess := func() {
		s.cache.SetPaired()
		_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
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

const (
	maxScheduleAttempts   = 5
	scheduleRetryBackoff  = 30 * time.Second
	scheduledSendTimeout  = 75 * time.Second
	schedulerDrainTimeout = 90 * time.Second
)

func unknownOutcome(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "context canceled") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "client is closed")
}

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
				continue
			}

			if sentThisPass > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(sendInterval):
				}
			}
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
				s.log.Printf("[INFO] scheduled message %s (chat %s): outcome unknown (%s); left for reclaim", m.ID, m.ChatJID, failure)
				continue
			}
			if failure != "" {
				if retryableSendFailure(failure) && m.Attempts < maxScheduleAttempts {
					attempts := m.Attempts + 1
					backoff := time.Duration(attempts) * scheduleRetryBackoff
					if err := s.cfg.Driver.RequeueScheduledMessage(m.ID, attempts, time.Now().Add(backoff)); err != nil {
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
