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

	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)
}
