package ratelimit

import (
	"testing"
	"time"
)

func TestRefundSendRestoresBudget(t *testing.T) {
	l := New(Config{
		MinSendInterval:       0,
		MaxSendsPerHour:       2,
		NonContactMinInterval: 0,
		NonContactMaxPerHour:  0,
	})

	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("first send denied: %s", d.Reason)
	}
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("second send denied: %s", d.Reason)
	}
	if d := l.AllowSend(true); d.Allowed {
		t.Fatal("third send should have hit the hourly cap")
	}

	l.RefundSend(true)

	d := l.AllowSend(true)
	if !d.Allowed {
		t.Fatalf("send after refund denied: %s (budget was not restored)", d.Reason)
	}
}

func TestRefundSendIsIdempotentAndSafe(t *testing.T) {
	l := New(Config{MinSendInterval: 0})
	l.RefundSend(true)
	l.RefundSend(false)
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("send after refunding an empty window denied: %s", d.Reason)
	}
	var nilLimiter *Limiter
	nilLimiter.RefundSend(true)
}

func TestRefundSendClearsBothBudgetAndPacing(t *testing.T) {
	l := New(Config{MinSendInterval: time.Minute})
	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("first send denied: %s", d.Reason)
	}
	if d := l.AllowSend(true); d.Allowed {
		t.Fatal("the interval floor should deny the immediate second send")
	}

	l.RefundSend(true)

	if d := l.AllowSend(true); !d.Allowed {
		t.Fatalf("send after refund denied: %s (pacing was not restored)", d.Reason)
	}
	if d := l.AllowSend(true); d.Allowed {
		t.Fatal("a real send must still pace the next one")
	}
}
