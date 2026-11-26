package client

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	wmstore "go.mau.fi/whatsmeow/store"
)

func newUnpairedClient() *Client {
	device := &wmstore.Device{}
	wa := whatsmeow.NewClient(device, NewStderrLogger("test", "ERROR", false))
	return &Client{
		wa:  wa,
		log: NewStderrLogger("test", "ERROR", false),
	}
}

func TestConnect_UnpairedRejectedByDefault(t *testing.T) {
	c := newUnpairedClient()
	err := c.Connect(context.Background(), ConnectOpts{})
	if err == nil {
		t.Fatal("expected error for unpaired device, got nil")
	}
	if !errors.Is(err, errNotPaired) {
		t.Fatalf("expected errNotPaired, got: %v", err)
	}
}

func TestConnect_AllowUnpairedSkipsGuard(t *testing.T) {
	c := newUnpairedClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so ConnectContext returns fast

	err := c.Connect(ctx, ConnectOpts{AllowUnpaired: true})
	if errors.Is(err, errNotPaired) {
		t.Fatal("AllowUnpaired should skip the paired-device guard")
	}
}
