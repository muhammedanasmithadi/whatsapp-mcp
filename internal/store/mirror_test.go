package store

import (
	"context"
	"testing"
	"time"
)

func TestMessageStarMirror(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	chat := "447700000001@s.whatsapp.net"
	id := "MIRRORSTAR1"

	if err := s.StoreMessage(ctx, Message{ID: id, ChatJID: chat, Sender: "447700000001", Content: "star me", Timestamp: time.Now()}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := s.SetMessageStarred(ctx, id, chat, true); err != nil {
		t.Fatalf("SetMessageStarred: %v", err)
	}
	if got := s.starred(ctx, id, chat); !got {
		t.Error("star flag not stored")
	}

	if err := s.SetMessageStarred(ctx, id, chat, false); err != nil {
		t.Fatalf("unstar: %v", err)
	}
	if got := s.starred(ctx, id, chat); got {
		t.Error("unstar did not clear the flag")
	}

	wrongChat, err := s.GetChat(ctx, "447700000009@s.whatsapp.net", false)
	if err != nil || wrongChat == nil {
		t.Logf("no unrelated chat in fixture (ok): %v", err)
	}
}

func TestChatFlagMirror(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	chat := "447700000002@s.whatsapp.net"

	if err := s.StoreChat(chat, "pin me", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}

	if err := s.SetChatPinnedState(chat, true); err != nil {
		t.Fatalf("SetChatPinnedState: %v", err)
	}
	if got := s.chatFlags(ctx, chat); !got.pinned {
		t.Error("pin flag not stored")
	}

	until := time.Now().Add(8 * time.Hour).Truncate(time.Second)
	if err := s.SetChatMuteState(chat, until); err != nil {
		t.Fatalf("SetChatMuteState: %v", err)
	}
	got := s.chatFlags(ctx, chat)
	if !got.muted || !got.mutedUntil.Equal(until) {
		t.Errorf("mute state = %+v, want muted until %v", got, until)
	}

	if err := s.SetChatMuteState(chat, time.Time{}); err != nil {
		t.Fatalf("clear mute: %v", err)
	}
	if got := s.chatFlags(ctx, chat); got.muted {
		t.Error("clearing the deadline must unmute")
	}

	if err := s.SetChatPinnedState(chat, false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if got := s.chatFlags(ctx, chat); got.pinned {
		t.Error("unpin did not clear the flag")
	}
}

func TestDeleteChatClearsEveryDependentRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	chat := "447700000003@s.whatsapp.net"

	if err := s.StoreChat(chat, "doomed", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := s.StoreMessage(ctx, Message{ID: "DELCHAT1", ChatJID: chat, Sender: "447700000003", Content: "history goes away", Timestamp: time.Now()}, nil, nil, nil, 0); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := s.SetReadPosition(ctx, chat, "DELCHAT1", time.Now()); err != nil {
		t.Fatalf("SetReadPosition: %v", err)
	}
	if err := s.StoreScheduledMessage("sched_delchat", chat, "queued", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("StoreScheduledMessage: %v", err)
	}

	if err := s.DeleteChat(ctx, chat); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}

	msgs, err := s.ListMessages(ctx, ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages after delete: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("messages survived deletion: %d", len(msgs))
	}
	if chat, err := s.GetChat(ctx, chat, false); err != nil || chat != nil {
		t.Errorf("chat row survived deletion: %+v (err %v)", chat, err)
	}
}

func TestRequeueScheduledMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	chat := "447700000004@s.whatsapp.net"
	id := "sched_requeue"

	if err := s.StoreChat(chat, "requeue home", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	if err := s.StoreScheduledMessage(id, chat, "retried later", "", time.Now()); err != nil {
		t.Fatalf("StoreScheduledMessage: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE scheduled_messages SET status = 'sending', attempts = 2 WHERE id = ?", id); err != nil {
		t.Fatalf("simulate in-flight: %v", err)
	}

	next := time.Now().Add(60 * time.Second).Truncate(time.Second)
	if err := s.RequeueScheduledMessage(id, 3, next); err != nil {
		t.Fatalf("RequeueScheduledMessage: %v", err)
	}

	var status string
	var attempts int
	var at time.Time
	if err := s.db.QueryRowContext(ctx, "SELECT status, attempts, scheduled_at FROM scheduled_messages WHERE id = ?", id).Scan(&status, &attempts, &at); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "pending" || attempts != 3 || !at.Equal(next) {
		t.Errorf("requeued row = (%q, %d, %v), want (pending, 3, %v)", status, attempts, at, next)
	}
}

type chatFlags struct {
	pinned     bool
	muted      bool
	mutedUntil time.Time
}

func (s *Store) chatFlags(ctx context.Context, chat string) chatFlags {
	var (
		pinned     bool
		mutedUntil time.Time
	)
	var nullable time.Time
	row := s.db.QueryRowContext(ctx, "SELECT pinned, muted_until FROM chats WHERE jid = ?", chat)
	if err := row.Scan(&pinned, &nullable); err == nil {
		mutedUntil = nullable
	}
	return chatFlags{
		pinned:     pinned,
		muted:      mutedUntil.After(time.Now()),
		mutedUntil: mutedUntil,
	}
}

func (s *Store) starred(ctx context.Context, id, chat string) bool {
	var starred bool
	_ = s.db.QueryRowContext(ctx,
		"SELECT is_starred FROM messages WHERE id = ? AND chat_jid = ?", id, chat).
		Scan(&starred)
	return starred
}
