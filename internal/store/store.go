package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func ChmodBestEffort(path string, mode os.FileMode) error {
	err := os.Chmod(path, mode)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

type Message struct {
	ID          string    `json:"id"`
	ChatJID     string    `json:"chat_jid"`
	Sender      string    `json:"sender"`                 // bare phone/user part
	SenderPhone string    `json:"sender_phone,omitempty"` // full phone if resolvable
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	IsFromMe    bool      `json:"is_from_me"`
	IsGroup     bool      `json:"is_group"`
	MediaType   string    `json:"media_type,omitempty"`
	Filename    string    `json:"filename,omitempty"`
	URL         string    `json:"url,omitempty"`
	// `?ccb=&oh=&oe=&_nc_sid=` auth query) captured verbatim from the
	DirectPath  string `json:"direct_path,omitempty"`
	ChatName    string `json:"chat_name,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	IsStarred   bool   `json:"is_starred"`
}

type Chat struct {
	JID             string    `json:"jid"`
	Name            string    `json:"name"`
	LastMessageTime time.Time `json:"last_message_time"`
	LastMessage     string    `json:"last_message,omitempty"`
	LastSender      string    `json:"last_sender,omitempty"`
	LastIsFromMe    bool      `json:"last_is_from_me"`
	PhoneNumber     string    `json:"phone_number,omitempty"`
	IsGroup         bool      `json:"is_group"`
	Archived        bool      `json:"archived"`
	Pinned          bool      `json:"pinned"`
	Muted           bool      `json:"muted"`
	MutedUntil      time.Time `json:"muted_until,omitempty"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name,omitempty"`
	JID         string `json:"jid"`
	LID         string `json:"lid,omitempty"`
	NameSource  string `json:"name_source,omitempty"`
	Saved       bool   `json:"saved,omitempty"`
	Resolved    bool   `json:"resolved,omitempty"`
	Status      string `json:"status,omitempty"`
	Stale       bool   `json:"stale,omitempty"`
}

type ContactProfile struct {
	JID            string
	Username       string
	NameSource     string
	AddressingMode string
	Stale          bool
	UpdatedAt      string
}

type MessageContext struct {
	Message Message   `json:"message"`
	Before  []Message `json:"before,omitempty"`
	After   []Message `json:"after,omitempty"`
}

type MessageReadPosition struct {
	ChatJID           string    `json:"chat_jid"`
	LastReadMessageID string    `json:"last_read_message_id,omitempty"`
	LastReadAt        time.Time `json:"last_read_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type ScheduledMessage struct {
	ID            string    `json:"id"`
	ChatJID       string    `json:"chat_jid"`
	Content       string    `json:"content"`
	MediaPath     string    `json:"media_path,omitempty"`
	ScheduledAt   time.Time `json:"scheduled_at"`
	Status        string    `json:"status"` // pending, sent, failed
	Attempts      int       `json:"attempts,omitempty"`
	SentAt        time.Time `json:"sent_at,omitempty"`
	SentMessageID string    `json:"sent_message_id,omitempty"`
	Error         string    `json:"error,omitempty"`
}

type Store struct {
	db          *sql.DB
	whatsmeowDB *sql.DB
	dir         string
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := ChmodBestEffort(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod store dir: %w", err)
	}
	msgPath := filepath.Join(dir, "messages.db")
	waPath := filepath.Join(dir, "whatsapp.db")

	db, err := sql.Open("sqlite3", "file:"+msgPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=-64000&_mmap_size=268435456")
	if err != nil {
		return nil, fmt.Errorf("open messages db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := ChmodBestEffort(msgPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod messages.db: %w", err)
	}

	waDB, err := sql.Open("sqlite3", "file:"+waPath+"?mode=ro")
	if err != nil {
		waDB = nil
	}
	if err := ChmodBestEffort(waPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod whatsapp.db: %w", err)
	}

	return &Store{db: db, whatsmeowDB: waDB, dir: dir}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Dir() string { return s.dir }

func (s *Store) Close() error {
	if s.whatsmeowDB != nil {
		_ = s.whatsmeowDB.Close()
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListChatJIDs() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT chat_jid FROM messages")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}

func (s *Store) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET name = excluded.name, last_message_time = excluded.last_message_time`,
		jid, name, lastMessageTime,
	)
	return err
}

func (s *Store) EnsureChat(jid, name string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, time.Time{},
	)
	return err
}

