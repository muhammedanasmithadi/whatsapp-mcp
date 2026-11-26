package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var (
	pairTmpl     = template.Must(template.ParseFS(templateFS, "templates/pair.html.tmpl"))
	pairedTmpl   = template.Must(template.ParseFS(templateFS, "templates/pair_success.html.tmpl"))
	pairCodeTmpl = template.Must(template.ParseFS(templateFS, "templates/pair_code.html.tmpl"))
)

const (
	qrPNGSize = 256
)

type pairPageData struct {
	CSRFToken string
	AuthToken string // bearer token for sub-resource requests, empty when unauthenticated
	State     string // current pairing state
}

type resetter interface {
	Logout(ctx context.Context) error
	Download(ctx context.Context, messageID, chatJID string) client.DownloadResult
}

type pairCoder interface {
	PairPhone(ctx context.Context, phone string) (string, error)
}

type csrfTokenState int

const (
	csrfTokenNone csrfTokenState = iota
	csrfTokenIssued
	csrfTokenConsumed
)

type csrfToken struct {
	state csrfTokenState
	value string
}

type pairHandlers struct {
	cache *PairCache
	reset resetter
	coder pairCoder

	pairGetLimiter   *Limiter
	pairQRLimiter    *Limiter
	pairResetLimiter *Limiter
	pairCodeLimiter  *Limiter

	startedAt time.Time

	driver pairDriver

	metrics struct {
		messagesIn  int64
		messagesOut int64
		sendsTotal  int64
		errorsTotal int64
		rateLimited int64
	}

	csrfMu    sync.Mutex
	csrf      csrfToken
	publishWS func(evType, data string)

	webhookSecret []byte
	authToken     string

	webhookLimiter *Limiter
}

func newPairHandlers(cache *PairCache, reset resetter, coder pairCoder, publishWS func(evType, data string), webhookSecret []byte, authToken string) *pairHandlers {
	handlers := &pairHandlers{
		cache:            cache,
		reset:            reset,
		coder:            coder,
		pairGetLimiter:   NewLimiter(15.0/60.0, 5),  // 15/min, burst 5
		pairQRLimiter:    NewLimiter(15.0/60.0, 10), // 15/min, burst 10
		pairResetLimiter: NewLimiter(1.0/60.0, 1),   // 1/min, burst 1
		pairCodeLimiter:  NewLimiter(3.0/60.0, 1),   // 3/min, burst 1; WhatsApp limits code requests server-side too
		webhookLimiter:   NewLimiter(10.0, 20),
		startedAt:        time.Now(),
		publishWS:        publishWS,
		webhookSecret:    webhookSecret,
		authToken:        authToken,
	}
	if driver, ok := reset.(pairDriver); ok {
		handlers.driver = driver
	}
	return handlers
}

func (h *pairHandlers) generateCSRFToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	token := hex.EncodeToString(b)
	h.csrfMu.Lock()
	h.csrf = csrfToken{state: csrfTokenIssued, value: token}
	h.csrfMu.Unlock()
	return token
}

func (h *pairHandlers) validCSRFToken(token string) bool {
	h.csrfMu.Lock()
	defer h.csrfMu.Unlock()
	if h.csrf.state != csrfTokenIssued || h.csrf.value == "" || token == "" {
		return false
	}
	ok := h.csrf.value == token
	if ok {
		h.csrf = csrfToken{state: csrfTokenConsumed} // single-use: consume after validation
	}
	return ok
}

func (h *pairHandlers) mount(mux *http.ServeMux) {
	mux.HandleFunc("/pair", h.handlePairPage)
	mux.HandleFunc("/pair/qr.png", h.handlePairQR)
	mux.HandleFunc("/pair/events", h.handlePairEventSource)
	mux.HandleFunc("/pair/reset", h.handlePairReset)
	mux.HandleFunc("/pair/code", h.handlePairCode)
	mux.HandleFunc("/healthz", h.handleHealth)
	mux.HandleFunc("/metrics", h.handleMetrics)
	mux.HandleFunc("/media/download/", h.handleMediaDownload)
	mux.HandleFunc("/webhook/", h.handleWebhook)
}

type mediaDownloadRequest struct {
	messageID string
	chatJID   string
}

func parseMediaDownloadRequest(r *http.Request) (mediaDownloadRequest, bool) {
	path := r.URL.Path
	prefix := "/media/download/"
	if !strings.HasPrefix(path, prefix) {
		return mediaDownloadRequest{}, false
	}
	rest := path[len(prefix):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return mediaDownloadRequest{}, false
	}
	return mediaDownloadRequest{
		messageID: parts[0],
		chatJID:   parts[1],
	}, true
}

