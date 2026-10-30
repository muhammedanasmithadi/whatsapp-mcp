package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"path/filepath"
	"syscall"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/daemon"
	mcpsrv "github.com/sealjay/mcp-whatsapp/internal/mcp"
	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func runServe(storeDir string, redactor *security.Redactor, args []string) int {
	var (
		addr           string
		allowRemote    bool
		readOnly       bool
		logFormat      string
		fullHistorySync bool
	)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&addr, "addr", "", "HTTP bind address (default: 127.0.0.1:8765, env WHATSAPP_MCP_ADDR)")
	fs.BoolVar(&allowRemote, "allow-remote", false, "allow binding to a non-loopback address")
	fs.BoolVar(&readOnly, "read-only", false, "expose only read/query tools; disable send, edit, delete, mark-read, group/privacy mutations (env WHATSAPP_MCP_READ_ONLY)")
	fs.StringVar(&logFormat, "log-format", "text", "log output format: text (default) or json")
	fs.BoolVar(&fullHistorySync, "full-history-sync", false, "after pairing, request full history sync for all cached chats (env WHATSAPP_MCP_FULL_HISTORY_SYNC)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if !readOnly {
		if v := os.Getenv("WHATSAPP_MCP_READ_ONLY"); v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
			readOnly = true
		}
	}
	if !fullHistorySync {
		if v := os.Getenv("WHATSAPP_MCP_FULL_HISTORY_SYNC"); v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
			fullHistorySync = true
		}
	}

	addr = resolveAddr(addr)
	if !allowRemote && !isLoopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "refusing to bind to non-loopback address %q; pass -allow-remote if you mean it\n", addr)
		return 2
	}

	// When binding to a non-loopback address we require a shared bearer
	// token. Loopback-only operation intentionally keeps no token so local
	// editors and curl can hit the daemon without extra setup.
	var authToken string
	if allowRemote {
		authToken = os.Getenv("WHATSAPP_MCP_TOKEN")
		if authToken == "" {
			fmt.Fprintln(os.Stderr, "-allow-remote requires WHATSAPP_MCP_TOKEN to be set in the environment")
			return 2
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	lock, err := store.TryLock(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer lock.Release()

	absStore, err := filepath.Abs(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve store dir: %v\n", err)
		return 1
	}
	allowedMediaRoot := os.Getenv("WHATSAPP_MCP_MEDIA_ROOT")
	if allowedMediaRoot == "" {
		allowedMediaRoot = filepath.Join(absStore, "uploads")
	}
	allowedMediaRoot = filepath.Clean(allowedMediaRoot)
	if mkErr := os.MkdirAll(allowedMediaRoot, 0o755); mkErr != nil {
		fmt.Fprintf(os.Stderr, "warn: could not create media root %q: %v\n", allowedMediaRoot, mkErr)
	}
	// Operator UX: make the media root obvious on boot so users know where
	// to drop outbound attachments.
	fmt.Fprintf(os.Stderr, "media root: %s (drop outbound files here)\n", allowedMediaRoot)

	st, err := store.Open(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	deviceName := os.Getenv("WHATSAPP_MCP_DEVICE_NAME")
	if deviceName == "" {
		deviceName = "WhatsApp MCP"
	}

	if logFormat == "" {
		logFormat = os.Getenv("WHATSAPP_MCP_LOG_FORMAT")
	}
	if logFormat == "" {
		logFormat = "text"
	}

	var logger waLog.Logger
	if logFormat == "json" {
		logger = client.NewJSONLogger("Client", "INFO")
	} else {
		logger = client.NewStderrLogger("Client", "INFO", false)
	}

	c, err := client.New(ctx, client.Config{
		StoreDir:         storeDir,
		Store:            st,
		Logger:           logger,
		AllowedMediaRoot: allowedMediaRoot,
		Redactor:         redactor,
		DeviceName:       deviceName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}

	drv := newProductionDriver(c)

	// The daemon owns the pair cache, so build it first, then hand the cache
	// to the MCP server (so pairing_status can surface the live QR), then
	// mount the MCP HTTP handler back onto the daemon.
	d, err := daemon.New(daemon.Config{
		Addr:            addr,
		Driver:          drv,
		AuthToken:       authToken,
		FullHistorySync: fullHistorySync,
		Logger:          log.New(os.Stderr, "Daemon ", log.LstdFlags),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon.New: %v\n", err)
		return 1
	}

	// Wire the daemon as the client's metrics sink so send/receive/error
	// counters are exposed at /metrics.
	c.SetMetricSink(d)

	// Wire real-time message events to WebSocket subscribers.
	c.SetOnIncomingMessage(func(data string) {
		d.PublishWS("message", data)
	})

	mcpOpts := []mcpsrv.Option{}
	if readOnly {
		mcpOpts = append(mcpOpts, mcpsrv.WithReadOnly(true))
		fmt.Fprintln(os.Stderr, "read-only mode: send/edit/delete/mark/group/privacy tools disabled")
	}
	mcpServer := mcpsrv.NewServer(c, d.Cache(), mcpOpts...)
	d.SetMCPMount(mcpServer.AttachHTTP)

	fmt.Fprintf(os.Stderr, "whatsapp-mcp listening on http://%s (MCP at /mcp, pairing at /pair)\n", addr)
	if !c.IsLoggedIn() {
		fmt.Fprintf(os.Stderr, "unpaired: open http://%s/pair to scan QR\n", addr)
	}
	if err := d.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		return 1
	}
	return 0
}

// resolveAddr applies the -addr / WHATSAPP_MCP_ADDR / default precedence.
func resolveAddr(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("WHATSAPP_MCP_ADDR"); env != "" {
		return env
	}
	return "127.0.0.1:8765"
}

// isLoopbackAddr reports whether addr's host resolves to a loopback IP.
// Hostnames (including "localhost") are resolved via DNS; all returned
// addresses must be loopback for the check to pass. Non-loopback binds
// require -allow-remote.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	// If it parses directly as an IP, check without DNS.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// Resolve hostname and require every address to be loopback.
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}