type ArchivedState bool

const (
	ChatActive   ArchivedState = false
	ChatArchived ArchivedState = true
)

func (s *Store) SetArchived(chatJID string, state ArchivedState) error {
	_, err := s.db.Exec("UPDATE chats SET archived = ? WHERE jid = ?", bool(state), chatJID)
	return err
}

func (s *Store) SetChatPinnedState(chatJID string, pinned bool) error {
	_, err := s.db.Exec("UPDATE chats SET pinned = ? WHERE jid = ?", pinned, chatJID)
	return err
}

func (s *Store) SetChatMuteState(chatJID string, mutedUntil time.Time) error {
	_, err := s.db.Exec("UPDATE chats SET muted_until = ? WHERE jid = ?", mutedUntil, chatJID)
	return err
}

func (s *Store) DeleteChat(ctx context.Context, chatJID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"DELETE FROM messages WHERE chat_jid = ?",
		"DELETE FROM poll_votes WHERE poll_chat_jid = ?",
		"DELETE FROM message_read_positions WHERE chat_jid = ?",
		"DELETE FROM label_associations WHERE chat_jid = ?",
		"DELETE FROM scheduled_messages WHERE chat_jid = ?",
		"DELETE FROM chats WHERE jid = ?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, chatJID); err != nil {
			if strings.Contains(err.Error(), "no such table") {
				continue
			}
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return tx.Commit()
}

func (s *Store) ArchiveChat(chatJID string) error {
	return s.SetArchived(chatJID, ChatArchived)
}

func (s *Store) UnarchiveChat(chatJID string) error {
	return s.SetArchived(chatJID, ChatActive)
}

func (s *Store) StoreMessage(ctx context.Context, m Message, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	if m.Content == "" && m.MediaType == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, direct_path, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Content, m.Timestamp, m.IsFromMe,
		m.MediaType, m.Filename, m.URL, m.DirectPath, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)
	return err
}

func (s *Store) SetMessageStarred(ctx context.Context, id, chatJID string, starred bool) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE messages SET is_starred = ? WHERE id = ? AND chat_jid = ?",
		starred, id, chatJID)
	return err
}

func (s *Store) UpdateMessage(ctx context.Context, id, chatJID, content string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE messages SET content = ? WHERE id = ? AND chat_jid = ?",
		content, id, chatJID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RevokeMessage(ctx context.Context, id, chatJID string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE messages SET content = '[deleted]', media_type = '', url = '', direct_path = '', media_key = NULL, file_sha256 = NULL, file_enc_sha256 = NULL, file_length = 0 WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) StoreMediaInfo(id, chatJID, url, directPath string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	_, err := s.db.Exec(
		"UPDATE messages SET url = ?, direct_path = ?, media_key = ?, file_sha256 = ?, file_enc_sha256 = ?, file_length = ? WHERE id = ? AND chat_jid = ?",
		url, directPath, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
	)
	return err
}

// including the signed `?ccb=&oh=&oe=&_nc_sid=` auth query. It may be empty
func (s *Store) GetMediaInfo(id, chatJID string) (mediaType, filename, url, directPath string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64, err error) {
	var dp sql.NullString
	err = s.db.QueryRow(
		"SELECT media_type, filename, url, direct_path, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	).Scan(&mediaType, &filename, &url, &dp, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)
	directPath = dp.String
	return
}

func (s *Store) GetMessage(ctx context.Context, id, chatJID string) (*Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, chat_jid, sender, COALESCE(content, ''), timestamp, is_from_me
		FROM messages WHERE id = ? AND chat_jid = ?`, id, chatJID)
	var m Message
	if err := row.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Content, &m.Timestamp, &m.IsFromMe); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.IsGroup = strings.HasSuffix(m.ChatJID, "@g.us")
	return &m, nil
}

func (s *Store) GetNewestMessage(chatJID string) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ?
		ORDER BY timestamp DESC
		LIMIT 1`, chatJID).Scan(&id, &ts, &isFromMe)
	return
}

// on-demand history-sync cursor by message key, so extending history backwards
func (s *Store) GetOldestMessage(chatJID string) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ?
		ORDER BY timestamp ASC
		LIMIT 1`, chatJID).Scan(&id, &ts, &isFromMe)
	return
}

func (s *Store) GetMessageAtOrBefore(chatJID string, before time.Time) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ? AND timestamp <= ?
		ORDER BY timestamp DESC
		LIMIT 1`, chatJID, before).Scan(&id, &ts, &isFromMe)
	return
}