func (h *pairHandlers) handleMediaDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	req, ok := parseMediaDownloadRequest(r)
	if !ok {
		http.Error(w, "invalid path; expected /media/download/{message_id}/{chat_jid}", http.StatusBadRequest)
		return
	}
	if h.reset == nil {
		http.Error(w, "media download not available", http.StatusInternalServerError)
		return
	}
	result := h.reset.Download(r.Context(), req.messageID, req.chatJID)
	if !result.Success {
		http.Error(w, result.Message, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		http.Error(w, "failed to read media file", http.StatusInternalServerError)
		return
	}
	contentType := http.DetectContentType(data)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

type healthzResponse struct {
	Connected bool   `json:"connected"`
	Paired    bool   `json:"paired"`
	UptimeSec int64  `json:"uptime_sec"`
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
}

func (h *pairHandlers) connected() bool {
	if h.driver == nil {
		return false
	}
	return h.driver.IsConnected()
}

func (h *pairHandlers) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	snap := h.cache.Snapshot()
	resp := healthzResponse{
		Connected: h.connected(),
		Paired:    h.cache.Paired(),
		UptimeSec: int64(time.Since(h.startedAt).Seconds()),
		State:     string(snap.State),
		Detail:    snap.Detail,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *pairHandlers) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	snap := h.cache.Snapshot()

	var pairingState float64
	switch snap.State {
	case PairStateUnpaired:
		pairingState = 0
	case PairStateAwaitingQR:
		pairingState = 1
	case PairStatePaired:
		pairingState = 2
	case PairStateError:
		pairingState = 3
	}

	mi := atomic.LoadInt64(&h.metrics.messagesIn)
	mo := atomic.LoadInt64(&h.metrics.messagesOut)
	sends := atomic.LoadInt64(&h.metrics.sendsTotal)
	errs := atomic.LoadInt64(&h.metrics.errorsTotal)
	rl := atomic.LoadInt64(&h.metrics.rateLimited)
	uptime := int64(time.Since(h.startedAt).Seconds())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP whatsapp_mcp_pairing_state Pairing state gauge (0=unpaired,1=awaiting_qr,2=paired,3=error)\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_pairing_state gauge\n")
	fmt.Fprintf(w, "whatsapp_mcp_pairing_state %g\n", pairingState)
	fmt.Fprintf(w, "# HELP whatsapp_mcp_messages_in_total Total incoming messages received\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_messages_in_total counter\n")
	fmt.Fprintf(w, "whatsapp_mcp_messages_in_total %d\n", mi)
	fmt.Fprintf(w, "# HELP whatsapp_mcp_messages_out_total Total outgoing messages sent\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_messages_out_total counter\n")
	fmt.Fprintf(w, "whatsapp_mcp_messages_out_total %d\n", mo)
	fmt.Fprintf(w, "# HELP whatsapp_mcp_sends_total Total send tool invocations\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_sends_total counter\n")
	fmt.Fprintf(w, "whatsapp_mcp_sends_total %d\n", sends)
	fmt.Fprintf(w, "# HELP whatsapp_mcp_errors_total Total errors\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_errors_total counter\n")
	fmt.Fprintf(w, "whatsapp_mcp_errors_total %d\n", errs)
	fmt.Fprintf(w, "# HELP whatsapp_mcp_rate_limited_total Total rate-limited requests\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_rate_limited_total counter\n")
	fmt.Fprintf(w, "whatsapp_mcp_rate_limited_total %d\n", rl)
	fmt.Fprintf(w, "# HELP whatsapp_mcp_uptime_seconds Daemon uptime in seconds\n")
	fmt.Fprintf(w, "# TYPE whatsapp_mcp_uptime_seconds counter\n")
	fmt.Fprintf(w, "whatsapp_mcp_uptime_seconds %d\n", uptime)
}

