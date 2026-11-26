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
	"path/filepath"
	"strings"
	"syscall"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/daemon"
	mcpsrv "github.com/muhammedanasmithadi/whatsapp-mcp/internal/mcp"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/stt"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func runServe(storeDir string, redactor *security.Redactor, args []string) int {
	var (
		addr            string
		allowRemote     bool
		readOnly        bool
		logFormat       string
		fullHistorySync bool
		enabledTools    string
	)
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&addr, "addr", "", "HTTP bind address (default: 127.0.0.1:8765, env WHATSAPP_MCP_ADDR)")
	fs.BoolVar(&allowRemote, "allow-remote", false, "allow binding to a non-loopback address")
	fs.BoolVar(&readOnly, "read-only", false, "expose only read/query tools; disable send, edit, delete, mark-read, group/privacy mutations (env WHATSAPP_MCP_READ_ONLY)")
	fs.StringVar(&logFormat, "log-format", "", "log output format: text (default) or json; also settable with WHATSAPP_MCP_LOG_FORMAT")
	fs.BoolVar(&fullHistorySync, "full-history-sync", false, "after pairing, request full history sync for all cached chats (env WHATSAPP_MCP_FULL_HISTORY_SYNC)")
	fs.StringVar(&enabledTools, "enabled-tools", "", "comma-separated list of tool names to enable; if set, only these tools are registered (env WHATSAPP_MCP_ENABLED_TOOLS)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if !readOnly {
		readOnly = readOnlyFromEnv()
	}
	if !fullHistorySync {
		if v := os.Getenv("WHATSAPP_MCP_FULL_HISTORY_SYNC"); v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
			fullHistorySync = true
		}
	}

	toolNames := parseEnabledTools(enabledTools)

	addr = resolveAddr(addr)
	if !allowRemote && !isLoopbackAddr(addr) {
		fmt.Fprintf(os.Stderr, "refusing to bind to non-loopback address %q; pass -allow-remote if you mean it\n", addr)
		return 2
	}

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

	var sttClient stt.Client
	if ep := os.Getenv("WHATSAPP_MCP_STT_ENDPOINT"); ep != "" {
		sttClient = stt.NewHTTPClient()
		fmt.Fprintf(os.Stderr, "voice-note transcription enabled: %s\n", ep)
	}

	c, err := client.New(ctx, client.Config{
		StoreDir:         storeDir,
		Store:            st,
		Logger:           logger,
		AllowedMediaRoot: allowedMediaRoot,
		Redactor:         redactor,
		DeviceName:       deviceName,
		STTClient:        sttClient,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}

	drv := newProductionDriver(c)

	d, err := daemon.New(daemon.Config{
		Addr:            addr,
		Driver:          drv,
		AuthToken:       authToken,
		FullHistorySync: fullHistorySync,
		WebhookSecret:   []byte(os.Getenv("WHATSAPP_WEBHOOK_SECRET")),
		Logger:          log.New(os.Stderr, "Daemon ", log.LstdFlags),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon.New: %v\n", err)
		return 1
	}

	c.SetMetricSink(d)

	c.SetOnIncomingMessage(func(data string) {
		d.PublishWS("message", data)
	})

	mcpOpts := []mcpsrv.Option{}
	if readOnly {
		mcpOpts = append(mcpOpts, mcpsrv.WithReadOnly(true))
		fmt.Fprintln(os.Stderr, "read-only mode: send/edit/delete/mark/group/privacy tools disabled")
	}
	if len(toolNames) > 0 {
		mcpOpts = append(mcpOpts, mcpsrv.WithAllowedTools(toolNames))
		fmt.Fprintf(os.Stderr, "allow-list mode: only %d tools enabled\n", len(toolNames))
	}
	mcpServer := mcpsrv.NewServer(c, st, d.Cache(), mcpOpts...)
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

func resolveAddr(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("WHATSAPP_MCP_ADDR"); env != "" {
		return env
	}
	return "127.0.0.1:8765"
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
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