func (s *Store) HasInboundFrom(chatJID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM messages WHERE chat_jid = ? AND is_from_me = 0 LIMIT 1)",
		chatJID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

func (s *Store) StoreLabel(labelID, name, color string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO labels (label_id, name, color) VALUES (?, ?, ?)",
		labelID, name, color,
	)
	return err
}

type LabelState bool

const (
	LabelUnlabeled LabelState = false
	LabelLabeled   LabelState = true
)

func (s *Store) StoreLabelAssociation(chatJID, labelID string, labeled LabelState) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO label_associations (chat_jid, label_id, labeled) VALUES (?, ?, ?)",
		chatJID, labelID, bool(labeled),
	)
	return err
}

func (s *Store) ListLabels() ([]map[string]string, error) {
	rows, err := s.db.Query("SELECT label_id, name, color FROM labels ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []map[string]string
	for rows.Next() {
		var labelID, name, color string
		if err := rows.Scan(&labelID, &name, &color); err != nil {
			return nil, err
		}
		labels = append(labels, map[string]string{
			"label_id": labelID,
			"name":     name,
			"color":    color,
		})
	}
	return labels, rows.Err()
}

func (s *Store) ListChatsByLabel(labelID string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT chat_jid FROM label_associations WHERE label_id = ? AND labeled = 1",
		labelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}

type AuditLogEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`
	ChatJID   string    `json:"chat_jid,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Action    string    `json:"action"`
	Result    string    `json:"result"`
	Details   string    `json:"details,omitempty"`
}

func (s *Store) AuditLog(toolName, chatJID, messageID, action, result, details string) error {
	_, err := s.db.Exec(
		"INSERT INTO audit_log (tool_name, chat_jid, message_id, action, result, details) VALUES (?, ?, ?, ?, ?, ?)",
		toolName, chatJID, messageID, action, result, details,
	)
	return err
}

func (s *Store) ListAuditLog(limit int) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		"SELECT id, timestamp, tool_name, chat_jid, message_id, action, result, details FROM audit_log ORDER BY id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ToolName, &e.ChatJID, &e.MessageID, &e.Action, &e.Result, &e.Details); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) StoreContact(jid, name, phoneNumber string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO contacts (jid, name, phone_number, updated_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)",
		jid, name, phoneNumber,
	)
	return err
}

