package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// StatusBroadcastJID is the fixed WhatsApp JID that carries Status (Stories)
// updates. Status messages are ordinary messages whose chat JID is this
// broadcast address and whose sender is the contact who posted them.
const StatusBroadcastJID = "status@broadcast"

// StatusTTL is how long a WhatsApp Status update stays visible. WhatsApp
// deletes statuses after 24 hours; we keep the cached row but mark it expired.
const StatusTTL = 24 * time.Hour

// StatusUpdate is one cached Status (Stories) item posted by a contact.
type StatusUpdate struct {
	MessageID   string    `json:"message_id"`
	SenderJID   string    `json:"sender_jid"`
	SenderName  string    `json:"sender_name,omitempty"`
	SenderPhone string    `json:"sender_phone,omitempty"`
	Content     string    `json:"content,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	IsFromMe    bool      `json:"is_from_me"`
	MediaType   string    `json:"media_type,omitempty"`
	Filename    string    `json:"filename,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	Expired     bool      `json:"expired"`
}

// PresenceEntry is the last known presence state for a contact.
type PresenceEntry struct {
	JID          string    `json:"jid"`
	Online       bool      `json:"online"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
	LastSeenSeen bool      `json:"last_seen_known"`
	SubscribedAt time.Time `json:"subscribed_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// ListStatusUpdates returns cached Status (Stories) messages since the given
// time, newest first. Sender display names are resolved from the local
// contacts table first, then the cached chat list.
func (s *Store) ListStatusUpdates(ctx context.Context, since time.Time, limit int) ([]StatusUpdate, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sender, content, timestamp, is_from_me, media_type, filename
		FROM messages
		WHERE chat_jid = ? AND timestamp >= ?
		ORDER BY timestamp DESC
		LIMIT ?`, StatusBroadcastJID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	var updates []StatusUpdate
	for rows.Next() {
		var (
			u         StatusUpdate
			sender    string
			content   sql.NullString
			mediaType sql.NullString
			filename  sql.NullString
		)
		if err := rows.Scan(&u.MessageID, &sender, &content, &u.Timestamp, &u.IsFromMe, &mediaType, &filename); err != nil {
			return nil, err
		}
		u.SenderPhone = sender
		u.SenderJID = sender
		if !strings.Contains(sender, "@") {
			u.SenderJID = sender + "@s.whatsapp.net"
		}
		u.Content = content.String
		u.MediaType = mediaType.String
		u.Filename = filename.String
		u.ExpiresAt = u.Timestamp.Add(StatusTTL)
		u.Expired = now.After(u.ExpiresAt)
		updates = append(updates, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The store uses a single SQLite connection (MaxOpenConns(1)), so the
	// outer result set must be fully drained and closed before issuing name
	// lookups. Resolving names inside the loop deadlocks on that one
	// connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	names := make(map[string]string)
	for i := range updates {
		if name, ok := names[updates[i].SenderJID]; ok {
			updates[i].SenderName = name
			continue
		}
		name := s.lookupDisplayName(updates[i].SenderJID)
		names[updates[i].SenderJID] = name
		updates[i].SenderName = name
	}
	return updates, nil
}

// StatusMessageIDs returns the message IDs of cached status updates posted by
// senderJID at or after since. Used to validate mark-viewed targets so a tool
// caller cannot send read receipts for arbitrary chats.
func (s *Store) StatusMessageIDs(ctx context.Context, senderJID string, since time.Time) (map[string]struct{}, error) {
	sender := strings.TrimSuffix(senderJID, "@s.whatsapp.net")
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM messages
		WHERE chat_jid = ? AND sender = ? AND timestamp >= ?`,
		StatusBroadcastJID, sender, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

// lookupDisplayName returns the best local name for a JID. User-created
// contact entries win, then the account's address book (stored by whatsmeow in
// whatsapp.db), then chat history names. A bare phone number saved as a chat
// name is ignored so callers see a real name when one exists.
func (s *Store) lookupDisplayName(jid string) string {
	var name string
	if err := s.db.QueryRow("SELECT COALESCE(name, '') FROM contacts WHERE jid = ?", jid).Scan(&name); err == nil && name != "" {
		return name
	}
	if s.whatsmeowDB != nil {
		err := s.whatsmeowDB.QueryRow(`
			SELECT COALESCE(NULLIF(full_name, ''), NULLIF(first_name, ''), NULLIF(push_name, ''), NULLIF(business_name, ''), '')
			FROM whatsmeow_contacts WHERE their_jid = ? LIMIT 1`, jid).Scan(&name)
		if err == nil && name != "" {
			return name
		}
	}
	if err := s.db.QueryRow("SELECT COALESCE(name, '') FROM chats WHERE jid = ?", jid).Scan(&name); err == nil && name != "" && !isDigitsOnly(name) {
		return name
	}
	return ""
}

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// UpsertPresence records the latest presence event for a JID. A zero lastSeen
// leaves any previously known last-seen timestamp intact.
func (s *Store) UpsertPresence(ctx context.Context, jid string, online bool, lastSeen time.Time) error {
	var lastSeenArg any
	if !lastSeen.IsZero() {
		lastSeenArg = lastSeen
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO presence (jid, online, last_seen, subscribed_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(jid) DO UPDATE SET
			online = excluded.online,
			last_seen = COALESCE(excluded.last_seen, presence.last_seen),
			updated_at = CURRENT_TIMESTAMP`,
		jid, online, lastSeenArg)
	return err
}

// MarkPresenceSubscribed records that this client asked WhatsApp for presence
// updates about a JID, without changing any previously observed state.
func (s *Store) MarkPresenceSubscribed(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO presence (jid, online, subscribed_at, updated_at)
		VALUES (?, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(jid) DO UPDATE SET
			subscribed_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP`,
		jid)
	return err
}

// GetPresence returns the cached presence entry for a JID. sql.ErrNoRows means
// no event or subscription has ever been observed.
func (s *Store) GetPresence(ctx context.Context, jid string) (*PresenceEntry, error) {
	var (
		p        PresenceEntry
		lastSeen sql.NullTime
		subAt    sql.NullTime
		updated  sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT jid, online, last_seen, subscribed_at, updated_at
		FROM presence WHERE jid = ?`, jid).
		Scan(&p.JID, &p.Online, &lastSeen, &subAt, &updated)
	if err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		p.LastSeen = lastSeen.Time
		p.LastSeenSeen = true
	}
	if subAt.Valid {
		p.SubscribedAt = subAt.Time
	}
	if updated.Valid {
		p.UpdatedAt = updated.Time
	}
	return &p, nil
}

// ListPresence returns every cached presence entry, most recently updated
// first.
func (s *Store) ListPresence(ctx context.Context) ([]PresenceEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT jid, online, last_seen, subscribed_at, updated_at
		FROM presence ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PresenceEntry
	for rows.Next() {
		var (
			p        PresenceEntry
			lastSeen sql.NullTime
			subAt    sql.NullTime
			updated  sql.NullTime
		)
		if err := rows.Scan(&p.JID, &p.Online, &lastSeen, &subAt, &updated); err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			p.LastSeen = lastSeen.Time
			p.LastSeenSeen = true
		}
		if subAt.Valid {
			p.SubscribedAt = subAt.Time
		}
		if updated.Valid {
			p.UpdatedAt = updated.Time
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePresence removes a presence entry. Mainly useful for tests and for
// clearing a contact the user no longer wants tracked.
func (s *Store) DeletePresence(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM presence WHERE jid = ?", jid)
	return err
}

// ErrNoPresence is returned by helpers that want to distinguish "never seen"
// from a database failure.
var ErrNoPresence = errors.New("no presence data")
