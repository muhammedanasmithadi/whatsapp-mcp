package store

import (
	"context"
	"time"
)

type ContactStore interface {
	ListContacts() ([]Contact, error)
	GetContact(jid string) (*Contact, error)
	StoreContact(jid, name, phoneNumber string) error
	DeleteContact(jid string) error
	UpsertContactProfile(jid, username, nameSource, addressingMode string, stale bool) error
	MarkContactStale(jid string) error
	GetContactProfile(jid string) (*ContactProfile, error)
	ListStaleContacts() ([]string, error)
}

type SchedulingStore interface {
	EnsureChat(jid, name string) error
	StoreScheduledMessage(id, chatJID, content, mediaPath string, scheduledAt time.Time) error
	ListScheduledMessages(status string) ([]ScheduledMessage, error)
	CancelScheduledMessage(id string) (bool, error)
	ClaimScheduledMessage(id string) (bool, error)
	NextScheduledMessageTime() (time.Time, bool, error)
}

type StatusStore interface {
	ListStatusUpdates(ctx context.Context, since time.Time, limit int) ([]StatusUpdate, error)
	StatusMessageIDs(ctx context.Context, senderJID string, since time.Time) (map[string]struct{}, error)
}

type PresenceStore interface {
	UpsertPresence(ctx context.Context, jid string, online PresenceState, lastSeen time.Time) error
	MarkPresenceSubscribed(ctx context.Context, jid string) error
	GetPresence(ctx context.Context, jid string) (*PresenceEntry, error)
	ListPresence(ctx context.Context) ([]PresenceEntry, error)
	DeletePresence(ctx context.Context, jid string) error
}

type AuditStore interface {
	AuditLog(toolName, chatJID, messageID, action, result, details string) error
	ListAuditLog(limit int) ([]AuditLogEntry, error)
}

type MessageReader interface {
	GetMessage(ctx context.Context, id, chatJID string) (*Message, error)
	RecentIncomingMessages(ctx context.Context, chatJID string, limit int) (ids []string, senders []string, err error)
	FindChatName(jid string) string
}

type AutoResponderStore interface {
	SetAutoResponder(ctx context.Context, enabled bool, message string) error
	CanAutoRespond(ctx context.Context, jid string) (AutoRespondDecision, error)
	RecordAutoRespond(ctx context.Context, jid string) error
}

var _ ContactStore = (*Store)(nil)
var _ SchedulingStore = (*Store)(nil)
var _ StatusStore = (*Store)(nil)
var _ PresenceStore = (*Store)(nil)
var _ AuditStore = (*Store)(nil)
var _ MessageReader = (*Store)(nil)
var _ AutoResponderStore = (*Store)(nil)