func (s *Store) GetContact(jid string) (*Contact, error) {
	row := s.db.QueryRow("SELECT jid, name, phone_number FROM contacts WHERE jid = ?", jid)
	var c Contact
	if err := row.Scan(&c.JID, &c.Name, &c.PhoneNumber); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListContacts() ([]Contact, error) {
	rows, err := s.db.Query("SELECT jid, name, phone_number FROM contacts ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.JID, &c.Name, &c.PhoneNumber); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

func (s *Store) DeleteContact(jid string) error {
	_, err := s.db.Exec("DELETE FROM contacts WHERE jid = ?", jid)
	return err
}

func (s *Store) UpsertContactProfile(jid, username, nameSource, addressingMode string, stale bool) error {
	staleInt := 0
	if stale {
		staleInt = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO contact_profiles (jid, username, name_source, addressing_mode, stale, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (jid) DO UPDATE SET
			username = excluded.username,
			name_source = excluded.name_source,
			addressing_mode = excluded.addressing_mode,
			stale = excluded.stale,
			updated_at = CURRENT_TIMESTAMP`,
		jid, username, nameSource, addressingMode, staleInt)
	return err
}

func (s *Store) MarkContactStale(jid string) error {
	_, err := s.db.Exec(`
		INSERT INTO contact_profiles (jid, stale, updated_at)
		VALUES (?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT (jid) DO UPDATE SET stale = 1, updated_at = CURRENT_TIMESTAMP`,
		jid)
	return err
}

func (s *Store) GetContactProfile(jid string) (*ContactProfile, error) {
	row := s.db.QueryRow(`
		SELECT jid, COALESCE(username,''), COALESCE(name_source,''),
		       COALESCE(addressing_mode,''), stale, COALESCE(updated_at,'')
		FROM contact_profiles WHERE jid = ?`, jid)
	var p ContactProfile
	var staleInt int
	if err := row.Scan(&p.JID, &p.Username, &p.NameSource, &p.AddressingMode, &staleInt, &p.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.Stale = staleInt != 0
	return &p, nil
}

func (s *Store) ListStaleContacts() ([]string, error) {
	rows, err := s.db.Query("SELECT jid FROM contact_profiles WHERE stale = 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			continue
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

func evidenceSourceRank(source string) int {
	switch source {
	case "pairing", "app_state", "usync":
		return 4
	case "message_alt", "history_sync", "group_info":
		return 3
	case "blocklist":
		return 2
	}
	return 1
}

func (s *Store) RecordEvidence(lid, pn, source, scope string, verified bool) error {
	if lid == "" || pn == "" {
		return nil
	}
	v := 0
	if verified {
		v = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO identity_evidence (lid, pn, source, scope, verified)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (lid, pn, source) DO UPDATE SET
			verified = MAX(verified, excluded.verified),
			learned_at = CURRENT_TIMESTAMP`,
		lid, pn, source, scope, v)
	return err
}

// GetBestPNForLID returns the most trustworthy phone JID for a LID, based on
func (s *Store) GetBestPNForLID(lid string) string {
	rows, err := s.db.Query(`
		SELECT pn, source, verified, learned_at
		FROM identity_evidence
		WHERE lid = ?
		ORDER BY verified DESC, learned_at DESC`, lid)
	if err != nil {
		return ""
	}
	defer rows.Close()

	type candidate struct {
		pn       string
		source   string
		verified bool
	}
	var best *candidate
	for rows.Next() {
		var pn, source, learnedAt string
		var verified int
		if err := rows.Scan(&pn, &source, &verified, &learnedAt); err != nil {
			continue
		}
		c := &candidate{pn: pn, source: source, verified: verified != 0}
		if best == nil || evidenceSourceRank(c.source) > evidenceSourceRank(best.source) ||
			(evidenceSourceRank(c.source) == evidenceSourceRank(best.source) && c.verified && !best.verified) {
			best = c
		}
	}
	if best == nil {
		return ""
	}
	return best.pn
}

func (s *Store) ListEvidence(lid string) ([]map[string]any, error) {
	rows, err := s.db.Query(`
		SELECT lid, pn, source, scope, learned_at, verified
		FROM identity_evidence
		WHERE lid = ?
		ORDER BY learned_at DESC`, lid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var lid, pn, source, scope, learnedAt string
		var verified int
		if err := rows.Scan(&lid, &pn, &source, &scope, &learnedAt, &verified); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"lid":        lid,
			"pn":         pn,
			"source":     source,
			"scope":      scope,
			"learned_at": learnedAt,
			"verified":   verified != 0,
		})
	}
	return out, rows.Err()
}

func (s *Store) StoreScheduledMessage(id, chatJID, content, mediaPath string, scheduledAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO scheduled_messages (id, chat_jid, content, media_path, scheduled_at) VALUES (?, ?, ?, ?, ?)",
		id, chatJID, content, mediaPath, scheduledAt,
	)
	return err
}

func (s *Store) NextScheduledMessageTime() (time.Time, bool, error) {
	var next time.Time
	err := s.db.QueryRow(`
		SELECT scheduled_at FROM scheduled_messages
		WHERE status = 'pending'
		ORDER BY scheduled_at ASC LIMIT 1
	`).Scan(&next)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return next, true, nil
}

func (s *Store) GetPendingScheduledMessages(ctx context.Context) ([]ScheduledMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chat_jid, content, media_path, scheduled_at, attempts FROM scheduled_messages
		WHERE status IN ('pending', 'sending') AND scheduled_at <= ?
		ORDER BY scheduled_at ASC
	`, time.Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []ScheduledMessage
	for rows.Next() {
		var (
			m         ScheduledMessage
			mediaPath sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Content, &mediaPath, &m.ScheduledAt, &m.Attempts); err != nil {
			return nil, err
		}
		m.MediaPath = mediaPath.String
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *Store) MarkMessageSent(id, messageID string, sentAt time.Time) error {
	_, err := s.db.Exec(
		"UPDATE scheduled_messages SET status = 'sent', sent_at = ?, sent_message_id = ? WHERE id = ?",
		sentAt, messageID, id,
	)
	return err
}

func (s *Store) MarkMessageFailed(id, reason string) error {
	_, err := s.db.Exec(
		"UPDATE scheduled_messages SET status = 'failed', error = ? WHERE id = ?",
		reason, id,
	)
	return err
}

func (s *Store) ListScheduledMessages(status string) ([]ScheduledMessage, error) {
	query := `SELECT id, chat_jid, content, media_path, scheduled_at, status, attempts,
		sent_at, COALESCE(sent_message_id, ''), COALESCE(error, '')
		FROM scheduled_messages`
	args := []any{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY scheduled_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []ScheduledMessage
	for rows.Next() {
		var (
			m         ScheduledMessage
			mediaPath sql.NullString
			sentAt    sql.NullTime
		)
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Content, &mediaPath, &m.ScheduledAt, &m.Status, &m.Attempts, &sentAt, &m.SentMessageID, &m.Error); err != nil {
			return nil, err
		}
		m.MediaPath = mediaPath.String
		if sentAt.Valid {
			m.SentAt = sentAt.Time
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *Store) RequeueScheduledMessage(id string, attempts int, nextAttempt time.Time) error {
	_, err := s.db.Exec(
		"UPDATE scheduled_messages SET status = 'pending', attempts = ?, scheduled_at = ? WHERE id = ?",
		attempts, nextAttempt, id,
	)
	return err
}

func (s *Store) ClaimScheduledMessage(id string) (bool, error) {
	res, err := s.db.Exec(
		"UPDATE scheduled_messages SET status = 'sending' WHERE id = ? AND status IN ('pending', 'sending')",
		id,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) CancelScheduledMessage(id string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM scheduled_messages WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) FindChatName(jid string) string {
	var name string
	_ = s.db.QueryRow("SELECT name FROM chats WHERE jid = ?", jid).Scan(&name)
	return name
}

func (s *Store) RecentIncomingMessages(ctx context.Context, chatJID string, limit int) (ids []string, senders []string, err error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sender
		FROM messages
		WHERE chat_jid = ? AND is_from_me = 0
		ORDER BY timestamp DESC
		LIMIT ?`, chatJID, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, sender string
		if err := rows.Scan(&id, &sender); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		senders = append(senders, sender)
	}
	return ids, senders, rows.Err()
}

func (s *Store) GetReadPosition(ctx context.Context, chatJID string) (*MessageReadPosition, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT chat_jid, last_read_message_id, last_read_at, updated_at
		FROM message_read_positions
		WHERE chat_jid = ?`, chatJID)
	var p MessageReadPosition
	var lastReadAt, updatedAt sql.NullTime
	if err := row.Scan(&p.ChatJID, &p.LastReadMessageID, &lastReadAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("get read position: %w", err)
	}
	if lastReadAt.Valid {
		p.LastReadAt = lastReadAt.Time
	}
	if updatedAt.Valid {
		p.UpdatedAt = updatedAt.Time
	}
	return &p, nil
}

func (s *Store) SetReadPosition(ctx context.Context, chatJID, messageID string, lastReadAt time.Time) error {
	if lastReadAt.IsZero() {
		err := s.db.QueryRowContext(ctx, `
			SELECT timestamp FROM messages WHERE id = ? AND chat_jid = ?`,
			messageID, chatJID).Scan(&lastReadAt)
		if err != nil {
			return fmt.Errorf("set read position: resolve message timestamp: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_read_positions (chat_jid, last_read_message_id, last_read_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(chat_jid) DO UPDATE SET
			last_read_message_id = excluded.last_read_message_id,
			last_read_at = excluded.last_read_at,
			updated_at = CURRENT_TIMESTAMP`,
		chatJID, messageID, lastReadAt,
	)
	if err != nil {
		return fmt.Errorf("set read position: %w", err)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS chats (
	jid TEXT PRIMARY KEY,
	name TEXT,
	last_message_time TIMESTAMP,
	archived BOOLEAN NOT NULL DEFAULT 0,
	pinned BOOLEAN NOT NULL DEFAULT 0,
	muted_until TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT,
	chat_jid TEXT,
	sender TEXT,
	content TEXT,
	timestamp TIMESTAMP,
	is_from_me BOOLEAN,
	media_type TEXT,
	filename TEXT,
	url TEXT,
	direct_path TEXT,
	media_key BLOB,
	file_sha256 BLOB,
	file_enc_sha256 BLOB,
	file_length INTEGER,
	poll_options_json TEXT,
	is_starred BOOLEAN NOT NULL DEFAULT 0,
	PRIMARY KEY (id, chat_jid),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS poll_votes (
	poll_message_id TEXT NOT NULL,
	poll_chat_jid TEXT NOT NULL,
	voter_jid TEXT NOT NULL,
	selected_options_json TEXT NOT NULL,
	timestamp TIMESTAMP NOT NULL,
	PRIMARY KEY (poll_message_id, poll_chat_jid, voter_jid)
);

CREATE TABLE IF NOT EXISTS labels (
	label_id TEXT PRIMARY KEY,
	name TEXT,
	color TEXT
);

CREATE TABLE IF NOT EXISTS label_associations (
	chat_jid TEXT NOT NULL,
	label_id TEXT NOT NULL,
	labeled BOOLEAN NOT NULL DEFAULT 1,
	PRIMARY KEY (chat_jid, label_id),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid),
	FOREIGN KEY (label_id) REFERENCES labels(label_id)
);

CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	tool_name TEXT NOT NULL,
	chat_jid TEXT,
	message_id TEXT,
	action TEXT NOT NULL,
	result TEXT NOT NULL,
	details TEXT
);

CREATE TABLE IF NOT EXISTS message_read_positions (
	chat_jid TEXT PRIMARY KEY,
	last_read_message_id TEXT,
	last_read_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS contacts (
	jid TEXT PRIMARY KEY,
	name TEXT,
	phone_number TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS contact_profiles (
	jid             TEXT PRIMARY KEY,
	username        TEXT,
	name_source     TEXT,
	addressing_mode TEXT,
	stale           INTEGER NOT NULL DEFAULT 0,
	updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS identity_evidence (
	lid         TEXT NOT NULL,
	pn          TEXT NOT NULL,
	source      TEXT NOT NULL,
	scope       TEXT NOT NULL DEFAULT '',
	learned_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	verified    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (lid, pn, source)
);

CREATE TABLE IF NOT EXISTS scheduled_messages (
	id TEXT PRIMARY KEY,
	chat_jid TEXT NOT NULL,
	content TEXT NOT NULL,
	media_path TEXT NOT NULL DEFAULT '',
	attempts INTEGER NOT NULL DEFAULT 0,
	scheduled_at TIMESTAMP NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	sent_at TIMESTAMP,
	sent_message_id TEXT,
	error TEXT,
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS presence (
	jid TEXT PRIMARY KEY,
	online BOOLEAN NOT NULL DEFAULT 0,
	last_seen TIMESTAMP,
	subscribed_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS auto_responder (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_scheduled_messages_scheduled_at ON scheduled_messages(scheduled_at);
CREATE INDEX IF NOT EXISTS idx_messages_chat_timestamp ON messages(chat_jid, timestamp DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_sender_timestamp ON messages(sender, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_messages_media_type ON messages(media_type, timestamp DESC) WHERE media_type != '';
CREATE INDEX IF NOT EXISTS idx_messages_is_from_me ON messages(is_from_me, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp DESC);

`

func migrateSchema(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return fmt.Errorf("pragma table_info(messages): %w", err)
	}
	defer rows.Close()
	hasPollOptions := false
	hasDirectPath := false
	hasStarred := false
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scan table_info(messages): %w", err)
		}
		if name == "poll_options_json" {
			hasPollOptions = true
		}
		if name == "direct_path" {
			hasDirectPath = true
		}
		if name == "is_starred" {
			hasStarred = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info(messages): %w", err)
	}
	if !hasPollOptions {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN poll_options_json TEXT"); err != nil {
			return fmt.Errorf("add column poll_options_json: %w", err)
		}
	}
	if !hasDirectPath {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN direct_path TEXT"); err != nil {
			return fmt.Errorf("add column direct_path: %w", err)
		}
	}
	if !hasStarred {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN is_starred BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add column is_starred: %w", err)
		}
	}
	chatRows, err := db.Query("PRAGMA table_info(chats)")
	if err != nil {
		return fmt.Errorf("pragma table_info(chats): %w", err)
	}
	defer chatRows.Close()
	hasArchived, hasChatPinned, hasChatMuted := false, false, false
	for chatRows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := chatRows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scan table_info(chats): %w", err)
		}
		switch name {
		case "archived":
			hasArchived = true
		case "pinned":
			hasChatPinned = true
		case "muted_until":
			hasChatMuted = true
		}
	}
	if err := chatRows.Err(); err != nil {
		return fmt.Errorf("iterate table_info(chats): %w", err)
	}
	if !hasArchived {
		if _, err := db.Exec("ALTER TABLE chats ADD COLUMN archived BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add column archived: %w", err)
		}
	}
	if !hasChatPinned {
		if _, err := db.Exec("ALTER TABLE chats ADD COLUMN pinned BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add column pinned: %w", err)
		}
	}
	if !hasChatMuted {
		if _, err := db.Exec("ALTER TABLE chats ADD COLUMN muted_until TIMESTAMP"); err != nil {
			return fmt.Errorf("add column muted_until: %w", err)
		}
	}
	if err := chatRows.Close(); err != nil {
		return fmt.Errorf("close table_info(chats): %w", err)
	}
	schedRows, err := db.Query("PRAGMA table_info(scheduled_messages)")
	if err != nil {
		return fmt.Errorf("pragma table_info(scheduled_messages): %w", err)
	}
	var hasScheduleError, hasScheduleMediaPath, hasScheduleAttempts bool
	for schedRows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := schedRows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			schedRows.Close()
			return fmt.Errorf("scan table_info(scheduled_messages): %w", err)
		}
		switch name {
		case "error":
			hasScheduleError = true
		case "media_path":
			hasScheduleMediaPath = true
		case "attempts":
			hasScheduleAttempts = true
		}
	}
	if err := schedRows.Close(); err != nil {
		return fmt.Errorf("close table_info(scheduled_messages): %w", err)
	}
	var schedTable string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='scheduled_messages'").Scan(&schedTable); err == nil {
		if !hasScheduleError {
			if _, err := db.Exec("ALTER TABLE scheduled_messages ADD COLUMN error TEXT"); err != nil {
				return fmt.Errorf("add column error: %w", err)
			}
		}
		if !hasScheduleMediaPath {
			if _, err := db.Exec("ALTER TABLE scheduled_messages ADD COLUMN media_path TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("add column media_path: %w", err)
			}
		}
		if !hasScheduleAttempts {
			if _, err := db.Exec("ALTER TABLE scheduled_messages ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("add column attempts: %w", err)
			}
		}
	}
	var arTable string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='auto_responder'").Scan(&arTable); err != nil {
	} else {
		arRows, err := db.Query("PRAGMA table_info(auto_responder)")
		if err != nil {
			return fmt.Errorf("pragma table_info(auto_responder): %w", err)
		}
		hasStartTime := false
		hasEndTime := false
		hasCoolDown := false
		for arRows.Next() {
			var (
				cid        int
				name       string
				ctype      string
				notnull    int
				dfltValue  sql.NullString
				primaryKey int
			)
			if err := arRows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
				arRows.Close()
				return fmt.Errorf("scan table_info(auto_responder): %w", err)
			}
			switch name {
			case "start_time":
				hasStartTime = true
			case "end_time":
				hasEndTime = true
			case "cooldown_minutes":
				hasCoolDown = true
			}
		}
		arRows.Close()
		if err := arRows.Err(); err != nil {
			return fmt.Errorf("iterate table_info(auto_responder): %w", err)
		}
		if !hasStartTime {
			if _, err := db.Exec("ALTER TABLE auto_responder ADD COLUMN start_time TEXT"); err != nil {
				return fmt.Errorf("add column start_time: %w", err)
			}
		}
		if !hasEndTime {
			if _, err := db.Exec("ALTER TABLE auto_responder ADD COLUMN end_time TEXT"); err != nil {
				return fmt.Errorf("add column end_time: %w", err)
			}
		}
		if !hasCoolDown {
			if _, err := db.Exec("ALTER TABLE auto_responder ADD COLUMN cooldown_minutes INTEGER DEFAULT 0"); err != nil {
				return fmt.Errorf("add column cooldown_minutes: %w", err)
			}
		}
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS auto_responder_cooldown (
			jid         TEXT PRIMARY KEY,
			last_reply  TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create table auto_responder_cooldown: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS contact_profiles (
			jid             TEXT PRIMARY KEY,
			username        TEXT,
			name_source     TEXT,
			addressing_mode TEXT,
			stale           INTEGER NOT NULL DEFAULT 0,
			updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create table contact_profiles: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS identity_evidence (
			lid         TEXT NOT NULL,
			pn          TEXT NOT NULL,
			source      TEXT NOT NULL,
			scope       TEXT NOT NULL DEFAULT '',
			learned_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			verified    INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (lid, pn, source)
		)
	`); err != nil {
		return fmt.Errorf("create table identity_evidence: %w", err)
	}
	return nil
}