func (h *pairHandlers) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !h.webhookLimiter.Allow() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "webhook rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(h.webhookSecret) > 0 {
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			http.Error(w, "missing X-Hub-Signature-256 header", http.StatusUnauthorized)
			return
		}
		mac := hmac.New(sha256.New, h.webhookSecret)
		mac.Write(body)
		expectedMAC := mac.Sum(nil)
		sigHex := strings.TrimSpace(strings.TrimPrefix(sig, "sha256="))
		gotMAC, err := hex.DecodeString(sigHex)
		if err != nil || !hmac.Equal(gotMAC, expectedMAC) {
			http.Error(w, "invalid X-Hub-Signature-256", http.StatusUnauthorized)
			return
		}
	}

	var payload struct {
		Type      string `json:"type"`
		MessageID string `json:"message_id"`
		ChatJID   string `json:"chat_jid"`
		Sender    string `json:"sender"`
		Content   string `json:"content"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if payload.MessageID == "" || payload.ChatJID == "" {
		http.Error(w, "message_id and chat_jid are required", http.StatusBadRequest)
		return
	}

	data, _ := json.Marshal(map[string]any{
		"type":       payload.Type,
		"message_id": payload.MessageID,
		"chat_jid":   payload.ChatJID,
		"sender":     payload.Sender,
		"content":    payload.Content,
		"timestamp":  payload.Timestamp,
	})
	h.publishWS("webhook_message", string(data))

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","message_id":"%s"}`, payload.MessageID)
}

func (h *pairHandlers) incMessagesIn() {
	atomic.AddInt64(&h.metrics.messagesIn, 1)
}

func (h *pairHandlers) incMessagesOut() {
	atomic.AddInt64(&h.metrics.messagesOut, 1)
}

func (h *pairHandlers) incSends() {
	atomic.AddInt64(&h.metrics.sendsTotal, 1)
}

func (h *pairHandlers) incErrors() {
	atomic.AddInt64(&h.metrics.errorsTotal, 1)
}

func (h *pairHandlers) incRateLimited() {
	atomic.AddInt64(&h.metrics.rateLimited, 1)
}

func (h *pairHandlers) handlePairPage(w http.ResponseWriter, r *http.Request) {
	if !h.pairGetLimiter.Allow() {
		w.Header().Set("Retry-After", "4")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := pairPageData{
		CSRFToken: h.generateCSRFToken(),
		AuthToken: h.authToken,
		State:     string(h.cache.State()),
	}
	if h.cache.Paired() {
		if err := pairedTmpl.Execute(w, data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
		return
	}
	if err := pairTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (h *pairHandlers) handlePairQR(w http.ResponseWriter, r *http.Request) {
	if !h.pairQRLimiter.Allow() {
		w.Header().Set("Retry-After", "4")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	qr := h.cache.QR()
	if qr == "" {
		http.NotFound(w, r)
		return
	}
	png, err := renderQRPNG(qr, qrPNGSize)
	if err != nil {
		http.Error(w, "qr render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (h *pairHandlers) handlePairEventSource(w http.ResponseWriter, r *http.Request) {
	if !h.pairGetLimiter.Allow() {
		w.Header().Set("Retry-After", "4")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx := r.Context()
	sub := h.cache.Subscribe()
	defer h.cache.Unsubscribe(sub)

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case ev := <-sub:
			b, err := json.Marshal(ev)
			if err != nil {
				http.Error(w, "marshal failed", http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

func (h *pairHandlers) handlePairReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !h.pairResetLimiter.Allow() {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	if !h.validCSRFToken(r.FormValue("csrf_token")) {
		http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
		return
	}
	if err := h.reset.Logout(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf("logout failed: %v", err), http.StatusInternalServerError)
		return
	}
	h.cache.Reset()
	http.Redirect(w, r, "/pair", http.StatusSeeOther)
}

type pairCodePageData struct {
	Code    string // formatted code, empty on error
	Phone   string
	Message string // non-empty on failure
}

func normalizePairingPhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) < 7 || len(digits) > 15 || strings.HasPrefix(digits, "0") {
		return ""
	}
	return digits
}

func (h *pairHandlers) handlePairCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !h.pairCodeLimiter.Allow() {
		w.Header().Set("Retry-After", "20")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	if h.cache.Paired() {
		http.Redirect(w, r, "/pair", http.StatusSeeOther)
		return
	}
	if h.coder == nil {
		http.Error(w, "phone-number pairing not supported by this driver", http.StatusNotImplemented)
		return
	}
	if !h.validCSRFToken(r.FormValue("csrf_token")) {
		http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
		return
	}
	phone := normalizePairingPhone(r.FormValue("phone"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := pairCodePageData{Phone: phone}
	if phone == "" {
		data.Phone = ""
		data.Message = "Enter a valid international phone number (country code first, digits only)."
	} else if code, err := h.coder.PairPhone(r.Context(), phone); err != nil {
		data.Message = fmt.Sprintf("Pairing-code request failed: %v", err)
	} else {
		data.Code = client.FormatPairingCode(code)
	}
	if err := pairCodeTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
