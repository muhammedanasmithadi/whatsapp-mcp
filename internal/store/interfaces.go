package store

import (
	"context"
	"time"
)

// ContactStore is the local address-book surface used by MCP contact tools.
// Linked devices cannot write the phone's address book, so these rows stay in
// the bridge database and only influence name resolution inside this bridge.
type ContactStore interface {
	ListContacts() ([]Contact, error)
	GetContact(jid string) (*Contact, error)
	StoreContact(jid, name, phoneNumber string) error
	DeleteContact(jid string) error
}

// SchedulingStore is the scheduled-message persistence surface. It is kept
// separate from the scheduler's wake/sleep mechanics (which live in the
// daemon) so the store never needs to know about the scheduler.
type SchedulingStore interface {
	// EnsureChat creates a chat row if the JID is not cached yet, preserving
	// any existing row.
	EnsureChat(jid, name string) error
	StoreScheduledMessage(id, chatJID, content string, scheduledAt time.Time) error
	ListScheduledMessages(status string) ([]ScheduledMessage, error)
	CancelScheduledMessage(id string) (bool, error)
	ClaimScheduledMessage(id string) (bool, error)
	NextScheduledMessageTime() (time.Time, bool, error)
}

// StatusStore is the cached Status (Stories) surface used by MCP status tools.
type StatusStore interface {
	ListStatusUpdates(ctx context.Context, since time.Time, limit int) ([]StatusUpdate, error)
	StatusMessageIDs(ctx context.Context, senderJID string, since time.Time) (map[string]struct{}, error)
}

// PresenceStore is the cached online/last-seen surface used by MCP presence
// tools.
type PresenceStore interface {
	UpsertPresence(ctx context.Context, jid string, online bool, lastSeen time.Time) error
	MarkPresenceSubscribed(ctx context.Context, jid string) error
	GetPresence(ctx context.Context, jid string) (*PresenceEntry, error)
	ListPresence(ctx context.Context) ([]PresenceEntry, error)
	DeletePresence(ctx context.Context, jid string) error
}

// AuditStore is the append-only audit-log surface.
type AuditStore interface {
	AuditLog(toolName, chatJID, messageID, action, result, details string) error
	ListAuditLog(limit int) ([]AuditLogEntry, error)
}

// MessageReader is the read surface for cached messages used by MCP query
// tools and the scheduler.
type MessageReader interface {
	GetMessage(ctx context.Context, id, chatJID string) (*Message, error)
	RecentIncomingMessages(ctx context.Context, chatJID string, limit int) (ids []string, senders []string, err error)
	FindChatName(jid string) string
}

// AutoResponderStore is the persistence surface for the out-of-office / auto
// reply configuration. A single row holds the enabled flag and the reply
// message text.
type AutoResponderStore interface {
	GetAutoResponder(ctx context.Context) (enabled bool, message string, err error)
	SetAutoResponder(ctx context.Context, enabled bool, message string) error
	CanAutoRespond(ctx context.Context, jid string) (AutoRespondDecision, error)
	RecordAutoRespond(ctx context.Context, jid string) error
}

// Store satisfies ContactStore, SchedulingStore, StatusStore, PresenceStore,
// AuditStore, MessageReader, and AutoResponderStore.

var _ ContactStore = (*Store)(nil)
var _ SchedulingStore = (*Store)(nil)
var _ StatusStore = (*Store)(nil)
var _ PresenceStore = (*Store)(nil)
var _ AuditStore = (*Store)(nil)
var _ MessageReader = (*Store)(nil)
var _ AutoResponderStore = (*Store)(nil)
