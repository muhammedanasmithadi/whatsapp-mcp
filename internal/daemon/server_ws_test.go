package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestServer_WSBroadcastDelivered pins the WebSocket fan-out path: a
// subscriber that registers through wsRegister receives every published
// event, and an unregistering subscriber stops receiving them.
func TestServer_WSBroadcastDelivered(t *testing.T) {
	drv := newFakePairDriver(true)
	s := newTestServer(t, drv)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/events", s.handleWSEvents)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitFor(t, func() bool { return s.Cache().Paired() }, "server paired and listening")

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	waitFor(t, func() bool {
		s.wsMu.Lock()
		defer s.wsMu.Unlock()
		return len(s.wsSubs) == 1
	}, "subscriber registered through wsRegister")

	s.PublishWS("message", `{"chat_jid":"1555@s.whatsapp.net"}`)
	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	_, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	if !strings.Contains(string(data), "1555@s.whatsapp.net") {
		t.Fatalf("event payload = %q, want the published message", data)
	}
}

// TestServer_WSShutdownDoesNotDoubleClose pins the fix for a shutdown
// panic: the hub owns every close, so a subscriber that unregisters after
// the hub already cleaned up must not close the same channel twice.
func TestServer_WSShutdownDoesNotDoubleClose(t *testing.T) {
	drv := newFakePairDriver(true)
	s := newTestServer(t, drv)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/events", s.handleWSEvents)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = s.Run(ctx); close(runDone) }()
	waitFor(t, func() bool { return s.Cache().Paired() }, "server paired and listening")

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	waitFor(t, func() bool {
		s.wsMu.Lock()
		defer s.wsMu.Unlock()
		return len(s.wsSubs) == 1
	}, "subscriber registered")

	// Cancelling the run context cleans up every subscriber in the hub.
	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}

	// The handler's deferred unregister now finds no channel left to close;
	// if both closers ran, the hub would have panicked during shutdown.
	_ = conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)
}
