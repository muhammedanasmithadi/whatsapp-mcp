package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
	mcpsrv "github.com/muhammedanasmithadi/whatsapp-mcp/internal/mcp"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/stt"
)

// parseEnabledTools resolves the -enabled-tools flag against its environment
// variable. Flag values win; empty entries are dropped so a trailing comma
// cannot register a tool with an empty name.
func parseEnabledTools(flagValue string) []string {
	v := strings.TrimSpace(flagValue)
	if v == "" {
		v = os.Getenv("WHATSAPP_MCP_ENABLED_TOOLS")
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		if n := strings.TrimSpace(p); n != "" {
			names = append(names, n)
		}
	}
	return names
}

func readOnlyFromEnv() bool {
	v := os.Getenv("WHATSAPP_MCP_READ_ONLY")
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// runStdio serves MCP over stdin/stdout. It is the transport used by MCPB
// desktop extensions, which launch the binary themselves and cannot open the
// HTTP pairing page. Pair once with the `login` subcommand before using it.
//
// Every diagnostic goes to stderr: stdout carries JSON-RPC frames only.
func runStdio(storeDir string, redactor *security.Redactor, args []string) int {
	var (
		readOnly     bool
		enabledTools string
	)
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&readOnly, "read-only", false, "expose only read/query tools (env WHATSAPP_MCP_READ_ONLY)")
	fs.StringVar(&enabledTools, "enabled-tools", "", "comma-separated list of tool names to enable (env WHATSAPP_MCP_ENABLED_TOOLS)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "whatsapp-mcp stdio: serve MCP over stdin/stdout for MCPB and desktop clients.")
		fmt.Fprintln(os.Stderr, "\nUsage: whatsapp-mcp [-store DIR] stdio [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !readOnly {
		readOnly = readOnlyFromEnv()
	}
	toolNames := parseEnabledTools(enabledTools)

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
	if err := os.MkdirAll(allowedMediaRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not create media root %q: %v\n", allowedMediaRoot, err)
	}

	lock, err := store.TryLock(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer lock.Release()

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

	var sttClient stt.Client
	if ep := os.Getenv("WHATSAPP_MCP_STT_ENDPOINT"); ep != "" {
		sttClient = stt.NewHTTPClient()
		fmt.Fprintf(os.Stderr, "voice-note transcription enabled: %s\n", ep)
	}

	c, err := client.New(context.Background(), client.Config{
		StoreDir:         storeDir,
		Store:            st,
		Logger:           client.NewStderrLogger("Client", "WARN", false),
		AllowedMediaRoot: allowedMediaRoot,
		Redactor:         redactor,
		DeviceName:       deviceName,
		STTClient:        sttClient,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}
	defer c.Disconnect()

	if !c.IsLoggedIn() {
		fmt.Fprintf(os.Stderr, "whatsapp-mcp: no paired device in %s\n", absStore)
		fmt.Fprintln(os.Stderr, "run `whatsapp-mcp -store DIR login` and scan the QR once before starting stdio mode.")
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := c.Connect(ctx, client.ConnectOpts{}); err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		return 1
	}

	opts := []mcpsrv.Option{}
	if readOnly {
		opts = append(opts, mcpsrv.WithReadOnly(true))
		fmt.Fprintln(os.Stderr, "read-only mode: send/edit/delete/mark/group/privacy tools disabled")
	}
	if len(toolNames) > 0 {
		opts = append(opts, mcpsrv.WithAllowedTools(toolNames))
		fmt.Fprintf(os.Stderr, "allow-list mode: only %d tools enabled\n", len(toolNames))
	}

	srv := mcpsrv.NewServer(c, st, nil, opts...)
	if err := mcpserver.NewStdioServer(srv.MCP()).Listen(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "stdio: %v\n", err)
		return 1
	}
	return 0
}
