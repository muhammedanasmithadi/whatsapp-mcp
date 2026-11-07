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
type PresenceState bool

const (
	PresenceOffline PresenceState = false
	PresenceOnline  PresenceState = true
)

type PresenceEntry struct {
	JID          string        `json:"jid"`
	Online       PresenceState `json:"online"`
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
func (s *Store) UpsertPresence(ctx context.Context, jid string, online PresenceState, lastSeen time.Time) error {
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
		jid, bool(online), lastSeenArg)
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

// GetAutoResponder returns the current auto-responder configuration.
// Returns enabled=false, message="" when never configured.
func (s *Store) GetAutoResponder(ctx context.Context) (enabled bool, message string, err error) {
	var enabledStr sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "enabled").Scan(&enabledStr)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	enabled = enabledStr.String == "true"

	var msg sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "message").Scan(&msg)
	if err == sql.ErrNoRows {
		return enabled, "", nil
	}
	if err != nil {
		return false, "", err
	}
	var startTime, endTime sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "start_time").Scan(&startTime)
	if err == nil && startTime.Valid {
		_ = startTime // stored but not yet enforced at store layer
	}
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "end_time").Scan(&endTime)
	if err == nil && endTime.Valid {
		_ = endTime // stored but not yet enforced at store layer
	}
	var coolDown sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "cooldown_minutes").Scan(&coolDown)
	if err == nil && coolDown.Valid {
		_ = coolDown // stored but not yet enforced at store layer
	}
	return enabled, msg.String, nil
}

// GetAutoResponderWithConfig returns the full auto-responder configuration
// including start time, end time, and cooldown minutes.
func (s *Store) GetAutoResponderWithConfig(ctx context.Context) (enabled bool, message, startTime, endTime string, cooldownMinutes int, err error) {
	var enabledStr sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "enabled").Scan(&enabledStr)
	if err == sql.ErrNoRows {
		return false, "", "", "", 0, nil
	}
	if err != nil {
		return false, "", "", "", 0, err
	}
	enabled = enabledStr.String == "true"

	var msg sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "message").Scan(&msg)
	if err == sql.ErrNoRows {
		return enabled, "", "", "", 0, nil
	}
	if err != nil {
		return false, "", "", "", 0, err
	}
	var st, et sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "start_time").Scan(&st)
	if err == nil && st.Valid {
		startTime = st.String
	}
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "end_time").Scan(&et)
	if err == nil && et.Valid {
		endTime = et.String
	}
	var coolDown sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT value FROM auto_responder WHERE key = ?`, "cooldown_minutes").Scan(&coolDown)
	if err == nil && coolDown.Valid {
		cooldownMinutes = int(coolDown.Int64)
	}
	return enabled, msg.String, startTime, endTime, cooldownMinutes, nil
}

// SetAutoResponder persists the auto-responder configuration.
func (s *Store) SetAutoResponder(ctx context.Context, enabled bool, message string) error {
	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('enabled', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, enabledStr); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('message', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, message)
	return err
}

// SetAutoResponderWithConfig persists the full auto-responder configuration
// including start time, end time, and cooldown minutes.
func (s *Store) SetAutoResponderWithConfig(ctx context.Context, enabled bool, message string, startTime, endTime string, cooldownMinutes int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('enabled', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, enabledStr); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('message', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, message); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('start_time', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, startTime); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('end_time', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, endTime); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auto_responder (key, value, updated_at) VALUES ('cooldown_minutes', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, cooldownMinutes); err != nil {
		return err
	}
	return tx.Commit()
}

// AutoRespondDecision is the explicit state of an auto-responder eligibility
// check.
type AutoRespondDecision string

const (
	AutoRespondAllowed       AutoRespondDecision = "allowed"
	AutoRespondDisabled     AutoRespondDecision = "disabled"
	AutoRespondOutsideWindow AutoRespondDecision = "outside_window"
	AutoRespondInCooldown   AutoRespondDecision = "in_cooldown"
)

// CanAutoRespond checks whether the auto-responder should reply to a message
// from the given JID at the current time. It checks:
//   - auto-responder is enabled
//   - current time is within the configured start/end window (if set)
//   - cooldown period has elapsed since the last reply to this JID (if set)
func (s *Store) CanAutoRespond(ctx context.Context, jid string) (AutoRespondDecision, error) {
	enabled, _, startTime, endTime, cooldownMinutes, err := s.GetAutoResponderWithConfig(ctx)
	if err != nil {
		return AutoRespondDisabled, err
	}
	if !enabled {
		return AutoRespondDisabled, nil
	}
	now := time.Now()
	// Check time window
	if startTime != "" {
		start, err := time.Parse("15:04", startTime)
		if err == nil {
			start = time.Date(now.Year(), now.Month(), now.Day(), start.Hour(), start.Minute(), 0, 0, now.Location())
			if now.Before(start) {
				return AutoRespondOutsideWindow, nil
			}
		}
	}
	if endTime != "" {
		end, err := time.Parse("15:04", endTime)
		if err == nil {
			end = time.Date(now.Year(), now.Month(), now.Day(), end.Hour(), end.Minute(), 0, 0, now.Location())
			if now.After(end) {
				return AutoRespondOutsideWindow, nil
			}
		}
	}
	// Check cooldown
	if cooldownMinutes > 0 {
		var lastReply sql.NullTime
		err = s.db.QueryRowContext(ctx, `SELECT last_reply FROM auto_responder_cooldown WHERE jid = ?`, jid).Scan(&lastReply)
		if err == nil && lastReply.Valid {
			elapsed := now.Sub(lastReply.Time)
			if elapsed < time.Duration(cooldownMinutes)*time.Minute {
				return AutoRespondInCooldown, nil
			}
		}
	}
	return AutoRespondAllowed, nil
}

// RecordAutoRespond records that an auto-response was sent to the given JID
// at the current time, for cooldown tracking.
func (s *Store) RecordAutoRespond(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auto_responder_cooldown (jid, last_reply) VALUES (?, CURRENT_TIMESTAMP)
		ON CONFLICT(jid) DO UPDATE SET last_reply = CURRENT_TIMESTAMP`, jid)
	return err
}

// ErrNoPresence is returned by helpers that want to distinguish "never seen"
// from a database failure.
var ErrNoPresence = errors.New("no presence data")
